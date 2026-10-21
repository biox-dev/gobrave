package container

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/container/agentanalysis"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// 本文件实现 tool.ProjectResourceBrowser：「浏览项目内的脚本 / 分析节点」与「浏览全局容器模板」。
//
// 只读内置工具共用同一份实现：
//   - list_project_scripts：按 RuntimeContext.ProjectID 列出全部脚本；
//   - get_script：按 script_id 查看脚本名称 / 描述 / 主文件路径 / io_schema.json 路径；
//   - get_analysis_node：按 analysis_node_id 查看节点名称 / 工作目录 / params.json / 参数；
//   - list_container_templates：列出全部容器模板（definition ID + spec 的 name / description）；
//   - get_container_template：按 @container:<id> 引用查看绑定行的 display_name / description；
//   - get_dataset：按 @dataset:<id> 引用查看数据集名称 / 描述 / 工作目录绝对路径。
//
// 路径口径与 create_script / update_script 完全一致：脚本主文件走
// utils.GetScriptFile，io_schema 走 utils.WorkflowIOSchemaPath（工作流与脚本共用同一份
// io_schema.json 布局），节点工作目录与 params.json 取自 analysisRepository 在读取时
// 还原的绝对路径（WorkspaceDir 落库为相对 storage.base_dir 的路径）。

// projectResourceBrowser 实现 tool.ProjectResourceBrowser。
type projectResourceBrowser struct {
	workflowSvc  interfaces.WorkflowService
	analysisRepo interfaces.AnalysisRepository
	containerSvc interfaces.ContainerService
	dataSvc      interfaces.DataService
	cfg          *config.Config
}

// newProjectResourceBrowser 构建「浏览项目内脚本 / 分析节点 / 容器模板 / 数据集」执行器并注入内置工具体系。
func newProjectResourceBrowser(workflowSvc interfaces.WorkflowService, analysisRepo interfaces.AnalysisRepository, containerSvc interfaces.ContainerService, dataSvc interfaces.DataService, cfg *config.Config) tool.ProjectResourceBrowser {
	return &projectResourceBrowser{
		workflowSvc:  workflowSvc,
		analysisRepo: analysisRepo,
		containerSvc: containerSvc,
		dataSvc:      dataSvc,
		cfg:          cfg,
	}
}

// ListScriptsByProject 返回指定项目下的全部脚本（按 id 倒序）。
func (b *projectResourceBrowser) ListScriptsByProject(ctx context.Context, projectID int64) ([]tool.ScriptBrief, error) {
	if projectID <= 0 {
		return nil, fmt.Errorf("list_project_scripts: project_id is required")
	}
	if b.workflowSvc == nil {
		return nil, fmt.Errorf("list_project_scripts: required services are not configured")
	}

	// 项目内脚本数量有限，用足够大的页长一次性取回，避免模型还要拼分页参数。
	pagination := &types.Pagination{Page: 1, PageSize: 1000}
	query := &types.ScriptPageQuery{ProjectID: projectID}
	scripts, _, err := b.workflowSvc.PageScript(ctx, pagination, query)
	if err != nil {
		return nil, fmt.Errorf("list_project_scripts: failed to list scripts: %w", err)
	}

	baseDir := b.storageBaseDir()
	out := make([]tool.ScriptBrief, 0, len(scripts))
	for _, script := range scripts {
		if script == nil {
			continue
		}
		out = append(out, b.toScriptBrief(baseDir, script))
	}
	return out, nil
}

// GetScriptByID 按主键读取脚本，并校验其属于当前激活项目。
func (b *projectResourceBrowser) GetScriptByID(ctx context.Context, projectID int64, scriptID int64) (tool.ScriptBrief, error) {
	if projectID <= 0 {
		return tool.ScriptBrief{}, fmt.Errorf("get_script: project_id is required")
	}
	if scriptID <= 0 {
		return tool.ScriptBrief{}, fmt.Errorf("get_script: script_id is required")
	}
	if b.workflowSvc == nil {
		return tool.ScriptBrief{}, fmt.Errorf("get_script: required services are not configured")
	}

	script, err := b.workflowSvc.GetScriptByID(ctx, scriptID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tool.ScriptBrief{}, fmt.Errorf("get_script: script %d not found", scriptID)
		}
		return tool.ScriptBrief{}, fmt.Errorf("get_script: failed to load script: %w", err)
	}
	if script == nil || script.ID == 0 {
		return tool.ScriptBrief{}, fmt.Errorf("get_script: script %d not found", scriptID)
	}
	if script.ProjectID != projectID {
		return tool.ScriptBrief{}, fmt.Errorf("get_script: script %d does not belong to the active project %d", script.ID, projectID)
	}
	return b.toScriptBrief(b.storageBaseDir(), script), nil
}

// GetAnalysisNodeByID 按主键读取分析节点，并校验其属于当前激活项目。
func (b *projectResourceBrowser) GetAnalysisNodeByID(ctx context.Context, projectID int64, nodeID int64) (tool.AnalysisNodeBrief, error) {
	if projectID <= 0 {
		return tool.AnalysisNodeBrief{}, fmt.Errorf("get_analysis_node: project_id is required")
	}
	if nodeID <= 0 {
		return tool.AnalysisNodeBrief{}, fmt.Errorf("get_analysis_node: analysis_node_id is required")
	}
	if b.analysisRepo == nil {
		return tool.AnalysisNodeBrief{}, fmt.Errorf("get_analysis_node: required services are not configured")
	}

	// repository 读取时已把 WorkspaceDir 还原成绝对路径，并填充 ParamsPath 等派生路径。
	node, err := b.analysisRepo.GetAnalysisNodeByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tool.AnalysisNodeBrief{}, fmt.Errorf("get_analysis_node: analysis node %d not found", nodeID)
		}
		return tool.AnalysisNodeBrief{}, fmt.Errorf("get_analysis_node: failed to load node: %w", err)
	}
	if node == nil || node.ID == 0 {
		return tool.AnalysisNodeBrief{}, fmt.Errorf("get_analysis_node: analysis node %d not found", nodeID)
	}
	if node.ProjectID != projectID {
		return tool.AnalysisNodeBrief{}, fmt.Errorf("get_analysis_node: analysis node %d does not belong to the active project %d", node.ID, projectID)
	}

	// 兜底：个别读取路径可能没有填充派生路径，这里按 WorkspaceDir 再推导一次（幂等）。
	if (node.ParamsPath == "" || node.CommandPath == "") && node.WorkspaceDir != "" {
		node.HydrateDerivedPaths()
	}

	return tool.AnalysisNodeBrief{
		AnalysisNodeID:      node.ID,
		NodeName:            node.NodeName,
		CreationSource:      node.CreationSource,
		NodeType:            node.NodeType(),
		NodeTypeDescription: node.NodeTypeDescription(),
		ScriptID:            node.ScriptID,
		WorkspaceDir:        node.WorkspaceDir,
		CommandPath:         node.CommandPath,
		ParamsPath:          node.ParamsPath,
		Command:             node.Command,
		RequestParam:        agentanalysis.DecodeRequestParam(node.RequestParam),
	}, nil
}

// ListContainerTemplates 返回全部可运行容器模板：每行一个绑定行（definition），
// 名称 / 描述取自该绑定行关联的共享运行配置（spec）。
//
// ListContainerTemplate 的读模型会把 DisplayName 优先填进 Name，而本工具要求返回
// spec 自身的 Name，因此这里按 SpecID 回查 spec（同一分组的多个镜像只查一次）。
func (b *projectResourceBrowser) ListContainerTemplates(ctx context.Context) ([]tool.ContainerTemplateBrief, error) {
	if b.containerSvc == nil {
		return nil, fmt.Errorf("list_container_templates: required services are not configured")
	}

	templates, err := b.containerSvc.ListContainerTemplate(ctx)
	if err != nil {
		return nil, fmt.Errorf("list_container_templates: failed to list container templates: %w", err)
	}

	specs := make(map[int64]*types.ContainerTemplateSpec)
	resolved := make(map[int64]bool)
	out := make([]tool.ContainerTemplateBrief, 0, len(templates))
	for _, tpl := range templates {
		if tpl == nil {
			continue
		}

		brief := tool.ContainerTemplateBrief{
			DefinitionID: tpl.ID,
			SpecID:       tpl.SpecID,
		}
		if !resolved[tpl.SpecID] {
			spec, err := b.containerSvc.GetContainerTemplateSpecByID(ctx, tpl.SpecID)
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("list_container_templates: failed to load template spec %d: %w", tpl.SpecID, err)
			}
			specs[tpl.SpecID] = spec
			resolved[tpl.SpecID] = true
		}
		if spec := specs[tpl.SpecID]; spec != nil {
			brief.Name = spec.Name
			brief.Description = spec.Description
		}
		out = append(out, brief)
	}
	return out, nil
}

// GetContainerTemplateByID 按主键读取单个容器模板（供 get_container_template 解析 @container:<id>）。
//
// 返回绑定行自身的 DisplayName / Description（longtext），并额外带上 spec 名称与镜像信息，
// 便于模型确认“这个模板到底是什么、能不能跑当前任务”。
func (b *projectResourceBrowser) GetContainerTemplateByID(ctx context.Context, containerTemplateID int64) (tool.ContainerTemplateDetail, error) {
	if containerTemplateID <= 0 {
		return tool.ContainerTemplateDetail{}, fmt.Errorf("get_container_template: container_template_id is required")
	}
	if b.containerSvc == nil {
		return tool.ContainerTemplateDetail{}, fmt.Errorf("get_container_template: required services are not configured")
	}

	tpl, err := b.containerSvc.GetContainerTemplateByID(ctx, containerTemplateID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tool.ContainerTemplateDetail{}, fmt.Errorf("get_container_template: container template %d not found", containerTemplateID)
		}
		return tool.ContainerTemplateDetail{}, fmt.Errorf("get_container_template: failed to load container template: %w", err)
	}
	if tpl == nil || tpl.ID == 0 {
		return tool.ContainerTemplateDetail{}, fmt.Errorf("get_container_template: container template %d not found", containerTemplateID)
	}

	detail := tool.ContainerTemplateDetail{
		DefinitionID: tpl.ID,
		SpecID:       tpl.SpecID,
		DisplayName:  tpl.DisplayName,
		Description:  tpl.DefinitionDescription,
		ImageName:    tpl.ImageName,
		PullPolicy:   tpl.PullPolicy,
		IsDefault:    tpl.IsDefault,
	}
	// 读模型里的 Name 可能是 DisplayName 的回退值，这里按 SpecID 回查共享配置名。
	if spec, specErr := b.containerSvc.GetContainerTemplateSpecByID(ctx, tpl.SpecID); specErr == nil && spec != nil {
		detail.SpecName = spec.Name
	}
	return detail, nil
}

// GetDatasetByID 按主键读取数据集（供 get_dataset 解析 @dataset:<id>）。
//
// 数据集按当前激活项目隔离：先校验 go_project_dataset 绑定，再返回 utils.GetDatasetDir
// 推导出的工作目录绝对路径，模型可直接在该目录下读写文件。
func (b *projectResourceBrowser) GetDatasetByID(ctx context.Context, projectID int64, datasetID int64) (tool.DatasetBrief, error) {
	if projectID <= 0 {
		return tool.DatasetBrief{}, fmt.Errorf("get_dataset: project_id is required")
	}
	if datasetID <= 0 {
		return tool.DatasetBrief{}, fmt.Errorf("get_dataset: dataset_id is required")
	}
	if b.dataSvc == nil {
		return tool.DatasetBrief{}, fmt.Errorf("get_dataset: required services are not configured")
	}

	dataset, err := b.dataSvc.GetDatasetByID(ctx, datasetID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tool.DatasetBrief{}, fmt.Errorf("get_dataset: dataset %d not found", datasetID)
		}
		return tool.DatasetBrief{}, fmt.Errorf("get_dataset: failed to load dataset: %w", err)
	}
	if dataset == nil || dataset.ID == 0 {
		return tool.DatasetBrief{}, fmt.Errorf("get_dataset: dataset %d not found", datasetID)
	}

	// 校验数据集属于当前激活项目（go_project_dataset 绑定）。
	bound, err := b.dataSvc.ListDatasetByProjectID(ctx, projectID)
	if err != nil {
		return tool.DatasetBrief{}, fmt.Errorf("get_dataset: failed to verify project binding: %w", err)
	}
	belongs := false
	for _, item := range bound {
		if item != nil && item.ID == datasetID {
			belongs = true
			break
		}
	}
	if !belongs {
		return tool.DatasetBrief{}, fmt.Errorf("get_dataset: dataset %d does not belong to the active project %d", datasetID, projectID)
	}

	baseDir := ""
	if b.cfg != nil && b.cfg.Storage != nil {
		baseDir = strings.TrimSpace(b.cfg.Storage.BaseDir)
	}
	if baseDir == "" {
		return tool.DatasetBrief{}, fmt.Errorf("get_dataset: storage base dir is not configured")
	}
	absBaseDir, err := utils.ResolveExternalPath(baseDir)
	if err != nil {
		return tool.DatasetBrief{}, fmt.Errorf("get_dataset: failed to resolve storage base dir: %w", err)
	}

	return tool.DatasetBrief{
		DatasetID:   dataset.ID,
		DatasetName: dataset.DatasetName,
		Description: dataset.Description,
		Path:        utils.GetDatasetDir(absBaseDir, projectID, datasetID),
	}, nil
}

// toScriptBrief 把 Script 记录转换成工具回传的摘要（补齐磁盘路径）。
func (b *projectResourceBrowser) toScriptBrief(baseDir string, script *types.Script) tool.ScriptBrief {
	brief := tool.ScriptBrief{
		ScriptID:            script.ID,
		ContainerTemplateID: script.ContainerTemplateID,
		ScriptName:          script.Name,
		Description:         script.Description,
		ScriptType:          script.ScriptType,
	}
	if baseDir == "" {
		return brief
	}
	scriptDir, scriptFile, _ := utils.GetScriptFile(baseDir, script.ProjectID, script.ScriptType, script.ScriptID)
	brief.ScriptFilePath = filepath.Join(scriptDir, scriptFile)
	// 脚本与工作流共用同一份 io_schema.json 布局（<dir>/io_schema.json），故复用工作流侧的路径解析。
	brief.IOSchemaPath = utils.WorkflowIOSchemaPath(baseDir, script.ProjectID, script.ScriptID)
	return brief
}

// storageBaseDir 返回 storage.base_dir；未配置时回退 "."（与 repository 口径一致）。
func (b *projectResourceBrowser) storageBaseDir() string {
	if b.cfg != nil && b.cfg.Storage != nil {
		if base := strings.TrimSpace(b.cfg.Storage.BaseDir); base != "" {
			return base
		}
	}
	return "."
}
