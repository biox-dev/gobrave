package types

import (
	"context"
	"encoding/json"
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
