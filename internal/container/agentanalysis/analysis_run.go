package agentanalysis

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/analysisform"
	"github.com/biox-dev/gobrave/internal/compiler"
	"github.com/biox-dev/gobrave/internal/dag/scheduler"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// 本文件实现「分析运行（nextflow 行）」级别的能力：
//   - runtimecontext.AnalysisCreator（create_analysis_workflow）：为工作流新建一次运行，见 analysis_create.go；
//   - runtimecontext.AnalysisUpdater（update_analysis_workflow）：改名 / 改参数 / 改缓存策略；
//   - runtimecontext.AnalysisRunner（run_analysis_workflow）：提交一次运行交给所属调度器。
//
// 两者共用同一个「保存 + 编译」流程，与 handler.SaveAnalysisController 完全同口径：
//  1. 按工作流表单（analysisform.BuildParseAnalysisResult）把 request_param 解析成
//     parse_analysis_result；
//  2. 静态图调度器需要时把 DAG 编译成 analysis_nodes / analysis_edges 并随保存落库
//     （compiler.BuildRuntimeTasks）；
//  3. analysisService.SaveAnalysisController 负责写 params.json、重置节点状态与缓存策略；
//  4. run_analysis_workflow 额外调用 orchestrator.StartAsync 异步推进。
//
// 这样 Agent 侧的分析提交与 UI 侧不会产生两套语义。

// analysisManager 同时实现 runtimecontext.AnalysisCreator / AnalysisUpdater / AnalysisRunner。
type analysisManager struct {
	analysisSvc interfaces.AnalysisService
	workflowSvc interfaces.WorkflowService
	projectRepo interfaces.ProjectRepository
	dataSvc     interfaces.DataService
	schedulers  *scheduler.Registry
	// resources 记录「当前会话 ↔ 分析运行」的关联（创建 / 更新 / 提交成功后写入）。
	resources agentinterfaces.SessionResourceService
}

// NewManager 构建「创建 / 更新 / 提交分析运行」执行器并注入内置工具体系。
func NewManager(
	analysisSvc interfaces.AnalysisService,
	workflowSvc interfaces.WorkflowService,
	projectRepo interfaces.ProjectRepository,
	dataSvc interfaces.DataService,
	schedulers *scheduler.Registry,
	resources agentinterfaces.SessionResourceService,
) runtimecontext.AnalysisRunner {
	return &analysisManager{
		analysisSvc: analysisSvc,
		workflowSvc: workflowSvc,
		projectRepo: projectRepo,
		dataSvc:     dataSvc,
		schedulers:  schedulers,
		resources:   resources,
	}
}

// UpdateAnalysis 更新分析运行的分析名称 / 请求参数 / 缓存策略。
func (m *analysisManager) UpdateAnalysis(ctx context.Context, in runtimecontext.AnalysisUpdateInput) (runtimecontext.AnalysisUpdateResult, error) {
	// params.json 文件编辑：以磁盘正文为基准编辑，再解析回 request_param 走统一校验流程。
	if in.File != "" {
		if in.File != runtimecontext.ResourceFileParams {
			return runtimecontext.AnalysisUpdateResult{}, fmt.Errorf("update_analysis_workflow: unsupported file %q (expected params)", in.File)
		}
		if in.RequestParam != nil {
			return runtimecontext.AnalysisUpdateResult{}, fmt.Errorf("update_analysis_workflow: file edit cannot be combined with request_param; choose one style")
		}
		analysis, err := m.analysisSvc.GetAnalysisByID(ctx, in.AnalysisID)
		if err != nil {
			return runtimecontext.AnalysisUpdateResult{}, fmt.Errorf("update_analysis_workflow: failed to load analysis %d: %w", in.AnalysisID, err)
		}
		current, err := os.ReadFile(analysis.ParamsFile())
		if err != nil && !os.IsNotExist(err) {
			return runtimecontext.AnalysisUpdateResult{}, fmt.Errorf("update_analysis_workflow: failed to read params.json: %w", err)
		}
		edit := runtimecontext.ResourceFileEdit{File: in.File, Content: in.Content, OldString: in.OldString, NewString: in.NewString}
		after, err := edit.Apply(string(current))
		if err != nil {
			return runtimecontext.AnalysisUpdateResult{}, fmt.Errorf("update_analysis_workflow: %w", err)
		}
		params := map[string]any{}
		if err := json.Unmarshal([]byte(after), &params); err != nil {
			return runtimecontext.AnalysisUpdateResult{}, fmt.Errorf("update_analysis_workflow: params edit produced invalid JSON: %w", err)
		}
		in.RequestParam = params
	}

	prepared, err := m.prepare(ctx, analysisUpdateRequest{
		projectID:    in.ProjectID,
		analysisID:   in.AnalysisID,
		analysisName: in.AnalysisName,
		requestParam: in.RequestParam,
		cacheType:    in.CacheType,
	}, "update_analysis_workflow")
	if err != nil {
		return runtimecontext.AnalysisUpdateResult{}, err
	}
	originalName := prepared.analysis.AnalysisName

	saved, _, _, err := m.save(ctx, prepared, "update_analysis_workflow")
	if err != nil {
		return runtimecontext.AnalysisUpdateResult{}, err
	}

	// 记录「当前会话修改了该分析运行」；关联失败不影响更新结果。
	if m.resources != nil {
		_ = m.resources.LinkFromContext(ctx, agenttypes.SessionResourceTypeAnalysis, saved.ID)
	}

	changed := make([]string, 0, 3)
	if in.AnalysisName != nil && prepared.analysisName() != originalName {
		changed = append(changed, "analysis_name")
	}
	if in.RequestParam != nil {
		changed = append(changed, "request_param")
	}
	if in.CacheType != nil && prepared.cacheType != prepared.analysis.CacheType {
		changed = append(changed, "cache_type")
	}

	return runtimecontext.AnalysisUpdateResult{
		AnalysisBrief: m.brief(ctx, saved, prepared.orchestrator),
		Changed:       changed,
	}, nil
}

// RunAnalysis 提交分析运行：物化 / 编译 DAG 后交给所属调度器异步推进。
func (m *analysisManager) RunAnalysis(ctx context.Context, in runtimecontext.AnalysisRunInput) (runtimecontext.AnalysisRunResult, error) {
	prepared, err := m.prepare(ctx, analysisUpdateRequest{
		projectID:    in.ProjectID,
		analysisID:   in.AnalysisID,
		requestParam: in.RequestParam,
		cacheType:    in.CacheType,
	}, "run_analysis_workflow")
	if err != nil {
		return runtimecontext.AnalysisRunResult{}, err
	}

	saved, parseResult, dagDefinition, err := m.save(ctx, prepared, "run_analysis_workflow")
	if err != nil {
		return runtimecontext.AnalysisRunResult{}, err
	}

	// 提交运行同样会重写分析记录，视为本会话「修改了该分析运行」。
	if m.resources != nil {
		_ = m.resources.LinkFromContext(ctx, agenttypes.SessionResourceTypeAnalysis, saved.ID)
	}

	// 与 handler.SaveAnalysisController 一致：StartAsync 需要 parse_analysis_result 与
	// DAG 定义，由调度器按各自策略使用（运行时物化的调度器忽略持久化的图）。
	if err := prepared.orchestrator.StartAsync(ctx, saved.ID, parseResult, dagDefinition); err != nil {
		return runtimecontext.AnalysisRunResult{}, fmt.Errorf("run_analysis_workflow: failed to start scheduler %s: %w", prepared.orchestrator.Name(), err)
	}

	brief := m.brief(ctx, saved, prepared.orchestrator)
	return runtimecontext.AnalysisRunResult{
		AnalysisID:  brief.AnalysisID,
		AnalysisKey: brief.AnalysisKey,
		Scheduler:   prepared.orchestrator.Name(),
		Started:     true,
		JobStatus:   brief.JobStatus,
	}, nil
}

// analysisUpdateRequest 是 update / run 共用的内部入参。
type analysisUpdateRequest struct {
	projectID    int64
	analysisID   int64
	analysisName *string
	requestParam map[string]any
	cacheType    *int
}

// preparedAnalysis 是校验通过后的执行上下文。
//
// analysis 保持库中读出的原样（analysisName 覆盖值单独保存），便于调用方对比改动；
// 真正生效的名称一律走 name。
type preparedAnalysis struct {
	analysis     *types.AnalysisWorkflow
	name         string
	requestParam map[string]any
	cacheType    int
	orchestrator interfaces.DagOrchestrator
}

// analysisName 返回本次生效的分析名称。
func (p preparedAnalysis) analysisName() string { return strings.TrimSpace(p.name) }

// prepare 校验入参并加载分析 / 调度器，返回本次保存需要的全部上下文。
//
// 名称覆盖值只放进 preparedAnalysis.name，不修改 analysis，便于调用方对比改动前后。
func (m *analysisManager) prepare(ctx context.Context, in analysisUpdateRequest, toolName string) (preparedAnalysis, error) {
	if in.projectID <= 0 {
		return preparedAnalysis{}, fmt.Errorf("%s: project_id is required", toolName)
	}
	if in.analysisID <= 0 {
		return preparedAnalysis{}, fmt.Errorf("%s: analysis_id is required", toolName)
	}
	if in.analysisName == nil && in.requestParam == nil && in.cacheType == nil {
		return preparedAnalysis{}, fmt.Errorf("%s: at least one of analysis_name / request_param / cache_type is required", toolName)
	}
	if in.analysisName != nil && strings.TrimSpace(*in.analysisName) == "" {
		return preparedAnalysis{}, fmt.Errorf("%s: analysis_name must not be empty", toolName)
	}
	if m.analysisSvc == nil || m.workflowSvc == nil || m.projectRepo == nil || m.dataSvc == nil || m.schedulers == nil {
		return preparedAnalysis{}, fmt.Errorf("%s: required services are not configured", toolName)
	}

	analysis, err := m.analysisSvc.GetAnalysisByID(ctx, in.analysisID)
	if err != nil || analysis == nil || analysis.ID == 0 {
		return preparedAnalysis{}, fmt.Errorf("%s: analysis %d not found in the active project", toolName, in.analysisID)
	}
	if analysis.ProjectID != in.projectID {
		return preparedAnalysis{}, fmt.Errorf("%s: analysis %d does not belong to the active project %d", toolName, analysis.ID, in.projectID)
	}
	// 运行中的分析不接受修改 / 重复提交：先停止再操作，避免与调度器并发写同一行。
	if status := strings.TrimSpace(analysis.JobStatus); status == types.AnalysisStatusRunning || status == types.AnalysisStatusStopping {
		return preparedAnalysis{}, fmt.Errorf("%s: analysis %d is %s; stop it before updating or running it again", toolName, analysis.ID, status)
	}

	orchestrator := m.schedulers.Resolve(analysis.SchedulerMode)
	if orchestrator == nil {
		return preparedAnalysis{}, fmt.Errorf("%s: unknown scheduler %q on analysis %d", toolName, analysis.SchedulerMode, analysis.ID)
	}

	// 本次生效的名称：给出时覆盖，否则沿用库中值。
	name := analysis.AnalysisName
	if in.analysisName != nil {
		name = strings.TrimSpace(*in.analysisName)
	}

	requestParam := in.requestParam
	if requestParam == nil {
		requestParam = DecodeRequestParam(analysis.RequestParam)
	}
	if requestParam == nil {
		requestParam = map[string]any{}
	}

	cacheType := analysis.CacheType
	if cacheType < types.CacheTypeRerunAll || cacheType > types.CacheTypeReuseWhenScriptAndParamsUnchanged {
		cacheType = types.CacheTypeRerunAll
	}
	if in.cacheType != nil {
		if *in.cacheType < types.CacheTypeRerunAll || *in.cacheType > types.CacheTypeReuseWhenScriptAndParamsUnchanged {
			return preparedAnalysis{}, fmt.Errorf("%s: cache_type must be between %d and %d", toolName, types.CacheTypeRerunAll, types.CacheTypeReuseWhenScriptAndParamsUnchanged)
		}
		cacheType = *in.cacheType
	}

	return preparedAnalysis{
		analysis:     analysis,
		name:         name,
		requestParam: requestParam,
		cacheType:    cacheType,
		orchestrator: orchestrator,
	}, nil
}

// save 按 UI 侧同一路径保存分析：解析参数 → （按调度器需要）编译并落库图 → 写 params.json。
func (m *analysisManager) save(
	ctx context.Context,
	prepared preparedAnalysis,
	toolName string,
) (*types.AnalysisWorkflow, map[string]any, map[string]any, error) {
	analysis := prepared.analysis
	parseResult, dagDefinition, err := m.buildParseResult(ctx, analysis.WorkflowID, prepared.name, prepared.requestParam)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: %w", toolName, err)
	}

	project, err := m.projectRepo.GetProjectByID(ctx, analysis.ProjectID)
	if err != nil || project == nil || project.ID == 0 {
		return nil, nil, nil, fmt.Errorf("%s: failed to load project %d", toolName, analysis.ProjectID)
	}

	input := &types.AnalysisControllerSaveInput{
		Project:             project,
		RequestParam:        prepared.requestParam,
		ParseAnalysisResult: parseResult,
		AnalysisID:          analysis.ID,
		WorkflowID:          analysis.WorkflowID,
		NodeName:            prepared.name,
		CacheType:           prepared.cacheType,
		// 保留原调度器归属：更新参数不应把分析重新分配给别的调度器。
		SchedulerMode: prepared.orchestrator.Name(),
	}

	// 静态图调度器需要把编译结果随保存落库；运行时物化的调度器忽略该字段。
	if prepared.orchestrator.PersistsGraphOnSave() {
		compileParams := make(map[string]any, len(prepared.requestParam)+1)
		for k, v := range prepared.requestParam {
			compileParams[k] = v
		}
		if name := strings.TrimSpace(prepared.name); name != "" {
			compileParams["analysis_name"] = name
		}
		dagRuntime, compileErr := compiler.BuildRuntimeTasks(analysis.ID, parseResult, compileParams, dagDefinition)
		if compileErr != nil {
			return nil, nil, nil, fmt.Errorf("%s: failed to compile runtime dag: %w", toolName, compileErr)
		}
		input.DagRuntime = dagRuntime
	}

	saved, err := m.analysisSvc.SaveAnalysisController(ctx, input)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%s: failed to save analysis: %w", toolName, err)
	}
	if saved == nil {
		return nil, nil, nil, fmt.Errorf("%s: analysis %d was not saved", toolName, analysis.ID)
	}
	return saved, parseResult, dagDefinition, nil
}

// buildParseResult 按工作流表单把 request_param 解析成 parse_analysis_result。
func (m *analysisManager) buildParseResult(ctx context.Context, workflowID int64, analysisName string, requestParam map[string]any) (map[string]any, map[string]any, error) {
	formJSON, err := m.workflowSvc.GetFormJSONByWorkflowID(ctx, workflowID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load workflow %d form: %w", workflowID, err)
	}
	parseResult, err := analysisform.BuildParseAnalysisResult(ctx, m.dataSvc, requestParam, formJSON, analysisName)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse request_param: %w", err)
	}
	dagDefinition, err := m.workflowSvc.GetWorkflowVisByWorkflowID(ctx, workflowID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to load workflow %d dag_definition: %w", workflowID, err)
	}
	return parseResult, dagDefinition, nil
}

// brief 构造分析运行的只读摘要（含工作流名称与节点状态分布）。
func (m *analysisManager) brief(ctx context.Context, analysis *types.AnalysisWorkflow, orchestrator interfaces.DagOrchestrator) runtimecontext.AnalysisBrief {
	brief := runtimecontext.AnalysisBrief{
		AnalysisID:    analysis.ID,
		AnalysisKey:   analysis.AnalysisID,
		AnalysisName:  analysis.AnalysisName,
		ProjectID:     analysis.ProjectID,
		WorkflowID:    analysis.WorkflowID,
		JobStatus:     analysis.JobStatus,
		SchedulerMode: orchestrator.Name(),
		CacheType:     analysis.CacheType,
		WorkspaceDir:  analysis.WorkspaceDir,
		ParamsPath:    analysis.ParamsFile(),
		RequestParam:  DecodeRequestParam(analysis.RequestParam),
	}
	if analysis.WorkflowID > 0 {
		if workflow, err := m.workflowSvc.GetWorkflowByID(ctx, analysis.WorkflowID); err == nil && workflow != nil {
			brief.WorkflowName = workflow.Name
		}
	}
	if nodes, err := m.analysisSvc.ListAnalysisNodesByAnalysisID(ctx, analysis.ID); err == nil {
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
	return brief
}
