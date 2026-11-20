package agentscript

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool/builtin"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// scriptCreator 实现 runtimecontext.ScriptCreator：把「创建脚本」能力暴露给内置工具 create_script。
//
// 脚本与节点是两个独立资源：为脚本创建分析节点由 agentanalysis.ScriptNodeCreator
// （create_analysis_node_script）负责，本类型不触碰 AnalysisNode。
//
// Script 落 pipeline_components；脚本目录为
// <base_dir>/data/<project>/pipeline/script/<scriptID>/（由 types.Script.Files 推导）。
//
// 创建时不做 git 提交：脚本目录写完即处于 dirty 状态，与「保存组件」语义一致，
// 提交仍由用户通过发布 / 提交版本入口触发。
type scriptCreator struct {
	/* 依赖注入的服务能力 */
	workflowSvc interfaces.WorkflowService
	projectRepo interfaces.ProjectRepository
	// containerRepo 用于解析默认容器模板（create_script 未传 container_template_id 时）。
	containerRepo interfaces.ContainerRepository
	// resources 记录「当前会话 ↔ 脚本」的关联（创建 / 更新成功后写入）。
	resources agentinterfaces.SessionResourceService
	cfg       *config.Config
}

// NewCreator 构建「创建脚本」执行器并注入内置工具体系。
//
// 返回值同时实现 runtimecontext.ScriptCreator（create_script）与 runtimecontext.ScriptUpdater
// （update_script）：创建与更新共享同一份脚本业务服务依赖，故由同一个对象承载
// （Options.ScriptCreator 一处注入即可）。
func NewCreator(
	workflowSvc interfaces.WorkflowService,
	projectRepo interfaces.ProjectRepository,
	containerRepo interfaces.ContainerRepository,
	resources agentinterfaces.SessionResourceService,
	cfg *config.Config,
) runtimecontext.ScriptCreator {
	return &scriptCreator{
		workflowSvc:   workflowSvc,
		projectRepo:   projectRepo,
		containerRepo: containerRepo,
		resources:     resources,
		cfg:           cfg,
	}
}

// CreateScript 只创建脚本（不创建分析节点），返回脚本 ID 与关键路径。
//
// 步骤：解析默认容器模板 → 落库 Script → 初始化脚本目录（主文件 + io_schema.json）。
// 分析节点由 create_analysis_node_script（agentanalysis）单独创建，因此这里不需要任何节点名称 / 节点目录。
func (c *scriptCreator) CreateScript(ctx context.Context, in runtimecontext.ScriptCreateInput) (runtimecontext.ScriptCreateResult, error) {
	if in.ProjectID <= 0 {
		return runtimecontext.ScriptCreateResult{}, fmt.Errorf("create_script: project_id is required")
	}
	if c.workflowSvc == nil || c.projectRepo == nil {
		return runtimecontext.ScriptCreateResult{}, fmt.Errorf("create_script: required services are not configured")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return runtimecontext.ScriptCreateResult{}, fmt.Errorf("create_script: name is required")
	}
	scriptType := strings.ToLower(strings.TrimSpace(in.ScriptType))
	if scriptType == "" {
		scriptType = builtin.DefaultScriptType
	}

	// 未显式指定容器模板时回落到系统默认模板；没有默认模板则直接报错，
	// 让模型提示用户先到「容器模板」页把某个模板设为默认。
	containerTemplateID := in.ContainerTemplateID
	containerTemplateName := ""
	if containerTemplateID <= 0 {
		resolved, err := c.resolveDefaultContainerTemplate(ctx)
		if err != nil {
			return runtimecontext.ScriptCreateResult{}, err
		}
		containerTemplateID = resolved.ID
		containerTemplateName = resolved.Name
	} else if tpl, err := c.containerRepo.GetContainerTemplateByID(ctx, containerTemplateID); err == nil && tpl != nil {
		containerTemplateName = tpl.Name
	}

	baseDir := c.storageBaseDir()
	if baseDir == "" {
		return runtimecontext.ScriptCreateResult{}, fmt.Errorf("create_script: storage.base_dir is empty")
	}

	project, err := c.projectRepo.GetProjectByID(ctx, in.ProjectID)
	if err != nil {
		return runtimecontext.ScriptCreateResult{}, fmt.Errorf("create_script: failed to get project %d: %w", in.ProjectID, err)
	}
	if project == nil || project.ID == 0 {
		return runtimecontext.ScriptCreateResult{}, fmt.Errorf("create_script: project %d not found", in.ProjectID)
	}

	// 1) 落库 Script：ScriptID 是脚本目录名，必须唯一，因此这里生成 UUID。
	script := &types.Script{
		ScriptID:            uuid.NewString(),
		ProjectID:           project.ID,
		ComponentType:       "script",
		Name:                name,
		Description:         strings.TrimSpace(in.Description),
		ContainerTemplateID: containerTemplateID,
		ScriptType:          scriptType,
		Category:            "default",
	}
	if err := c.workflowSvc.CreateScript(ctx, script); err != nil {
		return runtimecontext.ScriptCreateResult{}, fmt.Errorf("create_script: failed to create script: %w", err)
	}

	// 记录「当前会话创建了该脚本」；关联失败不影响创建结果。
	if c.resources != nil {
		_ = c.resources.LinkFromContext(ctx, agenttypes.SessionResourceTypeScript, script.ID)
	}

	// 2) 初始化脚本目录：主文件 + io_schema.json（+ 可选 README.md）。
	//
	// 工具可直接传入三类文件正文（script_content / io_schema / readme），
	// 因此模型创建脚本时无需再走 coder write / bash；未传时保持原默认行为。
	files := script.Files(baseDir)
	if err := os.MkdirAll(files.Dir, 0o755); err != nil {
		return runtimecontext.ScriptCreateResult{}, fmt.Errorf("create_script: failed to prepare script dir: %w", err)
	}
	scriptContent := in.ScriptContent
	if strings.TrimSpace(scriptContent) == "" {
		scriptContent = defaultScriptInitContent(script.ScriptType)
	}
	if err := utils.WriteScriptFileContent(baseDir, project.ID, script.ScriptType, script.ScriptID, scriptContent); err != nil {
		return runtimecontext.ScriptCreateResult{}, fmt.Errorf("create_script: failed to create script file: %w", err)
	}

	ioSchemaJSON := in.IOSchema
	if strings.TrimSpace(ioSchemaJSON) == "" {
		generated, err := buildIOSchemaJSON(in.Inputs, in.Outputs)
		if err != nil {
			return runtimecontext.ScriptCreateResult{}, err
		}
		ioSchemaJSON = generated
	}
	if err := utils.WriteScriptIOSchema(baseDir, project.ID, script.ScriptID, ioSchemaJSON); err != nil {
		return runtimecontext.ScriptCreateResult{}, fmt.Errorf("create_script: failed to write io_schema.json: %w", err)
	}
	if strings.TrimSpace(in.Readme) != "" {
		if err := utils.WriteScriptReadme(baseDir, project.ID, script.ScriptID, in.Readme); err != nil {
			return runtimecontext.ScriptCreateResult{}, fmt.Errorf("create_script: failed to write README.md: %w", err)
		}
	}

	return runtimecontext.ScriptCreateResult{
		ScriptID:              script.ID,
		ScriptKey:             script.ScriptID,
		ScriptName:            script.Name,
		ScriptFilePath:        files.MainPath,
		IOSchemaPath:          files.IOSchemaFile,
		ScriptType:            script.ScriptType,
		ContainerTemplateID:   containerTemplateID,
		ContainerTemplateName: containerTemplateName,
	}, nil
}

// resolveDefaultContainerTemplate 返回系统默认容器模板（is_default = true 的绑定行）。
//
// 没有默认模板时返回带用户指引导的错误：模型会把错误原文回给用户，
// 用户据此到「容器模板」页设置默认模板后重试。
func (c *scriptCreator) resolveDefaultContainerTemplate(ctx context.Context) (*types.ContainerTemplate, error) {
	if c.containerRepo == nil {
		return nil, fmt.Errorf("create_script: no container_template_id was provided and the container repository is not available; please ask the user to configure a default container template")
	}

	definition, err := c.containerRepo.GetDefaultContainerTemplateDefinition(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("create_script: no container_template_id was provided and no default container template is configured; please ask the user to open the Container Template page and set one template as default")
		}
		return nil, fmt.Errorf("create_script: failed to resolve default container template: %w", err)
	}

	tpl, err := c.containerRepo.GetContainerTemplateByID(ctx, definition.ID)
	if err != nil {
		return nil, fmt.Errorf("create_script: failed to load default container template %d: %w", definition.ID, err)
	}
	if tpl == nil {
		return nil, fmt.Errorf("create_script: default container template %d not found; please set another template as default", definition.ID)
	}
	return tpl, nil
}

// storageBaseDir 返回存储根目录（容忍 cfg/Storage 为空）。
func (c *scriptCreator) storageBaseDir() string {
	if c == nil || c.cfg == nil || c.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(c.cfg.Storage.BaseDir)
}
