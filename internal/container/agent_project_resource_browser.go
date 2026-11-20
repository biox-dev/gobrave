package container

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/container/agentanalysis"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// 本文件实现项目资源只读能力（按窄接口拆分：ScriptBrowser / AnalysisNodeBrowser /
// ContainerTemplateBrowser / DatasetBrowser / ProjectReportContextResolver）：
// 「浏览项目内的脚本 / 分析节点」与「浏览全局容器模板 / 数据集 / 报告上下文」。
//
// 只读内置工具共用同一份实现：
//   - list_project_scripts：按 RuntimeContext.ProjectID 列出全部脚本；
//   - get_script：按 script_id 查看脚本名称 / 描述 / 主文件路径 / io_schema.json 路径 / README.md 正文；
//   - get_analysis_node：按 analysis_node_id（兼容 @analysis_node:<id> 引用）查看节点名称 / 工作目录 / params.json / 输出目录 / 参数；
//   - list_project_workflow：按 RuntimeContext.ProjectID 列出全部工作流（摘要 + DAG 节点 / 连线数）；
//   - get_workflow：按 workflow_id（兼容 @workflow:<id> 引用）查看工作流元数据、DAG 定义与 io_schema 原文；
//   - get_analysis_workflow：按 analysis_id（兼容 @analysis:<id> 引用）查看一次分析运行的状态 / 参数 / 节点分布；
//   - list_container_templates：列出全部容器模板（definition ID + spec 的 name / description）；
//   - get_container_template：按 @container:<id> 引用查看绑定行的 display_name / description；
//   - get_dataset：按 @dataset:<id> 引用查看数据集名称 / 描述 / 工作目录绝对路径；
//   - get_project_database_path：按固定 dataset_key=DATABASES 准备并返回项目数据库目录；
//   - add_file_to_dataset：把文件登记到当前项目的数据集；
//   - get_report：按 @report:<id> 引用读回报告标题 / 正文文件路径 / 正文内容；
//   - get_report_context：按 @report:<id> 引用解析报告标题 / 工作目录 / 条目清单。
//
// 路径口径与 create_script / update_script 完全一致：脚本主文件 / io_schema.json / README.md
// 一律由 types.Script.Files（utils.ScriptLayout）推导，不在各调用点手写 join；
// 节点工作目录与 params.json 取自 analysisRepository 在读取时
// 还原的绝对路径（WorkspaceDir 落库为相对 storage.base_dir 的路径）。

// projectResourceBrowser 同时实现 runtimecontext 的多个窄接口，由 container 单点装配。
type projectResourceBrowser struct {
	workflowSvc  interfaces.WorkflowService
	analysisRepo interfaces.AnalysisRepository
	containerSvc interfaces.ContainerService
	dataSvc      interfaces.DataService
	// projectSvc / analysisSvc 供 get_report_context 解析报告条目
	// （标题 / 工作目录 / 各来源的结果文件路径），口径与 handler.RuntimeContextResolver
	// 的 projectReport 分支保持一致。
	//
	// 摘要条目只用到摘要的 ID / 标题 / 项目 ID，因此直接依赖 AISummaryRepository，
	// 而不是 interfaces.AISummaryService：后者依赖 *agentservice.AgentService，而
	// AgentService 又依赖由本对象构建的 agent client，注入 Service 会形成 DI 环。
	projectSvc  interfaces.ProjectService
	analysisSvc interfaces.AnalysisService
	summaryRepo interfaces.AISummaryRepository
	cfg         *config.Config
}

// newProjectResourceBrowser 构建「脚本 / 分析节点 / 容器模板 / 数据集 / 报告上下文」只读执行器。
//
// 返回具体类型：工具侧按消费者拆成窄接口（runtimecontext.ScriptBrowser 等），
// 由 buildAgentClient 单点装配到 builtin.Deps；容器内无需再维护组合接口。
func newProjectResourceBrowser(workflowSvc interfaces.WorkflowService, analysisRepo interfaces.AnalysisRepository, containerSvc interfaces.ContainerService, dataSvc interfaces.DataService, projectSvc interfaces.ProjectService, analysisSvc interfaces.AnalysisService, summaryRepo interfaces.AISummaryRepository, cfg *config.Config) *projectResourceBrowser {
	return &projectResourceBrowser{
		workflowSvc:  workflowSvc,
		analysisRepo: analysisRepo,
		containerSvc: containerSvc,
		dataSvc:      dataSvc,
		projectSvc:   projectSvc,
		analysisSvc:  analysisSvc,
		summaryRepo:  summaryRepo,
		cfg:          cfg,
	}
}

// ListScriptsByProject 返回指定项目下的全部脚本（按 id 倒序）。
func (b *projectResourceBrowser) ListScriptsByProject(ctx context.Context, projectID int64) ([]runtimecontext.ScriptBrief, error) {
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
	out := make([]runtimecontext.ScriptBrief, 0, len(scripts))
	for _, script := range scripts {
		if script == nil {
			continue
		}
		out = append(out, b.toScriptBrief(baseDir, script))
	}
	return out, nil
}

// GetScriptByID 按主键读取脚本，并校验其属于当前激活项目。
//
// 除摘要（含 README.md 路径）外，一并读回 README.md 正文：模型查看单个脚本时
// 无需再单独读盘就能知道脚本的用途与用法。README 不存在时正文为空；读取失败
// 不影响详情返回（路径仍在 readme_file 里，可自行读取）。
func (b *projectResourceBrowser) GetScriptByID(ctx context.Context, projectID int64, scriptID int64) (runtimecontext.ScriptDetail, error) {
	if projectID <= 0 {
		return runtimecontext.ScriptDetail{}, fmt.Errorf("get_script: project_id is required")
	}
	if scriptID <= 0 {
		return runtimecontext.ScriptDetail{}, fmt.Errorf("get_script: script_id is required")
	}
	if b.workflowSvc == nil {
		return runtimecontext.ScriptDetail{}, fmt.Errorf("get_script: required services are not configured")
	}

	script, err := b.workflowSvc.GetScriptByID(ctx, scriptID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.ScriptDetail{}, fmt.Errorf("get_script: script %d not found", scriptID)
		}
		return runtimecontext.ScriptDetail{}, fmt.Errorf("get_script: failed to load script: %w", err)
	}
	if script == nil || script.ID == 0 {
		return runtimecontext.ScriptDetail{}, fmt.Errorf("get_script: script %d not found", scriptID)
	}
	if script.ProjectID != projectID {
		return runtimecontext.ScriptDetail{}, fmt.Errorf("get_script: script %d does not belong to the active project %d", script.ID, projectID)
	}

	baseDir := b.storageBaseDir()
	detail := runtimecontext.ScriptDetail{ScriptBrief: b.toScriptBrief(baseDir, script)}
	if readme, readErr := utils.ReadScriptReadme(baseDir, script.ProjectID, script.ScriptID); readErr == nil {
		detail.ReadmeContent = string(readme)
	}
	return detail, nil
}

// ReadScriptFile 直接读取脚本目录下指定文件的正文。
func (b *projectResourceBrowser) ReadScriptFile(ctx context.Context, projectID int64, scriptID int64, file runtimecontext.ResourceFile) (runtimecontext.ResourceFileContent, error) {
	if projectID <= 0 {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read script file: project_id is required")
	}
	if scriptID <= 0 {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read script file: script_id is required")
	}
	if !isOneOfResourceFile(file, runtimecontext.ResourceFileScript, runtimecontext.ResourceFileIOSchema, runtimecontext.ResourceFileReadme) {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read script file: unsupported file %q", file)
	}
	if b.workflowSvc == nil {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read script file: required services are not configured")
	}

	script, err := b.workflowSvc.GetScriptByID(ctx, scriptID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.ResourceFileContent{}, fmt.Errorf("read script file: script %d not found", scriptID)
		}
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read script file: failed to load script: %w", err)
	}
	if script == nil || script.ID == 0 {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read script file: script %d not found", scriptID)
	}
	if script.ProjectID != projectID {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read script file: script %d does not belong to the active project %d", script.ID, projectID)
	}

	files := script.Files(b.storageBaseDir())
	path := ""
	switch file {
	case runtimecontext.ResourceFileScript:
		path = files.MainPath
	case runtimecontext.ResourceFileIOSchema:
		path = files.IOSchemaFile
	case runtimecontext.ResourceFileReadme:
		path = files.ReadmeFile
	}
	data, err := readFileIfExists(path)
	if err != nil {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read script file: %w", err)
	}
	return runtimecontext.NewResourceFileContent(file, path, data), nil
}

// GetAnalysisNodeByID 按主键读取分析节点，并校验其属于当前激活项目。
func (b *projectResourceBrowser) GetAnalysisNodeByID(ctx context.Context, projectID int64, nodeID int64) (runtimecontext.AnalysisNodeBrief, error) {
	if projectID <= 0 {
		return runtimecontext.AnalysisNodeBrief{}, fmt.Errorf("get_analysis_node: project_id is required")
	}
	if nodeID <= 0 {
		return runtimecontext.AnalysisNodeBrief{}, fmt.Errorf("get_analysis_node: analysis_node_id is required")
	}
	if b.analysisRepo == nil {
		return runtimecontext.AnalysisNodeBrief{}, fmt.Errorf("get_analysis_node: required services are not configured")
	}

	// repository 读取时已把 WorkspaceDir 还原成绝对路径，并填充 ParamsPath 等派生路径。
	node, err := b.analysisRepo.GetAnalysisNodeByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.AnalysisNodeBrief{}, fmt.Errorf("get_analysis_node: analysis node %d not found", nodeID)
		}
		return runtimecontext.AnalysisNodeBrief{}, fmt.Errorf("get_analysis_node: failed to load node: %w", err)
	}
	if node == nil || node.ID == 0 {
		return runtimecontext.AnalysisNodeBrief{}, fmt.Errorf("get_analysis_node: analysis node %d not found", nodeID)
	}
	if node.ProjectID != projectID {
		return runtimecontext.AnalysisNodeBrief{}, fmt.Errorf("get_analysis_node: analysis node %d does not belong to the active project %d", node.ID, projectID)
	}

	// 兜底：个别读取路径可能没有填充派生路径，这里按 WorkspaceDir 再推导一次（幂等）。
	if (node.ParamsPath == "" || node.CommandPath == "" || node.OutputDir == "") && node.WorkspaceDir != "" {
		node.HydrateDerivedPaths()
	}

	return runtimecontext.AnalysisNodeBrief{
		AnalysisNodeID:      node.ID,
		NodeName:            node.NodeName,
		CreationSource:      node.CreationSource,
		NodeType:            node.NodeType(),
		NodeTypeDescription: node.NodeTypeDescription(),
		ScriptID:            node.ScriptID,
		WorkspaceDir:        node.WorkspaceDir,
		CommandPath:         node.CommandPath,
		ParamsPath:          node.ParamsPath,
		OutputDir:           node.OutputDir,
		RequestParam:        agentanalysis.DecodeRequestParam(node.RequestParam),
	}, nil
}

// ReadAnalysisNodeFile 直接读取节点工作目录下指定文件的正文（command / params）。
func (b *projectResourceBrowser) ReadAnalysisNodeFile(ctx context.Context, projectID int64, nodeID int64, file runtimecontext.ResourceFile) (runtimecontext.ResourceFileContent, error) {
	if !isOneOfResourceFile(file, runtimecontext.ResourceFileCommand, runtimecontext.ResourceFileParams) {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis node file: unsupported file %q", file)
	}
	if b.analysisRepo == nil {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis node file: required services are not configured")
	}

	node, err := b.analysisRepo.GetAnalysisNodeByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis node file: analysis node %d not found", nodeID)
		}
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis node file: failed to load node: %w", err)
	}
	if node == nil || node.ID == 0 {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis node file: analysis node %d not found", nodeID)
	}
	if node.ProjectID != projectID {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis node file: analysis node %d does not belong to the active project %d", node.ID, projectID)
	}
	node.HydrateDerivedPaths()

	path := node.CommandPath
	if file == runtimecontext.ResourceFileParams {
		path = node.ParamsPath
	}
	data, err := readFileIfExists(path)
	if err != nil {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis node file: %w", err)
	}
	return runtimecontext.NewResourceFileContent(file, path, data), nil
}

// ListWorkflowsByProject 返回指定项目下的全部工作流（按 id 倒序，不分页）。
//
// 项目内工作流数量有限，用足够大的页长一次性取回，避免模型还要拼分页参数；
// 每项只带摘要与目录内文件路径，DAG / io_schema 正文留给 get_workflow 按需读取。
func (b *projectResourceBrowser) ListWorkflowsByProject(ctx context.Context, projectID int64) ([]runtimecontext.WorkflowBrief, error) {
	if projectID <= 0 {
		return nil, fmt.Errorf("list_project_workflow: project_id is required")
	}
	if b.workflowSvc == nil {
		return nil, fmt.Errorf("list_project_workflow: required services are not configured")
	}

	pagination := &types.Pagination{Page: 1, PageSize: 1000}
	query := &types.WorkflowPageQuery{ProjectID: projectID}
	workflows, _, err := b.workflowSvc.PageWorkflow(ctx, pagination, query)
	if err != nil {
		return nil, fmt.Errorf("list_project_workflow: failed to list workflows: %w", err)
	}

	baseDir := b.storageBaseDir()
	out := make([]runtimecontext.WorkflowBrief, 0, len(workflows))
	for _, workflow := range workflows {
		if workflow == nil {
			continue
		}
		out = append(out, b.workflowBrief(baseDir, workflow))
	}
	return out, nil
}

// GetWorkflowByID 按主键读取工作流，并校验其属于当前激活项目。
//
// dag_definition.json / io_schema.json / README.md 一律从工作流目录读取（目录内文件是
// 唯一数据源，不落库）：原始文本随详情一起回传，模型可直接据此编辑后再调用
// update_workflow；解析失败不影响详情返回（原始文本仍在）。
func (b *projectResourceBrowser) GetWorkflowByID(ctx context.Context, projectID int64, workflowID int64) (runtimecontext.WorkflowDetail, error) {
	if projectID <= 0 {
		return runtimecontext.WorkflowDetail{}, fmt.Errorf("get_workflow: project_id is required")
	}
	if workflowID <= 0 {
		return runtimecontext.WorkflowDetail{}, fmt.Errorf("get_workflow: workflow_id is required")
	}
	if b.workflowSvc == nil {
		return runtimecontext.WorkflowDetail{}, fmt.Errorf("get_workflow: required services are not configured")
	}

	workflow, err := b.workflowSvc.GetWorkflowByID(ctx, workflowID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.WorkflowDetail{}, fmt.Errorf("get_workflow: workflow %d not found", workflowID)
		}
		return runtimecontext.WorkflowDetail{}, fmt.Errorf("get_workflow: failed to load workflow: %w", err)
	}
	if workflow == nil || workflow.ID == 0 {
		return runtimecontext.WorkflowDetail{}, fmt.Errorf("get_workflow: workflow %d not found", workflowID)
	}
	if workflow.ProjectID != projectID {
		return runtimecontext.WorkflowDetail{}, fmt.Errorf("get_workflow: workflow %d does not belong to the active project %d", workflow.ID, projectID)
	}

	baseDir := b.storageBaseDir()
	detail := runtimecontext.WorkflowDetail{WorkflowBrief: b.workflowBrief(baseDir, workflow)}

	// DAG 定义：原文回传 + 引用脚本（节点 / 连线数已由 workflowBrief 填好）。
	if raw, readErr := utils.ReadWorkflowDagDefinitionFile(baseDir, workflow.ProjectID, workflow.WorkflowID); readErr == nil && len(raw) > 0 {
		detail.DagDefinition = strings.TrimSpace(string(raw))
	}
	if dagDefinition, parseErr := utils.ReadWorkflowDagDefinition(baseDir, workflow.ProjectID, workflow.WorkflowID); parseErr == nil {
		_, _, detail.Scripts = b.workflowGraphSummary(ctx, workflow.ProjectID, dagDefinition)
	}

	// 工作流级 io_schema：原文回传 + inputs 表单（运行分析时的参数键）。
	if raw, readErr := utils.ReadWorkflowIOSchemaFile(baseDir, workflow.ProjectID, workflow.WorkflowID); readErr == nil && len(raw) > 0 {
		detail.IOSchema = strings.TrimSpace(string(raw))
	}
	if schema, readErr := utils.ReadWorkflowIOSchema(baseDir, workflow.ProjectID, workflow.WorkflowID); readErr == nil {
		if inputs, ok := schema["inputs"].([]any); ok {
			detail.Inputs = inputs
		}
	}

	if readme, readErr := utils.ReadWorkflowReadme(baseDir, workflow.ProjectID, workflow.WorkflowID); readErr == nil {
		detail.ReadmeContent = string(readme)
	}
	return detail, nil
}

// ReadWorkflowFile 直接读取工作流目录下指定文件的正文
// （dag_definition / io_schema / readme）。
func (b *projectResourceBrowser) ReadWorkflowFile(ctx context.Context, projectID int64, workflowID int64, file runtimecontext.ResourceFile) (runtimecontext.ResourceFileContent, error) {
	if !isOneOfResourceFile(file, runtimecontext.ResourceFileDagDefinition, runtimecontext.ResourceFileIOSchema, runtimecontext.ResourceFileReadme) {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read workflow file: unsupported file %q", file)
	}
	if b.workflowSvc == nil {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read workflow file: required services are not configured")
	}

	workflow, err := b.workflowSvc.GetWorkflowByID(ctx, workflowID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.ResourceFileContent{}, fmt.Errorf("read workflow file: workflow %d not found", workflowID)
		}
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read workflow file: failed to load workflow: %w", err)
	}
	if workflow == nil || workflow.ID == 0 {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read workflow file: workflow %d not found", workflowID)
	}
	if workflow.ProjectID != projectID {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read workflow file: workflow %d does not belong to the active project %d", workflow.ID, projectID)
	}

	files := utils.WorkflowLayout(b.storageBaseDir(), workflow.ProjectID, workflow.WorkflowID)
	path := ""
	switch file {
	case runtimecontext.ResourceFileDagDefinition:
		path = files.DagDefinition
	case runtimecontext.ResourceFileIOSchema:
		path = files.IOSchemaFile
	case runtimecontext.ResourceFileReadme:
		path = files.ReadmeFile
	}
	data, err := readFileIfExists(path)
	if err != nil {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read workflow file: %w", err)
	}
	return runtimecontext.NewResourceFileContent(file, path, data), nil
}

// workflowGraphSummary 统计 DAG 的节点 / 连线数，并按 script_id 去重回查脚本名称。
//
// dag_definition 非法或缺字段时返回零值，不视为错误（原始文本仍会回传给模型）。
func (b *projectResourceBrowser) workflowGraphSummary(ctx context.Context, projectID int64, dagDefinition map[string]any) (int, int, []runtimecontext.WorkflowScriptBrief) {
	nodes, _ := dagDefinition["nodes"].([]any)
	edges, _ := dagDefinition["edges"].([]any)

	scripts := make([]runtimecontext.WorkflowScriptBrief, 0, len(nodes))
	seen := make(map[string]struct{}, len(nodes))
	for _, nodeAny := range nodes {
		node, ok := nodeAny.(map[string]any)
		if !ok {
			continue
		}
		scriptKey, _ := node["script_id"].(string)
		scriptKey = strings.TrimSpace(scriptKey)
		if scriptKey == "" {
			continue
		}
		if _, exists := seen[scriptKey]; exists {
			continue
		}
		seen[scriptKey] = struct{}{}
		if b.workflowSvc == nil {
			continue
		}
		script, err := b.workflowSvc.GetScriptByScriptID(ctx, projectID, scriptKey)
		if err != nil || script == nil || script.ID == 0 {
			continue
		}
		scripts = append(scripts, runtimecontext.WorkflowScriptBrief{
			ScriptID:   script.ID,
			ScriptKey:  script.ScriptID,
			ScriptName: script.Name,
		})
	}
	return len(nodes), len(edges), scripts
}

// GetAnalysisByID 按主键读取一次分析运行（nextflow 行），并校验其属于当前激活项目。
//
// WorkspaceDir 由 repository 还原成绝对路径，ParamsPath 按 WorkspaceDir 派生；
// 同时回传工作流名称与节点状态分布，便于模型判断这次运行当前处于什么阶段。
func (b *projectResourceBrowser) GetAnalysisByID(ctx context.Context, projectID int64, analysisID int64) (runtimecontext.AnalysisDetail, error) {
	if projectID <= 0 {
		return runtimecontext.AnalysisDetail{}, fmt.Errorf("get_analysis_workflow: project_id is required")
	}
	if analysisID <= 0 {
		return runtimecontext.AnalysisDetail{}, fmt.Errorf("get_analysis_workflow: analysis_id is required")
	}
	if b.analysisRepo == nil {
		return runtimecontext.AnalysisDetail{}, fmt.Errorf("get_analysis_workflow: required services are not configured")
	}

	analysis, err := b.analysisRepo.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.AnalysisDetail{}, fmt.Errorf("get_analysis_workflow: analysis %d not found", analysisID)
		}
		return runtimecontext.AnalysisDetail{}, fmt.Errorf("get_analysis_workflow: failed to load analysis: %w", err)
	}
	if analysis == nil || analysis.ID == 0 {
		return runtimecontext.AnalysisDetail{}, fmt.Errorf("get_analysis_workflow: analysis %d not found", analysisID)
	}
	if analysis.ProjectID != projectID {
		return runtimecontext.AnalysisDetail{}, fmt.Errorf("get_analysis_workflow: analysis %d does not belong to the active project %d", analysis.ID, projectID)
	}

	// 兜底：个别读取路径可能没有填充派生路径，这里按 WorkspaceDir 再推导一次（幂等）。
	if analysis.ParamsPath == "" && analysis.WorkspaceDir != "" {
		analysis.HydrateDerivedPaths()
	}

	brief := runtimecontext.AnalysisBrief{
		AnalysisID:    analysis.ID,
		AnalysisKey:   analysis.AnalysisID,
		AnalysisName:  analysis.AnalysisName,
		ProjectID:     analysis.ProjectID,
		WorkflowID:    analysis.WorkflowID,
		JobStatus:     analysis.JobStatus,
		SchedulerMode: types.NormalizeSchedulerMode(analysis.SchedulerMode),
		CacheType:     analysis.CacheType,
		WorkspaceDir:  analysis.WorkspaceDir,
		ParamsPath:    analysis.ParamsPath,
		RequestParam:  agentanalysis.DecodeRequestParam(analysis.RequestParam),
	}
	if b.workflowSvc != nil && analysis.WorkflowID > 0 {
		if workflow, wfErr := b.workflowSvc.GetWorkflowByID(ctx, analysis.WorkflowID); wfErr == nil && workflow != nil {
			brief.WorkflowName = workflow.Name
		}
	}

	nodes, nodeErr := b.analysisRepo.ListAnalysisNodesByAnalysisID(ctx, analysis.ID)
	if nodeErr == nil {
		counts := make(map[string]int, 8)
		for _, node := range nodes {
			if node == nil {
				continue
			}
			status := strings.TrimSpace(node.Status)
			if status == "" {
				status = "unknown"
			}
			counts[status]++
		}
		brief.NodeCount = len(nodes)
		if len(counts) > 0 {
			brief.NodeStatusCounts = counts
		}
	}

	return runtimecontext.AnalysisDetail{AnalysisBrief: brief}, nil
}

// ReadAnalysisFile 直接读取分析运行目录下指定文件的正文（params）。
func (b *projectResourceBrowser) ReadAnalysisFile(ctx context.Context, projectID int64, analysisID int64, file runtimecontext.ResourceFile) (runtimecontext.ResourceFileContent, error) {
	if file != runtimecontext.ResourceFileParams {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis file: unsupported file %q", file)
	}
	if b.analysisRepo == nil {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis file: required services are not configured")
	}

	analysis, err := b.analysisRepo.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis file: analysis %d not found", analysisID)
		}
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis file: failed to load analysis: %w", err)
	}
	if analysis == nil || analysis.ID == 0 {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis file: analysis %d not found", analysisID)
	}
	if analysis.ProjectID != projectID {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis file: analysis %d does not belong to the active project %d", analysis.ID, projectID)
	}
	analysis.HydrateDerivedPaths()

	data, err := readFileIfExists(analysis.ParamsPath)
	if err != nil {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis file: %w", err)
	}
	return runtimecontext.NewResourceFileContent(file, analysis.ParamsPath, data), nil
}

// workflowBrief 构造工作流的只读摘要：元数据 + 目录内文件路径 + DAG 节点 / 连线数。
//
// DAG 文件缺失 / 非法时节点数保持 0（不视为错误）；正文由调用方按需读取。
func (b *projectResourceBrowser) workflowBrief(baseDir string, workflow *types.Workflow) runtimecontext.WorkflowBrief {
	files := utils.WorkflowLayout(baseDir, workflow.ProjectID, workflow.WorkflowID)
	brief := runtimecontext.WorkflowBrief{
		WorkflowID:        workflow.ID,
		WorkflowKey:       workflow.WorkflowID,
		Name:              workflow.Name,
		Description:       workflow.Description,
		Category:          workflow.Category,
		ProjectID:         workflow.ProjectID,
		WorkflowDir:       files.Dir,
		DagDefinitionPath: files.DagDefinition,
		IOSchemaPath:      files.IOSchemaFile,
		ReadmeFile:        files.ReadmeFile,
	}
	if dagDefinition, err := utils.ReadWorkflowDagDefinition(baseDir, workflow.ProjectID, workflow.WorkflowID); err == nil {
		nodes, _ := dagDefinition["nodes"].([]any)
		edges, _ := dagDefinition["edges"].([]any)
		brief.NodeCount = len(nodes)
		brief.EdgeCount = len(edges)
	}
	return brief
}

// ListContainerTemplates 返回全部可运行容器模板：每行一个绑定行（definition），
// 名称 / 描述取自该绑定行关联的共享运行配置（spec）。
//
// ListContainerTemplate 的读模型会把 DisplayName 优先填进 Name，而本工具要求返回
// spec 自身的 Name，因此这里按 SpecID 回查 spec（同一分组的多个镜像只查一次）。
func (b *projectResourceBrowser) ListContainerTemplates(ctx context.Context) ([]runtimecontext.ContainerTemplateBrief, error) {
	if b.containerSvc == nil {
		return nil, fmt.Errorf("list_container_templates: required services are not configured")
	}

	templates, err := b.containerSvc.ListContainerTemplate(ctx)
	if err != nil {
		return nil, fmt.Errorf("list_container_templates: failed to list container templates: %w", err)
	}

	specs := make(map[int64]*types.ContainerTemplateSpec)
	resolved := make(map[int64]bool)
	out := make([]runtimecontext.ContainerTemplateBrief, 0, len(templates))
	for _, tpl := range templates {
		if tpl == nil {
			continue
		}

		brief := runtimecontext.ContainerTemplateBrief{
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
func (b *projectResourceBrowser) GetContainerTemplateByID(ctx context.Context, containerTemplateID int64) (runtimecontext.ContainerTemplateDetail, error) {
	if containerTemplateID <= 0 {
		return runtimecontext.ContainerTemplateDetail{}, fmt.Errorf("get_container_template: container_template_id is required")
	}
	if b.containerSvc == nil {
		return runtimecontext.ContainerTemplateDetail{}, fmt.Errorf("get_container_template: required services are not configured")
	}

	tpl, err := b.containerSvc.GetContainerTemplateByID(ctx, containerTemplateID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.ContainerTemplateDetail{}, fmt.Errorf("get_container_template: container template %d not found", containerTemplateID)
		}
		return runtimecontext.ContainerTemplateDetail{}, fmt.Errorf("get_container_template: failed to load container template: %w", err)
	}
	if tpl == nil || tpl.ID == 0 {
		return runtimecontext.ContainerTemplateDetail{}, fmt.Errorf("get_container_template: container template %d not found", containerTemplateID)
	}

	detail := runtimecontext.ContainerTemplateDetail{
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
func (b *projectResourceBrowser) GetDatasetByID(ctx context.Context, projectID int64, datasetID int64) (runtimecontext.DatasetBrief, error) {
	if projectID <= 0 {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("get_dataset: project_id is required")
	}
	if datasetID <= 0 {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("get_dataset: dataset_id is required")
	}
	if b.dataSvc == nil {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("get_dataset: required services are not configured")
	}

	dataset, err := b.dataSvc.GetDatasetByID(ctx, datasetID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.DatasetBrief{}, fmt.Errorf("get_dataset: dataset %d not found", datasetID)
		}
		return runtimecontext.DatasetBrief{}, fmt.Errorf("get_dataset: failed to load dataset: %w", err)
	}
	if dataset == nil || dataset.ID == 0 {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("get_dataset: dataset %d not found", datasetID)
	}

	// 校验数据集属于当前激活项目（go_project_dataset 绑定）。
	bound, err := b.dataSvc.ListDatasetByProjectID(ctx, projectID)
	if err != nil {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("get_dataset: failed to verify project binding: %w", err)
	}
	belongs := false
	for _, item := range bound {
		if item != nil && item.ID == datasetID {
			belongs = true
			break
		}
	}
	if !belongs {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("get_dataset: dataset %d does not belong to the active project %d", datasetID, projectID)
	}

	baseDir := ""
	if b.cfg != nil && b.cfg.Storage != nil {
		baseDir = strings.TrimSpace(b.cfg.Storage.BaseDir)
	}
	if baseDir == "" {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("get_dataset: storage base dir is not configured")
	}
	absBaseDir, err := utils.ResolveExternalPath(baseDir)
	if err != nil {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("get_dataset: failed to resolve storage base dir: %w", err)
	}

	return runtimecontext.DatasetBrief{
		DatasetID:   dataset.ID,
		DatasetName: dataset.DatasetName,
		Description: dataset.Description,
		Path:        utils.GetDatasetDir(absBaseDir, projectID, datasetID),
	}, nil
}

// EnsureDatasetByKey 返回当前激活项目下 dataset_key 匹配的数据集，不存在时创建
// （get_project_database_path）。
//
// 固定 key 的数据集（如 types.DatasetKeyDatabases）由 Agent 按需准备：先按
// go_project_dataset 绑定列出项目数据集并匹配 dataset_key，未命中时以 key 为名称
// 创建数据集并绑定到项目（CreateDataset 同时会确保工作目录存在）。第二个返回值
// 表示本次是否新建。
func (b *projectResourceBrowser) EnsureDatasetByKey(ctx context.Context, projectID int64, datasetKey string) (runtimecontext.DatasetBrief, bool, error) {
	const toolName = "get_project_database_path"
	key := strings.TrimSpace(datasetKey)
	if projectID <= 0 {
		return runtimecontext.DatasetBrief{}, false, fmt.Errorf("%s: project_id is required", toolName)
	}
	if key == "" {
		return runtimecontext.DatasetBrief{}, false, fmt.Errorf("%s: dataset_key is required", toolName)
	}
	if b.dataSvc == nil {
		return runtimecontext.DatasetBrief{}, false, fmt.Errorf("%s: required services are not configured", toolName)
	}

	datasets, err := b.dataSvc.ListDatasetByProjectID(ctx, projectID)
	if err != nil {
		return runtimecontext.DatasetBrief{}, false, fmt.Errorf("%s: failed to list project datasets: %w", toolName, err)
	}
	for _, dataset := range datasets {
		if dataset == nil || !strings.EqualFold(strings.TrimSpace(dataset.DatasetKey), key) {
			continue
		}
		brief, err := b.toDatasetBrief(toolName, projectID, dataset)
		return brief, false, err
	}

	dataset := &types.Dataset{
		DatasetKey:  key,
		DatasetName: key,
		Description: "由 Agent 自动创建的数据集（dataset_key=" + key + "）",
	}
	if err := b.dataSvc.CreateDataset(ctx, dataset, projectID); err != nil {
		return runtimecontext.DatasetBrief{}, false, fmt.Errorf("%s: failed to create dataset %q: %w", toolName, key, err)
	}

	brief, err := b.toDatasetBrief(toolName, projectID, dataset)
	return brief, true, err
}

// toDatasetBrief 把数据集映射为工具回传的摘要，Path 由 utils.GetDatasetDir 推导。
func (b *projectResourceBrowser) toDatasetBrief(toolName string, projectID int64, dataset *types.Dataset) (runtimecontext.DatasetBrief, error) {
	if dataset == nil || dataset.ID == 0 {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("%s: dataset is invalid", toolName)
	}
	absBaseDir, err := utils.ResolveExternalPath(b.storageBaseDir())
	if err != nil {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("%s: failed to resolve storage base dir: %w", toolName, err)
	}
	return runtimecontext.DatasetBrief{
		DatasetID:   dataset.ID,
		DatasetName: dataset.DatasetName,
		Description: dataset.Description,
		Path:        utils.GetDatasetDir(absBaseDir, projectID, dataset.ID),
	}, nil
}

// AddFileToDataset 把文件登记到数据集（add_file_to_dataset）。
//
// 先校验数据集属于当前激活项目，再把相对路径解析到数据集工作目录，最后复用
// dataService.AddFileToDataset（source=analysis 表示 path 已是绝对路径）。
func (b *projectResourceBrowser) AddFileToDataset(ctx context.Context, in runtimecontext.DatasetFileAddInput) (runtimecontext.DatasetFileAddResult, error) {
	const toolName = "add_file_to_dataset"
	if in.ProjectID <= 0 {
		return runtimecontext.DatasetFileAddResult{}, fmt.Errorf("%s: project_id is required", toolName)
	}
	if in.DatasetID <= 0 {
		return runtimecontext.DatasetFileAddResult{}, fmt.Errorf("%s: dataset_id is required", toolName)
	}
	if b.dataSvc == nil {
		return runtimecontext.DatasetFileAddResult{}, fmt.Errorf("%s: required services are not configured", toolName)
	}

	path, err := b.resolveDatasetFilePath(toolName, in.ProjectID, in.DatasetID, in.Path)
	if err != nil {
		return runtimecontext.DatasetFileAddResult{}, err
	}

	// 数据集按当前激活项目隔离，先校验 go_project_dataset 绑定。
	datasets, err := b.dataSvc.ListDatasetByProjectID(ctx, in.ProjectID)
	if err != nil {
		return runtimecontext.DatasetFileAddResult{}, fmt.Errorf("%s: failed to verify project binding: %w", toolName, err)
	}
	belongs := false
	for _, dataset := range datasets {
		if dataset != nil && dataset.ID == in.DatasetID {
			belongs = true
			break
		}
	}
	if !belongs {
		return runtimecontext.DatasetFileAddResult{}, fmt.Errorf("%s: dataset %d does not belong to the active project %d", toolName, in.DatasetID, in.ProjectID)
	}

	result, err := b.dataSvc.AddFileToDataset(ctx, &types.AddFileToDatasetRequest{
		DatasetID: in.DatasetID,
		ProjectID: in.ProjectID,
		Path:      path,
		Role:      strings.TrimSpace(in.Role),
		FileName:  strings.TrimSpace(in.FileName),
		IsPrefix:  in.IsPrefix,
		IsCopy:    in.IsCopy,
		// analysis 表示 Path 已是待校验的绝对路径（相对路径已在上方按数据集目录解析）。
		Source: "analysis",
	})
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.DatasetFileAddResult{}, fmt.Errorf("%s: file %q not found under the project storage directory", toolName, path)
		}
		return runtimecontext.DatasetFileAddResult{}, fmt.Errorf("%s: failed to add file to dataset: %w", toolName, err)
	}

	out := runtimecontext.DatasetFileAddResult{
		DatasetID: in.DatasetID,
		FilePath:  path,
	}
	if result != nil && result.File != nil {
		out.FileID = result.File.ID
		out.FilePath = result.File.Path
		out.FileName = result.File.FileName
		out.Role = result.File.Role
		out.Format = result.File.Format
		out.Size = result.File.Size
	}
	return out, nil
}

// resolveDatasetFilePath 把工具入参的 path 解析为绝对路径：绝对路径原样返回，
// 相对路径相对数据集工作目录（utils.GetDatasetDir）解析。
func (b *projectResourceBrowser) resolveDatasetFilePath(toolName string, projectID int64, datasetID int64, rawPath string) (string, error) {
	path := strings.TrimSpace(rawPath)
	if path == "" {
		return "", fmt.Errorf("%s: path is required", toolName)
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	absBaseDir, err := utils.ResolveExternalPath(b.storageBaseDir())
	if err != nil {
		return "", fmt.Errorf("%s: failed to resolve storage base dir: %w", toolName, err)
	}
	return filepath.Join(utils.GetDatasetDir(absBaseDir, projectID, datasetID), path), nil
}

// GetReportByID 按主键读取项目报告及其正文（get_report）。
//
// 复用 service 的归属校验：GetProjectReportDetailByID 会校验报告所属项目已绑定
// 当前用户（越权时按 not found 处理），GetProjectReportFile 负责读 output.md
// （文件不存在时返回空串）。这里只额外校验报告属于当前激活项目，避免跨项目读文件。
func (b *projectResourceBrowser) GetReportByID(ctx context.Context, userID int64, projectID int64, reportID int64) (runtimecontext.ProjectReportDetail, error) {
	const toolName = "get_report"
	if userID <= 0 {
		return runtimecontext.ProjectReportDetail{}, fmt.Errorf("%s: user_id is required", toolName)
	}
	if projectID <= 0 {
		return runtimecontext.ProjectReportDetail{}, fmt.Errorf("%s: project_id is required", toolName)
	}
	if reportID <= 0 {
		return runtimecontext.ProjectReportDetail{}, fmt.Errorf("%s: report_id is required", toolName)
	}
	if b.projectSvc == nil {
		return runtimecontext.ProjectReportDetail{}, fmt.Errorf("%s: required services are not configured", toolName)
	}

	report, err := b.projectSvc.GetProjectReportDetailByID(ctx, userID, reportID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.ProjectReportDetail{}, fmt.Errorf("%s: project report %d not found", toolName, reportID)
		}
		return runtimecontext.ProjectReportDetail{}, fmt.Errorf("%s: failed to load project report: %w", toolName, err)
	}
	if report == nil || report.ID == 0 {
		return runtimecontext.ProjectReportDetail{}, fmt.Errorf("%s: project report %d not found", toolName, reportID)
	}
	// 报告按当前激活项目隔离，避免跨项目读到别的报告正文。
	if report.ProjectID != projectID {
		return runtimecontext.ProjectReportDetail{}, fmt.Errorf("%s: project report %d does not belong to the active project %d", toolName, reportID, projectID)
	}

	content, err := b.projectSvc.GetProjectReportFile(ctx, userID, reportID)
	if err != nil {
		return runtimecontext.ProjectReportDetail{}, fmt.Errorf("%s: failed to read report file: %w", toolName, err)
	}

	return runtimecontext.ProjectReportDetail{
		ReportID:  report.ID,
		ProjectID: report.ProjectID,
		Title:     report.Title,
		FilePath:  report.OutputFile(b.storageBaseDir()),
		Content:   content,
	}, nil
}

// GetReportContext 按报告 ID 解析报告标题 / 工作目录 / 输出文件与条目列表。
//
// 这段逻辑原先位于 handler.RuntimeContextResolver.Resolve 的 projectReport 分支，把解析
// 结果写进系统提示词变量 project_report_items；报告撰写改为由 get_report_context
// 工具在执行期按 reportID 拉取，故整体前移到这里（Resolve 中的对应代码后续废弃）。
func (b *projectResourceBrowser) GetReportContext(ctx context.Context, userID int64, projectID int64, reportID int64) (runtimecontext.ProjectReportContext, error) {
	const toolName = "get_report_context"
	if reportID <= 0 {
		return runtimecontext.ProjectReportContext{}, fmt.Errorf("%s: report_id is required", toolName)
	}
	if b.projectSvc == nil {
		return runtimecontext.ProjectReportContext{}, fmt.Errorf("%s: required services are not configured", toolName)
	}

	report, err := b.projectSvc.GetProjectReportByID(ctx, reportID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.ProjectReportContext{}, fmt.Errorf("%s: project report %d not found", toolName, reportID)
		}
		return runtimecontext.ProjectReportContext{}, fmt.Errorf("%s: failed to load project report: %w", toolName, err)
	}
	if report == nil || report.ID == 0 {
		return runtimecontext.ProjectReportContext{}, fmt.Errorf("%s: project report %d not found", toolName, reportID)
	}
	// 报告按当前激活项目隔离，避免跨项目读到别的报告目录。
	if projectID > 0 && report.ProjectID != projectID {
		return runtimecontext.ProjectReportContext{}, fmt.Errorf("%s: project report %d does not belong to the active project %d", toolName, reportID, projectID)
	}

	// 报告工作目录是报告撰写的事实工作区（git 仓库），使用前确保存在。
	baseDir := b.storageBaseDir()
	dir := report.WorkspaceDir(baseDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return runtimecontext.ProjectReportContext{}, fmt.Errorf("%s: failed to create report working directory: %w", toolName, err)
	}

	items, err := b.resolveProjectReportItems(ctx, userID, baseDir, report)
	if err != nil {
		return runtimecontext.ProjectReportContext{}, err
	}

	return runtimecontext.ProjectReportContext{
		ReportID:    report.ID,
		ProjectID:   report.ProjectID,
		ReportTitle: report.Title,
		WorkingDir:  dir,
		OutputFile:  report.OutputFile(baseDir),
		Items:       items,
	}, nil
}

// resolveProjectReportItems 读取报告下的全部条目并解析为模板可用的名称 / 内容。
// 单个条目解析失败（owner 记录缺失等）时跳过该条目并记录告警，
// 避免一个失效引用导致整份报告上下文不可用。
func (b *projectResourceBrowser) resolveProjectReportItems(ctx context.Context, userID int64, baseDir string, report *types.ProjectReport) ([]runtimecontext.ProjectReportItemBrief, error) {
	if report == nil {
		return nil, nil
	}
	if b.projectSvc == nil {
		return nil, fmt.Errorf("get_report_context: required services are not configured")
	}

	items, err := b.projectSvc.ListProjectReportItemsByReportID(ctx, userID, report.ID)
	if err != nil {
		return nil, fmt.Errorf("get_report_context: failed to list project report items: %w", err)
	}

	result := make([]runtimecontext.ProjectReportItemBrief, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		name, content, ok := b.resolveProjectReportItemVar(ctx, baseDir, report, item)
		if !ok {
			logger.Warnf(ctx, "[ProjectResourceBrowser] skip project report item, item_id=%d owner_type=%s owner_id=%d", item.ID, item.OwnerType, item.OwnerID)
			continue
		}
		result = append(result, runtimecontext.ProjectReportItemBrief{Name: name, Content: content, OwnerType: string(item.OwnerType)})
	}
	return result, nil
}

// projectReportOutputMarkdownPath 拼接 owner（Analysis / AnalysisNode）输出目录下的 output.md 路径。
// 目录为空时返回空串，避免模板拿到悬空的 "output.md"（与 utils 派生路径的空值口径一致）。
func projectReportOutputMarkdownPath(dir string) string {
	if strings.TrimSpace(dir) == "" {
		return ""
	}
	return filepath.Join(dir, types.DefaultProjectReportFilename)
}

// resolveProjectReportItemVar 按 owner 类型解析单个报告条目的名称与内容。
// 名称与内容的口径与 handler.RuntimeContextResolver.resolveProjectReportItemVar 一致：
//   - analysis / analysis_node：名称取 AnalysisName / NodeName，内容为工作目录 / 输出目录下的 output.md；
//   - ai_summary：名称取摘要标题，内容为摘要文件路径（utils.GetAISummaryFile）；
//   - custom：名称取用户编辑的标题，内容为用户编辑的正文。
//
// 返回 ok=false 表示无法解析（owner 缺失、服务不可用或 owner 类型不支持）。
func (b *projectResourceBrowser) resolveProjectReportItemVar(ctx context.Context, baseDir string, report *types.ProjectReport, item *types.ProjectReportItem) (string, string, bool) {
	switch item.OwnerType {
	case types.ProjectReportItemOwnerAnalysis:
		if b.analysisSvc == nil {
			return "", "", false
		}
		analysis, err := b.analysisSvc.GetAnalysisByID(ctx, item.OwnerID)
		if err != nil {
			return "", "", false
		}
		// 内容给到 Analysis 工作目录下的 output.md，而不是目录本身：报告撰写
		// 模板需要的是可直接读取的结果文件路径（与 ai_summary 条目的口径一致）。
		return analysis.AnalysisName, projectReportOutputMarkdownPath(analysis.WorkspaceDir), true

	case types.ProjectReportItemOwnerAnalysisNode:
		if b.analysisSvc == nil {
			return "", "", false
		}
		node, err := b.analysisSvc.GetAnalysisNodeByID(ctx, item.OwnerID)
		if err != nil {
			return "", "", false
		}
		// 同上：节点输出目录下的 output.md。
		return node.NodeName, projectReportOutputMarkdownPath(node.OutputDir), true

	case types.ProjectReportItemOwnerAISummary:
		if b.summaryRepo == nil {
			return "", "", false
		}
		summary, err := b.summaryRepo.GetAISummaryByID(ctx, item.OwnerID)
		if err != nil {
			return "", "", false
		}
		// 摘要文件路径需要项目 ID：优先用摘要记录上的 ProjectID，
		// 尚未回填时回退到报告所属项目，保证路径仍可定位。
		projectID := summary.ProjectID
		if projectID == 0 && report != nil {
			projectID = report.ProjectID
		}
		return summary.Title, utils.GetAISummaryFile(baseDir, projectID, summary.ID), true

	case types.ProjectReportItemOwnerCustom:
		return item.Title, item.Content, true

	default:
		return "", "", false
	}
}

// toScriptBrief 把 Script 记录转换成工具回传的摘要（补齐磁盘路径）。
func (b *projectResourceBrowser) toScriptBrief(baseDir string, script *types.Script) runtimecontext.ScriptBrief {
	brief := runtimecontext.ScriptBrief{
		ScriptID:            script.ID,
		ContainerTemplateID: script.ContainerTemplateID,
		ScriptName:          script.Name,
		Description:         script.Description,
		ScriptType:          script.ScriptType,
	}
	if baseDir == "" {
		return brief
	}
	files := script.Files(baseDir)
	brief.ScriptFilePath = files.MainPath
	brief.IOSchemaPath = files.IOSchemaFile
	brief.ReadmeFile = files.ReadmeFile
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

// readFileIfExists 读取文件正文；文件不存在时返回 nil（不视为错误）。
func readFileIfExists(path string) ([]byte, error) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// isOneOfResourceFile 判断 file 是否属于 allowed。
func isOneOfResourceFile(file runtimecontext.ResourceFile, allowed ...runtimecontext.ResourceFile) bool {
	for _, candidate := range allowed {
		if file == candidate {
			return true
		}
	}
	return false
}
