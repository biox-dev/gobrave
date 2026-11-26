package container

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/container/agentanalysis"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// 本文件实现项目资源只读能力（按窄接口拆分：ScriptBrowser / AnalysisNodeBrowser /
// ContainerTemplateBrowser / DatasetBrowser / ProjectReportBrowser）：
// 「浏览项目内的脚本 / 分析节点」与「浏览全局容器模板 / 数据集 / 报告正文」。
//
// 只读内置工具共用同一份实现：
//   - list_project_scripts：按 RuntimeContext.ProjectID 列出全部脚本；
//   - get_script：按 script_id 查看脚本名称 / 描述 / 主文件路径 / io_schema.json 路径 / README.md 正文；
//   - get_analysis_node：按 analysis_node_id（兼容 @analysis_node:<id> 引用）查看节点名称 / 工作目录 / params.json / 输出目录 / 参数，并直接回传系统自动登记到 go_file 的节点产物文件（output_files）；
//   - list_project_workflow：按 RuntimeContext.ProjectID 列出全部工作流（摘要 + DAG 节点 / 连线数）；
//   - get_workflow：按 workflow_id（兼容 @workflow:<id> 引用）查看工作流元数据、DAG 定义与 io_schema 原文；
//   - get_analysis_workflow：按 analysis_id（兼容 @analysis:<id> 引用）查看一次分析运行的状态 / 参数 / 节点分布；
//   - list_container_templates：列出全部容器模板（definition ID + 绑定行的 name / description）；
//   - get_container_template：按 @container:<id> 引用查看绑定行的 name / description；
//   - get_dataset：按 @dataset:<id> 引用查看数据集名称 / 描述 / 工作目录绝对路径；
//   - get_project_database_path：按固定 dataset_key=DATABASES 准备并返回项目数据库目录；
//   - add_file_to_dataset：把文件登记到当前项目的数据集；
//   - get_report：按 @report:<id> 引用读回报告标题 / 正文文件路径 / 正文内容。
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
	// projectSvc / analysisSvc / summaryRepo 供报告相关只读逻辑使用，
	// 口径与 handler.RuntimeContextResolver 的 projectReport 分支保持一致。
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

	return runtimecontext.AnalysisNodeBrief{
		AnalysisNodeID:      node.ID,
		NodeName:            node.NodeName,
		CreationSource:      node.CreationSource,
		NodeType:            node.NodeType(),
		NodeTypeDescription: node.NodeTypeDescription(),
		ScriptID:            node.ScriptID,
		WorkspaceDir:        node.WorkspaceDir,
		CommandPath:         node.CommandFile(),
		ParamsPath:          node.ParamsFile(),
		OutputDir:           node.OutputDirectory(),
		RequestParam:        agentanalysis.DecodeRequestParam(node.RequestParam),
	}, nil
}

// ListAnalysisNodeFiles 返回节点由系统自动登记到 go_file 的产物文件。
//
// go_file.analysis_node_id 由节点完成时的 recordNodeOutputFiles 写入
// （agent 命令节点来自 output/outputs.json，script / scheduler 节点来自
// output_patterns 中 type=file 的条目），这里只做只读读取，并校验节点属于当前激活项目。
// 由 get_analysis_node 的 output_files 使用。
func (b *projectResourceBrowser) ListAnalysisNodeFiles(ctx context.Context, projectID int64, nodeID int64) ([]runtimecontext.AnalysisNodeFile, error) {
	if projectID <= 0 {
		return nil, fmt.Errorf("get_analysis_node: project_id is required")
	}
	if nodeID <= 0 {
		return nil, fmt.Errorf("get_analysis_node: analysis_node_id is required")
	}
	if b.analysisRepo == nil || b.dataSvc == nil {
		return nil, fmt.Errorf("get_analysis_node: required services are not configured")
	}

	node, err := b.analysisRepo.GetAnalysisNodeByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("get_analysis_node: analysis node %d not found", nodeID)
		}
		return nil, fmt.Errorf("get_analysis_node: failed to load node: %w", err)
	}
	if node == nil || node.ID == 0 {
		return nil, fmt.Errorf("get_analysis_node: analysis node %d not found", nodeID)
	}
	if node.ProjectID != projectID {
		return nil, fmt.Errorf("get_analysis_node: analysis node %d does not belong to the active project %d", node.ID, projectID)
	}

	files, err := b.dataSvc.ListFileByAnalysisNodeID(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("get_analysis_node: failed to list files of node %d: %w", nodeID, err)
	}

	out := make([]runtimecontext.AnalysisNodeFile, 0, len(files))
	for _, file := range files {
		if file == nil {
			continue
		}
		createdAt := ""
		if !file.CreatedAt.IsZero() {
			createdAt = file.CreatedAt.Format(time.RFC3339)
		}
		out = append(out, runtimecontext.AnalysisNodeFile{
			FileID:      file.ID,
			FileName:    file.FileName,
			Path:        file.Path,
			Format:      file.Format,
			Role:        file.Role,
			FileKey:     file.FileKey,
			Size:        file.Size,
			MD5:         file.MD5,
			Description: file.Description,
			CreatedAt:   createdAt,
		})
	}
	return out, nil
}

// ReadAnalysisNodeFile 直接读取节点工作目录下指定文件的正文（command / params / output）。
//
// output 不是单个文件：它读取节点 output 目录下的产物正文（先用 output.md，再按登记顺序
// 拼接 go_file 中该节点已登记的产物），多个文件以 "===== <path> =====" 分隔，见
// readAnalysisNodeOutputBundle。
func (b *projectResourceBrowser) ReadAnalysisNodeFile(ctx context.Context, projectID int64, nodeID int64, file runtimecontext.ResourceFile) (runtimecontext.ResourceFileContent, error) {
	if !isOneOfResourceFile(file, runtimecontext.ResourceFileCommand, runtimecontext.ResourceFileParams, runtimecontext.ResourceFileOutput) {
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
	if file == runtimecontext.ResourceFileOutput {
		return b.readAnalysisNodeOutputBundle(ctx, node)
	}
	path := node.CommandFile()
	if file == runtimecontext.ResourceFileParams {
		path = node.ParamsFile()
	}
	data, err := readFileIfExists(path)
	if err != nil {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis node file: %w", err)
	}
	return runtimecontext.NewResourceFileContent(file, path, data), nil
}

const (
	// analysisNodeOutputBundleMaxBytes 是 files=["output"] 一次性回传产物正文的字节上限。
	analysisNodeOutputBundleMaxBytes = 200 * 1024
)

// readAnalysisNodeOutputBundle 读取节点 output 目录下的产物正文，拼成单份返回。
//
// 读取顺序：先 output/output.md（分析报告），再按登记顺序读取 go_file 中该节点已登记的
// 产物（跳过重复路径、二进制文件、不存在的文件）。每个文件前加 "===== <path> =====" 分隔，
// 总长度超过 analysisNodeOutputBundleMaxBytes 时截断并在末尾注明。
//
// 没有可读正文时返回 Exists=false，Path 指向节点 output 目录，模型可改用 get_file 按
// output_files 中的 file_id 读取具体产物。
func (b *projectResourceBrowser) readAnalysisNodeOutputBundle(ctx context.Context, node *types.AnalysisNode) (runtimecontext.ResourceFileContent, error) {
	outputDir := node.OutputDirectory()
	content := runtimecontext.ResourceFileContent{File: runtimecontext.ResourceFileOutput, Path: outputDir}
	if outputDir == "" {
		return content, nil
	}

	paths := make([]string, 0, 4)
	seen := make(map[string]struct{}, 4)
	addPath := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		key := filepath.Clean(path)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		paths = append(paths, path)
	}

	// 分析报告优先，即便它没有被登记到 go_file 也先读。
	addPath(node.OutputMarkdownFile())
	if b.dataSvc != nil {
		files, err := b.dataSvc.ListFileByAnalysisNodeID(ctx, node.ID)
		if err != nil {
			return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis node file: failed to list output files of node %d: %w", node.ID, err)
		}
		for _, file := range files {
			if file != nil {
				addPath(file.Path)
			}
		}
	}

	var builder strings.Builder
	written := 0
	for _, path := range paths {
		if written >= analysisNodeOutputBundleMaxBytes {
			break
		}
		data, err := readFileIfExists(path)
		if err != nil || data == nil {
			continue
		}
		// 跳过二进制产物（图片 / 表格文件等），它们的正文对模型无意义且可能很大。
		if bytes.IndexByte(data, 0) >= 0 {
			continue
		}
		text := string(data)
		remaining := analysisNodeOutputBundleMaxBytes - written
		truncated := false
		if len(text) > remaining {
			text = text[:remaining]
			truncated = true
		}
		fmt.Fprintf(&builder, "===== %s =====\n", path)
		builder.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			builder.WriteString("\n")
		}
		if truncated {
			builder.WriteString("... (truncated)\n")
		}
		written += len(text)
	}

	if written > 0 {
		content.Content = builder.String()
		content.Exists = true
	}
	return content, nil
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
		ParamsPath:    analysis.ParamsFile(),
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
	data, err := readFileIfExists(analysis.ParamsFile())
	if err != nil {
		return runtimecontext.ResourceFileContent{}, fmt.Errorf("read analysis file: %w", err)
	}
	return runtimecontext.NewResourceFileContent(file, analysis.ParamsFile(), data), nil
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
// 名称 / 描述直接取自读模型 ContainerTemplate（同源于绑定行本身），不再回查共享配置。
func (b *projectResourceBrowser) ListContainerTemplates(ctx context.Context) ([]runtimecontext.ContainerTemplateBrief, error) {
	if b.containerSvc == nil {
		return nil, fmt.Errorf("list_container_templates: required services are not configured")
	}

	templates, err := b.containerSvc.ListContainerTemplate(ctx)
	if err != nil {
		return nil, fmt.Errorf("list_container_templates: failed to list container templates: %w", err)
	}

	out := make([]runtimecontext.ContainerTemplateBrief, 0, len(templates))
	for _, tpl := range templates {
		if tpl == nil {
			continue
		}
		out = append(out, runtimecontext.ContainerTemplateBrief{
			DefinitionID: tpl.ID,
			SpecID:       tpl.SpecID,
			Name:         tpl.Name,
			Description:  tpl.Description,
		})
	}
	return out, nil
}

// GetContainerTemplateByID 按主键读取单个容器模板（供 get_container_template 解析 @container:<id>）。
//
// 返回绑定行自身的 Name / Description（longtext），并额外带上 spec 名称与镜像信息，
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
		Name:         tpl.Name,
		Description:  tpl.Description,
		ImageName:    tpl.ImageName,
		PullPolicy:   tpl.PullPolicy,
	}
	// 读模型里的 Name 是绑定行展示名，这里按 SpecID 回查共享配置名。
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

// ListDatasetsByProject 列出当前激活项目下的全部数据集（list_dataset）。
func (b *projectResourceBrowser) ListDatasetsByProject(ctx context.Context, projectID int64) ([]runtimecontext.DatasetBrief, error) {
	const toolName = "list_dataset"
	if projectID <= 0 {
		return nil, fmt.Errorf("%s: project_id is required", toolName)
	}
	if b.dataSvc == nil {
		return nil, fmt.Errorf("%s: required services are not configured", toolName)
	}

	datasets, err := b.dataSvc.ListDatasetByProjectID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("%s: failed to list datasets: %w", toolName, err)
	}

	out := make([]runtimecontext.DatasetBrief, 0, len(datasets))
	for _, dataset := range datasets {
		if dataset == nil || dataset.ID == 0 {
			continue
		}
		brief, briefErr := b.toDatasetBrief(toolName, projectID, dataset)
		if briefErr != nil {
			return nil, briefErr
		}
		out = append(out, brief)
	}
	return out, nil
}

// ListFilesByProject 列出当前激活项目下的文件（list_file），roles 为空表示不过滤。
func (b *projectResourceBrowser) ListFilesByProject(ctx context.Context, projectID int64, roles []types.Role) ([]runtimecontext.ProjectFileBrief, error) {
	const toolName = "list_file"
	if projectID <= 0 {
		return nil, fmt.Errorf("%s: project_id is required", toolName)
	}
	if b.dataSvc == nil {
		return nil, fmt.Errorf("%s: required services are not configured", toolName)
	}

	items, err := b.dataSvc.ListFileByProjectID(ctx, projectID, roles)
	if err != nil {
		return nil, fmt.Errorf("%s: failed to list files: %w", toolName, err)
	}

	out := make([]runtimecontext.ProjectFileBrief, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		out = append(out, toProjectFileBrief(item))
	}
	return out, nil
}

// toProjectFileBrief 把 types.FileWithDatasetInfo 映射为工具回传的文件摘要。
func toProjectFileBrief(item *types.FileWithDatasetInfo) runtimecontext.ProjectFileBrief {
	datasetID, _ := strconv.ParseInt(strings.TrimSpace(item.DatasetID), 10, 64)
	return runtimecontext.ProjectFileBrief{
		FileID:         item.FileID,
		FileName:       item.FileName,
		Path:           item.Path,
		Format:         item.Format,
		Role:           item.Role,
		DatasetID:      datasetID,
		DatasetName:    item.DatasetName,
		AnalysisNodeID: item.AnalysisNodeID,
		Size:           item.Size,
		MD5:            item.MD5,
		Storage:        item.Storage,
		Description:    item.Description,
	}
}

// CreateDataset 在当前激活项目下创建数据集（create_dataset）。
func (b *projectResourceBrowser) CreateDataset(ctx context.Context, in runtimecontext.DatasetCreateInput) (runtimecontext.DatasetBrief, error) {
	const toolName = "create_dataset"
	if in.ProjectID <= 0 {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("%s: project_id is required", toolName)
	}
	name := strings.TrimSpace(in.DatasetName)
	if name == "" {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("%s: dataset_name is required", toolName)
	}
	if b.dataSvc == nil {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("%s: required services are not configured", toolName)
	}

	dataset := &types.Dataset{
		DatasetName: name,
		DatasetKey:  strings.TrimSpace(in.DatasetKey),
		Description: in.Description,
		Metadata:    in.Metadata,
	}
	if err := b.dataSvc.CreateDataset(ctx, dataset, in.ProjectID); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.DatasetBrief{}, fmt.Errorf("%s: project %d not found", toolName, in.ProjectID)
		}
		return runtimecontext.DatasetBrief{}, fmt.Errorf("%s: failed to create dataset: %w", toolName, err)
	}

	return b.toDatasetBrief(toolName, in.ProjectID, dataset)
}

// UpdateDataset 更新当前激活项目下已有数据集（update_dataset）。
//
// 空串字段表示不修改：先读取现有记录并合并，再整体回写，避免误清空未提供的列。
func (b *projectResourceBrowser) UpdateDataset(ctx context.Context, in runtimecontext.DatasetUpdateInput) (runtimecontext.DatasetBrief, error) {
	const toolName = "update_dataset"
	if in.ProjectID <= 0 {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("%s: project_id is required", toolName)
	}
	if in.DatasetID <= 0 {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("%s: dataset_id is required", toolName)
	}
	if b.dataSvc == nil {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("%s: required services are not configured", toolName)
	}

	if err := b.ensureDatasetInProject(ctx, toolName, in.ProjectID, in.DatasetID); err != nil {
		return runtimecontext.DatasetBrief{}, err
	}

	dataset, err := b.dataSvc.GetDatasetByID(ctx, in.DatasetID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.DatasetBrief{}, fmt.Errorf("%s: dataset %d not found", toolName, in.DatasetID)
		}
		return runtimecontext.DatasetBrief{}, fmt.Errorf("%s: failed to load dataset: %w", toolName, err)
	}

	if name := strings.TrimSpace(in.DatasetName); name != "" {
		dataset.DatasetName = name
	}
	if key := strings.TrimSpace(in.DatasetKey); key != "" {
		dataset.DatasetKey = key
	}
	if in.Description != "" {
		dataset.Description = in.Description
	}
	if in.Metadata != "" {
		dataset.Metadata = in.Metadata
	}
	if err := b.dataSvc.UpdateDataset(ctx, dataset); err != nil {
		return runtimecontext.DatasetBrief{}, fmt.Errorf("%s: failed to update dataset: %w", toolName, err)
	}

	return b.toDatasetBrief(toolName, in.ProjectID, dataset)
}

// UpdateFile 更新当前激活项目下的文件元数据（update_file）。
//
// 仅允许修改 dataset 归属文件（go_project_dataset -> go_dataset_file -> go_file），
// 以避免越权改到其它项目的文件。空串字段表示不修改。
func (b *projectResourceBrowser) UpdateFile(ctx context.Context, in runtimecontext.FileUpdateInput) (runtimecontext.FileUpdateResult, error) {
	const toolName = "update_file"
	if in.ProjectID <= 0 {
		return runtimecontext.FileUpdateResult{}, fmt.Errorf("%s: project_id is required", toolName)
	}
	if in.FileID <= 0 {
		return runtimecontext.FileUpdateResult{}, fmt.Errorf("%s: file_id is required", toolName)
	}
	if b.dataSvc == nil {
		return runtimecontext.FileUpdateResult{}, fmt.Errorf("%s: required services are not configured", toolName)
	}

	// 校验文件属于当前激活项目：用项目文件列表里是否存在该 file_id 判定。
	files, err := b.dataSvc.ListFileByProjectID(ctx, in.ProjectID, nil)
	if err != nil {
		return runtimecontext.FileUpdateResult{}, fmt.Errorf("%s: failed to verify project binding: %w", toolName, err)
	}
	belongs := false
	for _, item := range files {
		if item != nil && item.FileID == in.FileID {
			belongs = true
			break
		}
	}
	if !belongs {
		return runtimecontext.FileUpdateResult{}, fmt.Errorf("%s: file %d does not belong to the active project %d", toolName, in.FileID, in.ProjectID)
	}

	file, err := b.dataSvc.GetFileByID(ctx, in.FileID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.FileUpdateResult{}, fmt.Errorf("%s: file %d not found", toolName, in.FileID)
		}
		return runtimecontext.FileUpdateResult{}, fmt.Errorf("%s: failed to load file: %w", toolName, err)
	}

	changed := make([]string, 0, 4)
	if v := strings.TrimSpace(in.FileName); v != "" && v != file.FileName {
		file.FileName = v
		changed = append(changed, "file_name")
	}
	if v := strings.TrimSpace(in.Path); v != "" && v != file.Path {
		file.Path = v
		changed = append(changed, "path")
	}
	if v := strings.TrimSpace(in.Format); v != "" && v != file.Format {
		file.Format = v
		changed = append(changed, "format")
	}
	if v := strings.TrimSpace(in.FileKey); v != "" && v != file.FileKey {
		file.FileKey = v
		changed = append(changed, "file_key")
	}
	if v := types.NormalizeRole(in.Role); v != "" && v != file.Role {
		file.Role = v
		changed = append(changed, "role")
	}
	if v := strings.TrimSpace(in.Storage); v != "" && v != file.Storage {
		file.Storage = v
		changed = append(changed, "storage")
	}
	if in.Description != "" && in.Description != file.Description {
		file.Description = in.Description
		changed = append(changed, "description")
	}

	if len(changed) > 0 {
		if err := b.dataSvc.UpdateFile(ctx, file); err != nil {
			return runtimecontext.FileUpdateResult{}, fmt.Errorf("%s: failed to update file: %w", toolName, err)
		}
	}

	return runtimecontext.FileUpdateResult{
		FileID:      file.ID,
		FileName:    file.FileName,
		Path:        file.Path,
		Format:      file.Format,
		Role:        file.Role,
		FileKey:     file.FileKey,
		Description: file.Description,
		Changed:     changed,
	}, nil
}

// ensureDatasetInProject 校验数据集属于当前激活项目（go_project_dataset 绑定）。
func (b *projectResourceBrowser) ensureDatasetInProject(ctx context.Context, toolName string, projectID, datasetID int64) error {
	datasets, err := b.dataSvc.ListDatasetByProjectID(ctx, projectID)
	if err != nil {
		return fmt.Errorf("%s: failed to verify project binding: %w", toolName, err)
	}
	for _, dataset := range datasets {
		if dataset != nil && dataset.ID == datasetID {
			return nil
		}
	}
	return fmt.Errorf("%s: dataset %d does not belong to the active project %d", toolName, datasetID, projectID)
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
		Role:      types.NormalizeRole(string(in.Role)),
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
