package agentanalysis

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/compiler"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
)

// CreateAnalysis 实现 runtimecontext.AnalysisCreator：为指定工作流新建一次分析运行。
//
// 与 UI 侧 handler.SaveAnalysisController 同一口径，只是不提交运行：
//  1. 按工作流表单（analysisform.BuildParseAnalysisResult）把 request_param 解析成
//     parse_analysis_result；
//  2. 静态图调度器需要时把 DAG 编译成 analysis_nodes / analysis_edges 并随保存落库
//     （compiler.BuildRuntimeTasks）；
//  3. analysisService.SaveAnalysisController 写库并把 params.json 落到分析目录，
//     新建行 job_status=created。
//
// 新建的分析归属系统默认调度器（schedulers.Resolve("")），之后用
// run_analysis_workflow 提交运行即可。
func (m *analysisManager) CreateAnalysis(ctx context.Context, in runtimecontext.AnalysisCreateInput) (runtimecontext.AnalysisCreateResult, error) {
	const toolName = "create_analysis_workflow"

	if in.ProjectID <= 0 {
		return runtimecontext.AnalysisCreateResult{}, fmt.Errorf("%s: project_id is required", toolName)
	}
	if in.WorkflowID <= 0 {
		return runtimecontext.AnalysisCreateResult{}, fmt.Errorf("%s: workflow_id is required", toolName)
	}
	if m.analysisSvc == nil || m.workflowSvc == nil || m.projectRepo == nil || m.dataSvc == nil || m.schedulers == nil {
		return runtimecontext.AnalysisCreateResult{}, fmt.Errorf("%s: required services are not configured", toolName)
	}

	workflow, err := m.workflowSvc.GetWorkflowByID(ctx, in.WorkflowID)
	if err != nil {
		return runtimecontext.AnalysisCreateResult{}, fmt.Errorf("%s: failed to load workflow %d: %w", toolName, in.WorkflowID, err)
	}
	if workflow == nil || workflow.ID == 0 {
		return runtimecontext.AnalysisCreateResult{}, fmt.Errorf("%s: workflow %d not found", toolName, in.WorkflowID)
	}
	if workflow.ProjectID != in.ProjectID {
		return runtimecontext.AnalysisCreateResult{}, fmt.Errorf("%s: workflow %d does not belong to the active project %d", toolName, workflow.ID, in.ProjectID)
	}

	// 名称缺省时回退到工作流名称，保证落库的 analysis_name 非空。
	analysisName := strings.TrimSpace(in.AnalysisName)
	if analysisName == "" {
		analysisName = strings.TrimSpace(workflow.Name)
	}
	if analysisName == "" {
		return runtimecontext.AnalysisCreateResult{}, fmt.Errorf("%s: analysis_name is required", toolName)
	}

	cacheType := types.CacheTypeRerunAll
	if in.CacheType != nil {
		if *in.CacheType < types.CacheTypeRerunAll || *in.CacheType > types.CacheTypeReuseWhenScriptAndParamsUnchanged {
			return runtimecontext.AnalysisCreateResult{}, fmt.Errorf("%s: cache_type must be between %d and %d", toolName, types.CacheTypeRerunAll, types.CacheTypeReuseWhenScriptAndParamsUnchanged)
		}
		cacheType = *in.CacheType
	}

	requestParam := in.RequestParam
	if requestParam == nil {
		requestParam = map[string]any{}
	}

	orchestrator := m.schedulers.Resolve("")
	if orchestrator == nil {
		return runtimecontext.AnalysisCreateResult{}, fmt.Errorf("%s: no scheduler is registered", toolName)
	}

	project, err := m.projectRepo.GetProjectByID(ctx, in.ProjectID)
	if err != nil || project == nil || project.ID == 0 {
		return runtimecontext.AnalysisCreateResult{}, fmt.Errorf("%s: failed to load project %d", toolName, in.ProjectID)
	}

	parseResult, dagDefinition, err := m.buildParseResult(ctx, workflow.ID, analysisName, requestParam)
	if err != nil {
		return runtimecontext.AnalysisCreateResult{}, fmt.Errorf("%s: %w", toolName, err)
	}

	// 主键在这里生成：静态图调度器需要在落库前用它编译分析图（与 UI 保存同一口径）。
	analysisID := utils.GenerateID()
	input := &types.AnalysisControllerSaveInput{
		Project:             project,
		RequestParam:        requestParam,
		ParseAnalysisResult: parseResult,
		AnalysisID:          analysisID,
		WorkflowID:          workflow.ID,
		NodeName:            analysisName,
		CacheType:           cacheType,
		SchedulerMode:       orchestrator.Name(),
	}
	if orchestrator.PersistsGraphOnSave() {
		compileParams := make(map[string]any, len(requestParam)+1)
		for k, v := range requestParam {
			compileParams[k] = v
		}
		compileParams["analysis_name"] = analysisName
		dagRuntime, compileErr := compiler.BuildRuntimeTasks(analysisID, parseResult, compileParams, dagDefinition)
		if compileErr != nil {
			return runtimecontext.AnalysisCreateResult{}, fmt.Errorf("%s: failed to compile runtime dag: %w", toolName, compileErr)
		}
		input.DagRuntime = dagRuntime
	}

	saved, err := m.analysisSvc.SaveAnalysisController(ctx, input)
	if err != nil {
		return runtimecontext.AnalysisCreateResult{}, fmt.Errorf("%s: failed to save analysis: %w", toolName, err)
	}
	if saved == nil {
		return runtimecontext.AnalysisCreateResult{}, fmt.Errorf("%s: analysis was not saved", toolName)
	}

	// 记录「当前会话创建了该分析运行」；关联失败不影响创建结果。
	if m.resources != nil {
		_ = m.resources.LinkFromContext(ctx, agenttypes.SessionResourceTypeAnalysis, saved.ID)
	}

	return runtimecontext.AnalysisCreateResult{AnalysisBrief: m.brief(ctx, saved, orchestrator)}, nil
}
