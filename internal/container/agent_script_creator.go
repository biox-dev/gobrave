package container

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/tool/builtin"
	"github.com/biox-dev/gobrave/internal/analysisform"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/dag/prepare"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// defaultIOSchemaJSON 是新建脚本默认写入 io_schema.json 的内容。
//
// 与 handler.GetDefaultIOSchame 保持同一口径（一个输入、一个输出），但不直接引用
// handler 包：container 已是 handler 的下游，再反向依赖会形成循环引用。
const defaultIOSchemaJSON = `{
    "inputs": [
        {
            "label": "input1",
            "name": "input1",
            "type": "BaseInput",
            "required": true
        }
    ],
    "outputs": [
        {
            "name": "output",
            "type": "file"
        }
    ],
    "params": [],
    "resources": {
        "cpu": 4,
        "memory": "6GB"
    }
}
`

// defaultIOSchemaResourcesJSON 是新建脚本 io_schema 的默认 resources 段（与 GetDefaultIOSchame 一致）。
const defaultIOSchemaResourcesJSON = `{
    "cpu": 4,
    "memory": "6GB"
}`

// buildIOSchemaJSON 把工具传入的 inputs / outputs 组装成完整的 io_schema.json 文本。
//
// inputs / outputs 都为空时直接返回 defaultIOSchemaJSON（保持既有默认口径）。
// 只提供其中一侧时，另一侧回落默认值：inputs 回落到默认的单个 input1，outputs 回落到
// 默认的 output。条目内缺失的字段按如下规则补齐（与 utils.FormatIOSchemaItems 的消费
// 口径一致：name 是参数键，type / required / label 会展示在人类用户的表单上）：
//   - name / type 必须有值，name 为空的条目被丢弃；
//   - label 缺失时取 name；inputs 的 required 缺失时按 true；
//   - outputs 的 type 缺失时按 file。
func buildIOSchemaJSON(inputs, outputs []map[string]any) (string, error) {
	normalizedInputs := normalizeIOSchemaSide(inputs, "BaseInput", true)
	normalizedOutputs := normalizeIOSchemaSide(outputs, "file", false)
	if len(normalizedInputs) == 0 && len(normalizedOutputs) == 0 {
		return defaultIOSchemaJSON, nil
	}

	var defaultSchema map[string]any
	if err := json.Unmarshal([]byte(defaultIOSchemaJSON), &defaultSchema); err != nil {
		return "", fmt.Errorf("create_script: invalid builtin default io_schema: %w", err)
	}

	schema := map[string]any{
		"inputs":  normalizedInputs,
		"outputs": normalizedOutputs,
	}
	if len(normalizedInputs) == 0 {
		schema["inputs"] = defaultSchema["inputs"]
	}
	if len(normalizedOutputs) == 0 {
		schema["outputs"] = defaultSchema["outputs"]
	}
	schema["params"] = []any{}
	var resources map[string]any
	if err := json.Unmarshal([]byte(defaultIOSchemaResourcesJSON), &resources); err == nil {
		schema["resources"] = resources
	}

	raw, err := json.MarshalIndent(schema, "", "    ")
	if err != nil {
		return "", fmt.Errorf("create_script: failed to marshal io_schema: %w", err)
	}
	return string(raw) + "\n", nil
}

// normalizeIOSchemaSide 清洗 inputs / outputs 的一侧；名称重复时以后出现的为准。
func normalizeIOSchemaSide(items []map[string]any, defaultType string, defaultRequired bool) []map[string]any {
	if len(items) == 0 {
		return nil
	}
	seen := make(map[string]int, len(items))
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		name := strings.TrimSpace(fmt.Sprintf("%v", item["name"]))
		if name == "" || name == "<nil>" {
			continue
		}
		normalized := make(map[string]any, len(item)+3)
		for k, v := range item {
			normalized[k] = v
		}
		normalized["name"] = name
		if v, _ := normalized["type"].(string); strings.TrimSpace(v) == "" {
			normalized["type"] = defaultType
		}
		if v, _ := normalized["label"].(string); strings.TrimSpace(v) == "" {
			normalized["label"] = name
		}
		if _, ok := normalized["required"]; !ok {
			normalized["required"] = defaultRequired
		}
		if idx, ok := seen[name]; ok {
			out[idx] = normalized
			continue
		}
		seen[name] = len(out)
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// defaultScriptInitContent 是新建脚本主文件的初始内容占位（按脚本类型区分）。
func defaultScriptInitContent(scriptType string) string {
	switch strings.ToLower(strings.TrimSpace(scriptType)) {
	case "jupyter":
		return "{\n \"cells\": [],\n \"metadata\": {},\n \"nbformat\": 4,\n \"nbformat_minor\": 5\n}\n"
	default:
		return "\n"
	}
}

// scriptCreator 实现 tool.ScriptCreator：把「创建脚本」与「为脚本创建独立分析节点」
// 两个业务能力暴露给内置工具 create_script / create_analysis_node。
//
// 与 AnalysisHandler.SaveAnalysisNodeControllerWithScript 的独立节点分支保持同一口径：
// Script 落 pipeline_components，AnalysisNode 落 analysis_nodes（analysis_id=0 表示
// 独立节点），节点工作目录为 <base_dir>/data/<project>/analysis_node/<scriptPK>/<nodeID>，
// 并由 NodeRuntimePreparer 生成 params.json / run.sh / env.sh 等运行期文件。
//
// 创建时不做 git 提交：脚本目录写完即处于 dirty 状态，与「保存组件」语义一致，
// 提交仍由用户通过发布 / 提交版本入口触发。
type scriptCreator struct {
	/* 依赖注入的服务能力 */
	workflowSvc  interfaces.WorkflowService
	analysisRepo interfaces.AnalysisRepository
	projectRepo  interfaces.ProjectRepository
	// containerRepo 用于解析默认容器模板（create_script 未传 container_template_id 时）。
	containerRepo interfaces.ContainerRepository
	// dataSvc 用于按脚本表单解析 request_param（构建分析节点时）。
	dataSvc interfaces.DataService
	// preparer 负责写节点 params.json / run.sh / env.sh，并软链脚本与 io_schema.json。
	preparer prepare.NodeRuntimePreparer
	cfg      *config.Config
}

// newScriptCreator 构建「创建脚本 / 创建分析节点」执行器并注入内置工具体系。
//
// 返回值同时实现 tool.ScriptCreator（create_script / create_analysis_node）与
// tool.ScriptUpdater：创建与更新共享同一份脚本业务服务依赖，故由同一个对象承载
// （Options.ScriptCreator 一处注入即可）。
func newScriptCreator(
	workflowSvc interfaces.WorkflowService,
	analysisRepo interfaces.AnalysisRepository,
	projectRepo interfaces.ProjectRepository,
	containerRepo interfaces.ContainerRepository,
	dataSvc interfaces.DataService,
	preparer prepare.NodeRuntimePreparer,
	cfg *config.Config,
) *scriptCreator {
	return &scriptCreator{
		workflowSvc:   workflowSvc,
		analysisRepo:  analysisRepo,
		projectRepo:   projectRepo,
		containerRepo: containerRepo,
		dataSvc:       dataSvc,
		preparer:      preparer,
		cfg:           cfg,
	}
}

// CreateScript 只创建脚本（不创建分析节点），返回脚本 ID 与关键路径。
//
// 步骤：解析默认容器模板 → 落库 Script → 初始化脚本目录（主文件 + io_schema.json）。
// 分析节点由 CreateAnalysisNode 单独创建，因此这里不需要任何节点名称 / 节点目录。
func (c *scriptCreator) CreateScript(ctx context.Context, in tool.ScriptCreateInput) (tool.ScriptCreateResult, error) {
	if in.ProjectID <= 0 {
		return tool.ScriptCreateResult{}, fmt.Errorf("create_script: project_id is required")
	}
	if c.workflowSvc == nil || c.projectRepo == nil {
		return tool.ScriptCreateResult{}, fmt.Errorf("create_script: required services are not configured")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return tool.ScriptCreateResult{}, fmt.Errorf("create_script: name is required")
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
			return tool.ScriptCreateResult{}, err
		}
		containerTemplateID = resolved.ID
		containerTemplateName = resolved.Name
	} else if tpl, err := c.containerRepo.GetContainerTemplateByID(ctx, containerTemplateID); err == nil && tpl != nil {
		containerTemplateName = tpl.Name
	}

	baseDir := c.storageBaseDir()
	if baseDir == "" {
		return tool.ScriptCreateResult{}, fmt.Errorf("create_script: storage.base_dir is empty")
	}

	project, err := c.projectRepo.GetProjectByID(ctx, in.ProjectID)
	if err != nil {
		return tool.ScriptCreateResult{}, fmt.Errorf("create_script: failed to get project %d: %w", in.ProjectID, err)
	}
	if project == nil || project.ID == 0 {
		return tool.ScriptCreateResult{}, fmt.Errorf("create_script: project %d not found", in.ProjectID)
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
		return tool.ScriptCreateResult{}, fmt.Errorf("create_script: failed to create script: %w", err)
	}

	// 2) 初始化脚本目录：主文件 + io_schema.json。
	ioSchemaJSON, err := buildIOSchemaJSON(in.Inputs, in.Outputs)
	if err != nil {
		return tool.ScriptCreateResult{}, err
	}
	scriptDir, scriptFile, _ := utils.GetScriptFile(baseDir, project.ID, script.ScriptType, script.ScriptID)
	scriptFilePath := filepath.Join(scriptDir, scriptFile)
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		return tool.ScriptCreateResult{}, fmt.Errorf("create_script: failed to prepare script dir: %w", err)
	}
	if _, statErr := os.Stat(scriptFilePath); os.IsNotExist(statErr) {
		if err := os.WriteFile(scriptFilePath, []byte(defaultScriptInitContent(script.ScriptType)), 0o644); err != nil {
			return tool.ScriptCreateResult{}, fmt.Errorf("create_script: failed to create script file: %w", err)
		}
	}
	if err := utils.WriteScriptIOSchema(baseDir, project.ID, script.ScriptID, ioSchemaJSON); err != nil {
		return tool.ScriptCreateResult{}, fmt.Errorf("create_script: failed to write io_schema.json: %w", err)
	}

	return tool.ScriptCreateResult{
		ScriptID:              script.ID,
		ScriptKey:             script.ScriptID,
		ScriptName:            script.Name,
		ScriptFilePath:        scriptFilePath,
		IOSchemaPath:          utils.ScriptIOSchemaPath(baseDir, project.ID, script.ScriptID),
		ScriptType:            script.ScriptType,
		ContainerTemplateID:   containerTemplateID,
		ContainerTemplateName: containerTemplateName,
	}, nil
}

// CreateAnalysisNode 为已有脚本创建一个独立分析节点（create_analysis_node）。
//
// 与 handler 保存独立节点保持同一口径：脚本必须属于 project_id，request_param 非空时
// 按其解析 resolved_inputs 并写入节点 params.json。节点的 inputs_patterns /
// output_patterns 取自脚本 io_schema.json，因此该文件必须已经存在（由 CreateScript
// 初始化，或模型已经写好）——缺失时直接报错，而不是带空契约建出一个跑不通的节点。
func (c *scriptCreator) CreateAnalysisNode(ctx context.Context, in tool.AnalysisNodeCreateInput) (tool.AnalysisNodeCreateResult, error) {
	if in.ProjectID <= 0 {
		return tool.AnalysisNodeCreateResult{}, fmt.Errorf("create_analysis_node: project_id is required")
	}
	if in.ScriptID <= 0 {
		return tool.AnalysisNodeCreateResult{}, fmt.Errorf("create_analysis_node: script_id is required")
	}
	if c.workflowSvc == nil || c.analysisRepo == nil {
		return tool.AnalysisNodeCreateResult{}, fmt.Errorf("create_analysis_node: required services are not configured")
	}

	script, err := c.workflowSvc.GetScriptByID(ctx, in.ScriptID)
	if err != nil {
		return tool.AnalysisNodeCreateResult{}, fmt.Errorf("create_analysis_node: failed to get script %d: %w", in.ScriptID, err)
	}
	if script == nil || script.ID == 0 {
		return tool.AnalysisNodeCreateResult{}, fmt.Errorf("create_analysis_node: script %d not found", in.ScriptID)
	}
	if script.ProjectID != in.ProjectID {
		return tool.AnalysisNodeCreateResult{}, fmt.Errorf("create_analysis_node: script %d belongs to project %d, not the active project %d", script.ID, script.ProjectID, in.ProjectID)
	}

	node, err := c.createNode(ctx, script, in.RequestParam, in.NodeName)
	if err != nil {
		return tool.AnalysisNodeCreateResult{}, fmt.Errorf("create_analysis_node: %w", err)
	}

	// 脚本侧路径由脚本目录派生（与 create_script / update_script 同口径）：
	// 建好节点后模型经常要紧接着改脚本或 io_schema，这里一并回传免得多查一次。
	result := tool.AnalysisNodeCreateResult{
		AnalysisNodeID: node.ID,
		NodeName:       node.NodeName,
		ScriptID:       script.ID,
		ScriptName:     script.Name,
		ScriptType:     script.ScriptType,
		WorkspaceDir:   node.WorkspaceDir,
		ParamsPath:     node.ParamsPath,
		RequestParam:   in.RequestParam,
	}
	if baseDir := c.storageBaseDir(); baseDir != "" {
		scriptDir, scriptFile, _ := utils.GetScriptFile(baseDir, script.ProjectID, script.ScriptType, script.ScriptID)
		result.ScriptFilePath = filepath.Join(scriptDir, scriptFile)
		result.IOSchemaPath = utils.ScriptIOSchemaPath(baseDir, script.ProjectID, script.ScriptID)
	}
	return result, nil
}

// createNode 按脚本契约构建、落库并准备工作目录，返回最终节点。
//
// 构建逻辑（含读取脚本表单）收敛在 analysisform.NewAnalysisNodeFromScript（前端保存 /
// create_script / create_analysis_node 共用），这里只负责落库与生成运行期文件。
// io_schema.json 缺失时 NewAnalysisNodeFromScript 会返回读取错误，因此调用方无需再校验。
func (c *scriptCreator) createNode(ctx context.Context, script *types.Script, requestParam map[string]any, nodeName string) (*types.AnalysisNode, error) {
	built, err := analysisform.NewAnalysisNodeFromScript(ctx, c.dataSvc, c.workflowSvc, script, analysisform.AnalysisNodeSpec{
		ProjectID:      script.ProjectID,
		NodeName:       nodeName,
		RequestParam:   requestParam,
		CreationSource: analysisform.NodeCreationSourceAgent,
		BaseDir:        c.storageBaseDir(),
	})
	if err != nil {
		return nil, err
	}
	node := built.Node

	if err := c.analysisRepo.CreateAnalysisNodes(ctx, []*types.AnalysisNode{node}); err != nil {
		return nil, fmt.Errorf("failed to create analysis node: %w", err)
	}

	// 运行期文件（params.json / run.sh / env.sh 与软链）；有 request_param 时 params.json 即已就绪。
	if c.preparer != nil {
		node.HydrateDerivedPaths()
		if err := c.preparer.Prepare(prepare.WithSkipCleanOutput(ctx), node); err != nil {
			return nil, fmt.Errorf("failed to prepare node workspace: %w", err)
		}
	}
	return node, nil
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
