package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// RunAnalysisToolName 是「提交分析运行」工具名。
const RunAnalysisToolName = "run_analysis"

// RunAnalysisInput 是 run_analysis 工具的入参。
//
// RequestParam / CacheType 缺省时沿用分析记录上已保存的值；给出时先更新再提交。
type RunAnalysisInput struct {
	AnalysisID   string         `json:"analysis_id"`
	RequestParam map[string]any `json:"request_param"`
	CacheType    *int           `json:"cache_type"`
}

// RunAnalysisOutput 是 run_analysis 工具返回给模型的结果。
type RunAnalysisOutput struct {
	runtimecontext.AnalysisRunResult
	Error string `json:"error,omitempty"`
}

// runAnalysisDescription 返回 run_analysis 的工具描述（Markdown 格式）。
func runAnalysisDescription() string {
	return `## 用途

按 analysis_id 提交一次「分析运行」（整个工作流），由所属调度器异步推进。与 run_analysis_node 不同：那个只跑单个节点，本工具跑整条 DAG。

## 行为

- 先按工作流表单解析请求参数并重写 params.json，静态图调度器还会重新编译节点 / 连线（按 cache_type 决定哪些节点复用）；
- 然后调用调度器异步启动，**不等待分析结束**，因此返回 started=true 后应隔一段时间再用 get_analysis 查看 job_status 与 node_status_counts；
- job_status=running / stopping 时拒绝重复提交，需要先停止。

## 入参

- **analysis_id**（必填）：分析运行主键。
- **request_param**（可选）：本次运行的请求参数，键为工作流 io_schema.inputs 的 name。缺省时沿用分析记录上已保存的参数；显式传空对象（{}）表示清空参数。
- **cache_type**（可选）：1 全量重跑；2 复用已有节点；3 脚本未变则复用；4 脚本与参数都未变才复用。缺省沿用分析记录上的值。

## 返回

- **started / scheduler**：是否已提交与所属调度器（dag / dynamic / dataflow）。
- **analysis_id / analysis_key / job_status**：提交后的分析标识与状态。

## 后续工具

- 查看提交后的运行状态与节点分布用 get_analysis；看单个节点产物用 get_analysis_node。`
}

// RunAnalysis 返回一个「提交分析运行」工具。
//
// 该工具会触发整条工作流运行，因此声明 analysis 授权（与 run_analysis_node 同类，
// 都映射到「分析运行」操作类型），上层在执行前会先走权限门禁。
func RunAnalysis(runner runtimecontext.AnalysisRunner, browser runtimecontext.AnalysisBrowser) tool.Tool {
	return tool.NewFunc(
		RunAnalysisToolName,
		runAnalysisDescription(),
		tool.Schema("提交分析运行的入参", map[string]any{
			"analysis_id": tool.StringProperty("要提交的分析运行主键 ID（必填，整数，字符串以保精度）。"),
			"request_param": map[string]any{
				"type":        "object",
				"description": "本次运行的请求参数对象，键为工作流 io_schema.inputs 中各条目的 name。缺省表示沿用已保存的参数；显式传空对象（{}）表示清空参数。",
			},
			"cache_type": tool.IntegerProperty("缓存策略：1 全量重跑；2 复用已有节点；3 脚本未变则复用；4 脚本与参数都未变才复用。不修改时留空。"),
		}, "analysis_id"),
		func(ctx context.Context, in RunAnalysisInput) (RunAnalysisOutput, error) {
			projectID, err := requireProjectID(ctx, RunAnalysisToolName)
			if err != nil {
				return RunAnalysisOutput{}, err
			}
			analysisID, err := parseAnalysisID(in.AnalysisID)
			if err != nil {
				return RunAnalysisOutput{}, wrapToolNameError(RunAnalysisToolName, err)
			}
			if runner == nil {
				return RunAnalysisOutput{}, fmt.Errorf("%s: analysis runner is not available in this runtime", RunAnalysisToolName)
			}

			result, runErr := runner.RunAnalysis(ctx, runtimecontext.AnalysisRunInput{
				ProjectID:    projectID,
				AnalysisID:   analysisID,
				RequestParam: in.RequestParam,
				CacheType:    in.CacheType,
			})
			out := RunAnalysisOutput{AnalysisRunResult: result}
			if runErr != nil {
				out.Error = runErr.Error()
			}
			return out, nil
		},
	).WithPermissionPreview("analysis", "", "提交分析运行（解析参数、编译 DAG 并交给调度器异步推进）", func(ctx context.Context, in RunAnalysisInput) tool.PermissionPreview {
		return previewAnalysisParams(ctx, browser, RunAnalysisToolName, in.AnalysisID, in.RequestParam)
	})
}
