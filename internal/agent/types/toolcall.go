package types

import (
	"context"
	"encoding/json"
	"strings"
)

// 本文件定义「工具调用」在领域层的身份与数据形态。
//
// 工具调用 ID / 轮次 ID 是任务中断后恢复执行的全部依据：
//   - tool_call_id 把 assistant 的工具调用与对应的结果配对（agent_messages.tool_call_id）；
//   - turn_id 把同一次模型响应产生的 assistant 文本 / tool_call / tool_result 归为一轮
//     （agent_messages.turn_id），恢复时据此算出「哪些调用还没执行」。
//
// 恢复所需的事实全部来自 agent_messages，不再有独立的检查点存储。

// ToolResultBlock 是一次完整的工具执行结果（StreamEventToolResult 的 Data）。
type ToolResultBlock struct {
	ToolCallID string `json:"tool_call_id"`
	// TurnID 是发起该调用的模型响应轮次（与对应 tool_call 消息一致）。
	TurnID  string `json:"turn_id,omitempty"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

// ToolRound 是一次「模型响应 → 工具调用」往返的已落库事实。
//
// 同一 TurnID 的 assistant 文本 / 工具调用 / 工具结果构成一轮：
// Calls 是本轮模型发起的调用（按模型输出顺序），Results 是其中已执行完成的部分。
// 二者求差即可得到「恢复时需要重放的调用」。
type ToolRound struct {
	TurnID  string
	Content string
	// Reasoning 是本轮 assistant 的思考内容。思考模式下带 tool_calls 的消息必须把它
	// 原样回传给厂商（DeepSeek 等会把缺失视为请求非法）。
	Reasoning string
	Calls     []ToolCall
	Results   []ToolResultBlock
}

// Result 返回指定工具调用的执行结果。
func (r ToolRound) Result(callID string) (ToolResultBlock, bool) {
	if callID == "" {
		return ToolResultBlock{}, false
	}
	for _, res := range r.Results {
		if res.ToolCallID == callID {
			return res, true
		}
	}
	return ToolResultBlock{}, false
}

// Messages 把一轮还原为发给模型的消息：assistant（文本 / 思考 / 工具调用）
// 与紧随其后的 tool（工具结果）。
//
// 没有工具调用时只返回一条 assistant 文本消息，且不携带思考内容：
// 思考模式的厂商只要求「带 tool_calls 的 assistant 消息」回传 reasoning_content，
// 跨轮把 CoT 当成正文发回去既无意义也浪费 token。
func (r ToolRound) Messages() []Message {
	if len(r.Calls) == 0 {
		if strings.TrimSpace(r.Content) == "" {
			return nil
		}
		return []Message{{Role: RoleAssistant, Content: r.Content}}
	}

	results := make([]ToolResultBlock, 0, len(r.Calls))
	for _, call := range r.Calls {
		res, ok := r.Result(call.ID)
		if !ok {
			// 结果缺失（落库失败 / 数据被清理）：补一条错误结果，保持
			// assistant(tool_calls) 与 tool 消息一一对应，避免 Provider 拒绝请求。
			res = ToolResultBlock{ToolCallID: call.ID, Content: "tool result was not persisted", IsError: true}
		}
		results = append(results, res)
	}
	return []Message{
		{Role: RoleAssistant, Content: r.Content, Reasoning: r.Reasoning, ToolCalls: r.Calls},
		{Role: RoleTool, ToolResults: results},
	}
}

// GroupToolRounds 把消息按 turn_id 归为若干轮，保持落库顺序。
//
// 只处理 Agent 侧消息：assistant 文本、思考、工具调用、工具结果。user / error
// 等其它类型由调用方处理或忽略。
//
// 分组是「连续相同 turn_id」语义而不是全局聚合：历史消息的 turn_id 可能为空
// （早期数据或未上报轮次标识的 Provider），把空值全局聚合会把不相干的多轮并成
// 一轮；按连续段归组则天然保持时间线原样。
func GroupToolRounds(msgs []Message) []ToolRound {
	rounds := make([]ToolRound, 0, 4)
	var current *ToolRound

	// round 返回消息所属的轮次：turn_id 变化即开启新一轮。轮内消息在时间线上是
	// 连续的（思考 → assistant 文本 → 工具调用 → 工具结果），因此按连续段归组即可。
	round := func(turnID string) *ToolRound {
		if current != nil && current.TurnID == turnID {
			return current
		}
		rounds = append(rounds, ToolRound{TurnID: turnID})
		current = &rounds[len(rounds)-1]
		return current
	}

	for _, m := range msgs {
		switch m.Kind {
		case KindAssistant:
			if strings.TrimSpace(m.Content) == "" {
				continue
			}
			r := round(m.TurnID)
			if r.Content != "" {
				r.Content += "\n"
			}
			r.Content += m.Content
		case KindReasoning:
			// 思考内容与 assistant 消息一起回传，缺了它思考模式下的请求会被厂商拒绝。
			if content, ok := ReasoningBlockContent(m.Data); ok && strings.TrimSpace(content) != "" {
				round(m.TurnID).Reasoning = content
			}
		case KindToolCall:
			if call, ok := ToolCallFromData(m.Data); ok {
				r := round(m.TurnID)
				r.Calls = append(r.Calls, call)
			}
		case KindToolResult:
			if res, ok := ToolResultFromData(m.Data); ok {
				r := round(m.TurnID)
				r.Results = append(r.Results, res)
			}
		}
	}
	return rounds
}

// ToolCallFromData 从 tool_call 事件 / 消息的 Data 还原工具调用。
//
// Data 可能是内存态结构体，也可能是从 agent_messages 读回的 JSON map（serializer:json）。
func ToolCallFromData(data any) (ToolCall, bool) {
	switch v := data.(type) {
	case ToolCall:
		return v, v.ID != ""
	case *ToolCall:
		if v == nil {
			return ToolCall{}, false
		}
		return *v, v.ID != ""
	case map[string]any:
		call := ToolCall{Arguments: v["arguments"]}
		call.ID, _ = v["id"].(string)
		call.TurnID, _ = v["turn_id"].(string)
		call.Name, _ = v["name"].(string)
		return call, call.ID != ""
	default:
		return ToolCall{}, false
	}
}

// ToolResultFromData 从 tool_result 事件 / 消息的 Data 还原工具执行结果。
func ToolResultFromData(data any) (ToolResultBlock, bool) {
	switch v := data.(type) {
	case ToolResultBlock:
		return v, v.ToolCallID != ""
	case *ToolResultBlock:
		if v == nil {
			return ToolResultBlock{}, false
		}
		return *v, v.ToolCallID != ""
	case map[string]any:
		res := ToolResultBlock{}
		res.ToolCallID, _ = v["tool_call_id"].(string)
		res.TurnID, _ = v["turn_id"].(string)
		res.Name, _ = v["name"].(string)
		res.Content, _ = v["content"].(string)
		res.IsError, _ = v["is_error"].(bool)
		return res, res.ToolCallID != ""
	default:
		return ToolResultBlock{}, false
	}
}

// ToolCallArguments 把工具调用的参数还原为 JSON 原文。
//
// 内存态下 Arguments 可能是原始字符串（Provider 给的 JSON 文本），也可能是结构化值
// （事件经 JSON 落库再读回）；两种情况统一成 Provider 需要的 JSON 字符串。
func ToolCallArguments(args any) string {
	switch v := args.(type) {
	case nil:
		return ""
	case string:
		return v
	case json.RawMessage:
		return string(v)
	}
	b, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return string(b)
}

// 工具调用 ID 的上下文键。
//
// 它把「当前执行的是哪个 tool call」传给权限 / 提问子系统：
//   - 权限请求写入 Operation.Metadata，重启后据此把已批准的权限与待执行工具精确配对；
//   - 提问请求写入 QuestionRequest.ToolCallID，重启后可复用已作答的结果。
type toolCallIDContextKey struct{}

// WithToolCallID 把工具调用 ID 注入 ctx；id 为空时原样返回。
func WithToolCallID(ctx context.Context, id string) context.Context {
	if id == "" || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, toolCallIDContextKey{}, id)
}

// ToolCallIDFromContext 读取当前工具调用 ID；不存在时返回空串。
func ToolCallIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(toolCallIDContextKey{}).(string)
	return id
}

// OperationMetadataToolCallID 是 Operation.Metadata 中记录工具调用 ID 的键。
const OperationMetadataToolCallID = "tool_call_id"
