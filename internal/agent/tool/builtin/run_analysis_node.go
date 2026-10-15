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

// analysisNodeIDVarKey 是运行时上下文 SystemPromptVars 中承载分析节点 ID 的键，
// 与 RuntimeContext.SystemPromptVars["analysis_node_id"] 保持一致。
const analysisNodeIDVarKey = "analysis_node_id"

// RunAnalysisNodeInput 是 run_analysis_node 工具的入参。
//
// AnalysisNodeID 允许为空：此时回退到当前运行时上下文（系统提示词里的 ## Analysis Node ID）
// 承载的分析节点，使模型在 analysisNode 上下文里无需显式传 ID 即可运行「当前节点」。
type RunAnalysisNodeInput struct {
	AnalysisNodeID string `json:"analysis_node_id"` // 分析节点 ID（字符串以保精度）；为空时使用当前上下文节点
	TimeoutSeconds int    `json:"timeout_seconds"`  // 等待完成的超时（秒），可选，默认 600
}

// RunAnalysisNodeOutput 是 run_analysis_node 工具返回给模型的结果。
type RunAnalysisNodeOutput struct {
	AnalysisNodeID int64  `json:"analysis_node_id"`
	Status         string `json:"status"`          // 节点终态（success / failed / timeout ...）
	Success        bool   `json:"success"`         // 是否为成功终态
	Log            string `json:"log,omitempty"`   // 节点执行日志（command.log 内容）
	Error          string `json:"error,omitempty"` // 失败原因（如有）
}

// RunAnalysisNode 返回一个「运行分析节点」工具。
//
// 行为：
//   - 解析入参 analysis_node_id（缺省时回退到运行时上下文里的 analysis_node_id）；
//   - 通过注入的 AnalysisNodeRunner 触发节点异步执行并等待其进入终态；
//   - 把节点执行日志（LogPath / command.log）内容回传给模型。
//
// 该工具会改变计算状态（触发一次节点运行），因此声明 analysis_node 授权：与直接执行
// shell / 脚本（execute）区分开，便于按动作粒度配置许可策略（如允许 execute 但限制
// analysis_node，或反之）。上层在执行前会先走权限门禁。
func RunAnalysisNode() tool.Tool {
	return tool.NewFunc(
		RunAnalysisNodeToolName,
		"运行指定（或当前上下文）的分析节点，等待其完成后返回节点的执行日志。用于触发一次计算并检查其运行输出。",
		tool.Schema("运行分析节点的入参", map[string]any{
			"analysis_node_id": tool.StringProperty("要运行的分析节点 ID（字符串以保精度）。留空则运行当前上下文中的分析节点。"),
			"timeout_seconds":  tool.IntegerProperty("等待节点完成的超时（秒），默认 600。"),
		}),
		func(ctx context.Context, in RunAnalysisNodeInput) (RunAnalysisNodeOutput, error) {
			rc := tool.RuntimeContextFrom(ctx)
			if rc == nil {
				return RunAnalysisNodeOutput{}, fmt.Errorf("run_analysis_node: runtime context is not available in this tool execution")
			}

			nodeID, err := resolveAnalysisNodeID(rc, in.AnalysisNodeID)
			if err != nil {
				return RunAnalysisNodeOutput{}, err
			}

			runner := tool.AnalysisNodeRunnerFrom(ctx)
			if runner == nil {
				return RunAnalysisNodeOutput{}, fmt.Errorf("run_analysis_node: analysis node runner is not available in this runtime")
			}

			result, runErr := runner.RunAnalysisNode(ctx, nodeID, in.TimeoutSeconds)
			out := RunAnalysisNodeOutput{
				AnalysisNodeID: nodeID,
				Status:         result.Status,
				Success:        result.Success,
				Log:            result.Log,
			}
			if runErr != nil {
				out.Error = runErr.Error()
				return out, nil
			}
			return out, nil
		},
	).WithPermission("analysis_node", "", "运行分析节点（触发计算并读取日志）")
}

// resolveAnalysisNodeID 解析要运行的分析节点 ID：优先取工具入参，为空时回退到运行时
// 上下文的系统提示词变量 analysis_node_id（即系统提示词里的 ## Analysis Node ID）。
func resolveAnalysisNodeID(rc *tool.RuntimeContext, raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idStr == "" {
		idStr = rc.SystemPromptVar(analysisNodeIDVarKey)
	}
	if idStr == "" {
		return 0, fmt.Errorf("run_analysis_node: analysis_node_id is required (pass it explicitly or run inside an analysisNode context)")
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("run_analysis_node: invalid analysis_node_id %q: %w", idStr, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("run_analysis_node: analysis_node_id must be a positive integer")
	}
	return id, nil
}
