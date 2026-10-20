package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// RunAnalysisNodeToolName 是「运行分析节点」工具名。
const RunAnalysisNodeToolName = "run_analysis_node"

// RunAnalysisNodeInput 是 run_analysis_node 工具的入参。
//
// AnalysisNodeID 为必填：模型必须显式给出要运行的分析节点 ID，不再从运行时上下文回退。
//
// RequestParam 可选：非空时以它为本次运行的请求参数（并回写节点记录），为空时回退到
// AnalysisNode 上已保存的 request_param。两种情况下都由 AnalysisNodeRunner 按脚本最新
// io_schema.json 解析、回写节点并重新生成 params.json。
type RunAnalysisNodeInput struct {
	AnalysisNodeID string         `json:"analysis_node_id"` // 分析节点 ID（必填，字符串以保精度）
	RequestParam   map[string]any `json:"request_param"`    // 本次运行的请求参数，可选；为空时用节点已保存的 request_param
	TimeoutSeconds int            `json:"timeout_seconds"`  // 等待完成的超时（秒），可选，默认 600
}

// RunAnalysisNodeOutput 是 run_analysis_node 工具返回给模型的结果。
//
// 除状态与日志外，还回传节点在磁盘上的关键路径与本次实际生效的参数：
//   - WorkspaceDir：节点根目录（output / params.json / run.sh / command.log 均在其下）；
//   - CommandPath：本次实际执行的 run.sh；
//   - ParamsPath：本次运行读取的 params.json；
//   - LogPath：本次执行的 command.log（Log 字段被截断时可据此读取完整日志）；
//   - RequestParam / ParamsJSON：本次实际生效的请求参数与 params.json 正文。
type RunAnalysisNodeOutput struct {
	AnalysisNodeID int64 `json:"analysis_node_id,string"`
	// ScriptID 是该节点绑定的脚本 ID，便于模型接着用 get_script 查看脚本或定位脚本文件。
	ScriptID int64 `json:"script_id,string"`

	Status       string `json:"status"`                  // 节点终态（success / failed / timeout ...）
	Success      bool   `json:"success"`                 // 是否为成功终态
	Log          string `json:"log,omitempty"`           // 节点执行日志（command.log 内容）
	LogPath      string `json:"log_path,omitempty"`      // 节点 command.log 绝对路径（Log 截断时可读全文）
	WorkspaceDir string `json:"workspace_dir,omitempty"` // 节点根目录
	CommandPath  string `json:"command_path,omitempty"`  // 本次执行的 run.sh 路径
	ParamsPath   string `json:"params_path,omitempty"`   // 本次运行的 params.json 路径
	// RequestParam 是本次运行实际生效的请求参数（键为 io_schema inputs 的 name），
	// 取自节点记录并在运行前按 io_schema 解析校验。
	RequestParam map[string]any `json:"request_param,omitempty"`
	// ResolvedInputs 是按脚本 io_schema 解析后的结构化输入（已写入节点）。
	ResolvedInputs map[string]any `json:"resolved_inputs,omitempty"`
	// ParamsJSON 是本次运行实际使用的 params.json 正文，便于确认脚本拿到的参数。
	ParamsJSON string `json:"params_json,omitempty"`
	// ParamsUpdated 表示本次运行前是否解析并更新了节点参数（request_param 非空时）。
	ParamsUpdated bool `json:"params_updated,omitempty"`
	// InputValidationErrors / OutputValidationErrors 是本次运行前写回节点的校验错误快照
	// （运行前会被清空，由编排器在运行期重新填充）。
	InputValidationErrors  []any `json:"input_validation_errors,omitempty"`
	OutputValidationErrors []any `json:"output_validation_errors,omitempty"`
	// ParamsWarnings 是参数解析阶段的非致命提示（如缺少必填输入、存在 io_schema 未声明的键）。
	ParamsWarnings []string `json:"params_warnings,omitempty"`
	Error          string   `json:"error,omitempty"` // 失败原因（如有）
}

// RunAnalysisNode 返回一个「运行分析节点」工具。
//
// 行为：
//   - 解析必填入参 analysis_node_id；
//   - 把运行交给 AnalysisNodeRunner：runner 以本次传入的 request_param 为准（未传时回退到
//     节点上已保存的 request_param），按脚本最新 io_schema.json 解析并回写节点
//     （request_param / resolved_inputs / patterns / 清空校验错误），然后重新生成
//     params.json 再提交运行；
//   - 等待节点进入终态，返回执行日志、params_path、params.json 正文与本次生效的
//     request_param。
//
// 需要临时换参数直接运行时，可在本次调用里传入 request_param（会持久化到节点）；
// 只想先试算参数而不运行时，用 build_analysis_node_params。
//
// 该工具会改变计算状态（触发一次节点运行），因此声明 analysis_node 授权：与直接执行
// shell / 脚本（execute）区分开，便于按动作粒度配置许可策略（如允许 execute 但限制
// analysis_node，或反之）。上层在执行前会先走权限门禁。
func RunAnalysisNode() tool.Tool {
	return tool.NewFunc(
		RunAnalysisNodeToolName,
		"运行指定分析节点（analysis_node_id 必填），等待其完成后返回节点状态、绑定的 script_id、执行日志（log；截断时用 log_path 读取完整日志）、params_path、params.json 正文与本次生效的 request_param。可选传入 request_param（键为脚本 io_schema 的 name）覆盖本次运行参数；不传则使用节点上已保存的 request_param。运行前会按脚本最新 io_schema.json 解析参数并重新生成 params.json，同时刷新节点的 inputs_patterns / output_patterns / resolved_inputs / request_param 并清空上一轮 input_validation_errors / output_validation_errors。",
		tool.Schema("运行分析节点的入参", map[string]any{
			"analysis_node_id": tool.StringProperty("要运行的分析节点 ID（必填，字符串以保精度）。"),
			"request_param": map[string]any{
				"type":        "object",
				"description": "本次运行的请求参数对象，键为脚本 io_schema.json inputs 中各条目的 name。留空则使用节点上已保存的 request_param。",
			},
			"timeout_seconds": tool.IntegerProperty("等待节点完成的超时（秒），可选，默认 600。"),
		}, "analysis_node_id"),
		func(ctx context.Context, in RunAnalysisNodeInput) (RunAnalysisNodeOutput, error) {
			rc := tool.RuntimeContextFrom(ctx)
			if rc == nil {
				return RunAnalysisNodeOutput{}, fmt.Errorf("run_analysis_node: runtime context is not available in this tool execution")
			}

			nodeID, err := parseAnalysisNodeID(in.AnalysisNodeID)
			if err != nil {
				return RunAnalysisNodeOutput{}, err
			}

			runner := tool.AnalysisNodeRunnerFrom(ctx)
			if runner == nil {
				return RunAnalysisNodeOutput{}, fmt.Errorf("run_analysis_node: analysis node runner is not available in this runtime")
			}

			// 参数解析 / 落库 / 写出 params.json 全部由 runner 完成。
			result, runErr := runner.RunAnalysisNode(ctx, nodeID, in.RequestParam, in.TimeoutSeconds)

			out := RunAnalysisNodeOutput{
				AnalysisNodeID:         nodeID,
				ScriptID:               result.ScriptID,
				Status:                 result.Status,
				Success:                result.Success,
				Log:                    result.Log,
				LogPath:                result.LogPath,
				WorkspaceDir:           result.WorkspaceDir,
				CommandPath:            result.CommandPath,
				ParamsPath:             result.ParamsPath,
				RequestParam:           result.RequestParam,
				ResolvedInputs:         result.ResolvedInputs,
				ParamsJSON:             result.ParamsJSON,
				ParamsUpdated:          len(result.RequestParam) > 0 && !strings.EqualFold(result.Status, "params_failed"),
				InputValidationErrors:  result.InputValidationErrors,
				OutputValidationErrors: result.OutputValidationErrors,
				ParamsWarnings:         result.ParamsWarnings,
			}
			if runErr != nil {
				out.Error = runErr.Error()
			}
			return out, nil
		},
	).WithPermission("analysis_node", "", "运行分析节点（解析参数、触发计算并读取日志）")
}

// parseAnalysisNodeID 解析必填的分析节点 ID：为空或非法时返回错误。
func parseAnalysisNodeID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idStr == "" {
		return 0, fmt.Errorf("analysis_node_id is required")
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid analysis_node_id %q: %w", idStr, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("analysis_node_id must be a positive integer")
	}
	return id, nil
}
