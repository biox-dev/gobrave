package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// CreateAnalysisWorkflowToolName 是「为工作流创建一次分析运行（可选提交）」工具名。
const CreateAnalysisWorkflowToolName = "create_analysis_workflow"

// CreateAnalysisWorkflowInput 是 create_analysis_workflow 工具的入参。
//
// ProjectID 不由模型提供：它取自执行事实（tool.ProjectIDFrom，当前用户激活项目），
// 避免把运行写到别的项目下。
type CreateAnalysisWorkflowInput struct {
	WorkflowID   string         `json:"workflow_id"`   // 工作流主键 ID（必填，字符串以保精度）
	AnalysisName string         `json:"analysis_name"` // 分析名称，留空时使用工作流名称
	RequestParam map[string]any `json:"request_param"` // 本次运行的请求参数（键为工作流 io_schema inputs 的 name），可留空
	CacheType    *int           `json:"cache_type"`    // 缓存策略，留空时按 1（全量重跑）
	// Run 可选：是否在创建后立即提交运行。nil / true 表示提交（默认），false 表示只创建。
	Run *bool `json:"run"`
}

// CreateAnalysisWorkflowOutput 是 create_analysis_workflow 工具返回给模型的结果。
//
// 内嵌 AnalysisBrief（analysis_id / job_status / workspace_dir / params_path ...），
// 再补本次是否提交运行与提交结果。
type CreateAnalysisWorkflowOutput struct {
	runtimecontext.AnalysisBrief
	// Ran 表示本次是否提交了运行（run=false 时为 false）。
	Ran bool `json:"ran"`
	// Started / Scheduler 只在 ran=true 时有意义：分析运行是异步提交，不等运行结束。
	Started   bool   `json:"started"`
	Scheduler string `json:"scheduler,omitempty"`
	Error     string `json:"error,omitempty"`
}

// CreateAnalysisWorkflow 返回一个「为工作流创建一次分析运行（可选提交）」工具。
//
// 行为：
//   - 从 RuntimeContext 取当前项目 ID，workflow_id 必须属于当前激活项目；
//   - 按工作流表单解析 request_param 并落库，生成分析目录与 params.json（job_status=created）；
//   - run 为 true（默认）时紧接着调用 run_analysis_workflow 的提交链路异步启动调度器，
//     返回 started / scheduler / job_status，不等待运行结束。
//
// 该工具会创建业务数据（写库 + 写 params.json），默认还会提交一次运行，因此声明
// analysis 授权（与 run_analysis_workflow 同类）。
func CreateAnalysisWorkflow(creator runtimecontext.AnalysisCreator, runner runtimecontext.AnalysisRunner) tool.Tool {
	return tool.NewFunc(
		CreateAnalysisWorkflowToolName,
		`为已有工作流创建一次新的分析运行（analysis = 某个工作流的一次提交，带 job_status）。

两层别混用：工作流定义（create_workflow / update_workflow）只是模板；本工具按其 workflow_id 生成一条分析运行记录，再交给调度器推进。

入参说明：
- workflow_id：工作流主键（必填，整数，字符串以保精度）；用户消息里出现 @workflow:<id> 时直接用冒号后的 ID。不确定时先用 list_project_workflow 列出当前项目工作流。
- analysis_name：分析名称，留空时使用工作流名称。
- request_param：本次运行的请求参数，键为工作流 io_schema.inputs 中各条目的 name；可留空（工作流没有输入参数时）。
- cache_type：缓存策略。1=全量重跑（默认）；2=复用已有节点；3=脚本未变则复用；4=脚本与参数都未变才复用。
- run：可选，默认 true。为 true 时创建后立即提交运行（异步启动，不等待结束）；为 false 时只创建分析运行（job_status=created），之后可用 run_analysis_workflow 提交。

返回：analysis_id（后续 get_analysis_workflow / update_analysis_workflow / run_analysis_workflow 都用它）、analysis_key、analysis_name、workflow_id / workflow_name、job_status、scheduler_mode、workspace_dir、params_path、request_param、节点分布，以及 ran / started / scheduler。

跑起来之后用 get_analysis_workflow 查看 job_status 与 node_status_counts；看单个节点产物用 get_analysis_node。所属项目取自当前会话上下文，无需传入。`,
		tool.Schema("创建工作流的一次分析运行的入参", map[string]any{
			"workflow_id":   tool.StringProperty("工作流主键 ID（必填，整数，字符串以保精度）；可直接使用 @workflow:<id>（冒号后的 ID）。"),
			"analysis_name": tool.StringProperty("分析名称；留空时使用工作流名称。"),
			"request_param": map[string]any{
				"type":        "object",
				"description": "本次运行的请求参数对象，键为工作流 io_schema.inputs 中各条目的 name。可留空。",
			},
			"cache_type": tool.IntegerProperty("缓存策略：1 全量重跑；2 复用已有节点；3 脚本未变则复用；4 脚本与参数都未变才复用。留空时按 1（全量重跑）。"),
			"run":        tool.BooleanProperty("是否在创建后立即提交运行，可选，默认 true。传 false 时只创建分析运行，不提交运行。"),
		}, "workflow_id"),
		func(ctx context.Context, in CreateAnalysisWorkflowInput) (CreateAnalysisWorkflowOutput, error) {
			projectID, err := requireProjectID(ctx, CreateAnalysisWorkflowToolName)
			if err != nil {
				return CreateAnalysisWorkflowOutput{}, err
			}
			workflowID, err := parseWorkflowID(in.WorkflowID)
			if err != nil {
				return CreateAnalysisWorkflowOutput{}, wrapToolNameError(CreateAnalysisWorkflowToolName, err)
			}

			if creator == nil {
				return CreateAnalysisWorkflowOutput{}, fmt.Errorf("%s: analysis creator is not available in this runtime", CreateAnalysisWorkflowToolName)
			}

			if err := authorizeResourceWrite(ctx, CreateAnalysisWorkflowToolName, fmt.Sprintf("Create analysis run for workflow %d", workflowID), previewCreateAnalysisWorkflow(ctx, in)); err != nil {
				return CreateAnalysisWorkflowOutput{}, err
			}

			result, createErr := creator.CreateAnalysis(ctx, runtimecontext.AnalysisCreateInput{
				ProjectID:    projectID,
				WorkflowID:   workflowID,
				AnalysisName: strings.TrimSpace(in.AnalysisName),
				RequestParam: in.RequestParam,
				CacheType:    in.CacheType,
			})
			out := CreateAnalysisWorkflowOutput{AnalysisBrief: result.AnalysisBrief}
			if createErr != nil {
				out.Error = createErr.Error()
				return out, nil
			}

			// run 可选且默认为 true：nil 视为未传，按默认运行处理。
			run := true
			if in.Run != nil {
				run = *in.Run
			}
			out.Ran = run
			if !run {
				return out, nil
			}

			if runner == nil {
				out.Ran = false
				out.Error = "create_analysis_workflow: analysis runner is not available in this runtime; the analysis was created but not run"
				return out, nil
			}

			// request_param 传 nil 让 runner 使用刚保存到分析记录上的参数。
			runResult, runErr := runner.RunAnalysis(ctx, runtimecontext.AnalysisRunInput{
				ProjectID:  projectID,
				AnalysisID: result.AnalysisID,
			})
			out.Started = runResult.Started
			out.Scheduler = runResult.Scheduler
			if runResult.JobStatus != "" {
				out.JobStatus = runResult.JobStatus
			}
			if runErr != nil {
				out.Error = runErr.Error()
			}
			return out, nil
		},
	)
}

// previewCreateAnalysisWorkflow 生成批准前预览：新分析运行 params.json 将写入的请求参数
// （新增文件 diff）。未给出 request_param 时没有可预览的内容。
func previewCreateAnalysisWorkflow(_ context.Context, in CreateAnalysisWorkflowInput) tool.PermissionPreview {
	if in.RequestParam == nil {
		return tool.PermissionPreview{}
	}
	content := paramsJSONContent(in.RequestParam)
	if content == "" {
		return tool.PermissionPreview{}
	}
	return textDiffPreview("params.json", "", content)
}
