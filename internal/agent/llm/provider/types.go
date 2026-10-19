package provider

// Package provider 是 LLM 协议客户端层（迁移自 opencode internal/llm/provider）。
//
// 它只负责「一轮模型调用」：把对话历史与工具定义转换成各家 SDK 的请求，
// 并把流式响应归一化为 Event。Agent 循环（工具执行 / 事件输出）在 native 包，
// 二者通过本包的 Provider 接口解耦。

import (
	"context"

	"github.com/biox-dev/gobrave/internal/agent/llm/models"
)

// Role 是对话消息角色。
type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall 是模型发起的一次工具调用。
type ToolCall struct {
	ID    string
	Name  string
	Input string // JSON 参数
}

// ToolResult 是一条工具调用结果（Role == RoleTool 的消息按 ToolResults 回填）。
type ToolResult struct {
	ToolCallID string
	Content    string
	IsError    bool
}

// Message 是发给模型的对话消息。
//
// 与 gobrave 的 message.Message 不同，它额外承载工具调用 / 结果，用于在
// Agent 循环内部维护「assistant(tool_calls) → tool(results)」的完整往返。
type Message struct {
	Role        Role
	Content     string
	ToolCalls   []ToolCall
	ToolResults []ToolResult
}

// FinishReason 是一轮调用的终止原因。
type FinishReason string

const (
	FinishEndTurn  FinishReason = "end_turn"
	FinishMaxToken FinishReason = "max_tokens"
	FinishToolUse  FinishReason = "tool_use"
	FinishUnknown  FinishReason = "unknown"
)

// Usage 是 token 用量统计。
type Usage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheCreationTokens int64
	CacheReadTokens     int64
}

// Response 是一轮调用的聚合结果。
type Response struct {
	Content      string
	ToolCalls    []ToolCall
	Usage        Usage
	FinishReason FinishReason
}

// EventType 是流式事件类型。
type EventType string

const (
	EventContentDelta  EventType = "content_delta"
	EventThinkingDelta EventType = "thinking_delta"
	EventComplete      EventType = "complete"
	EventError         EventType = "error"
)

// Event 是流式响应中的一个事件。
type Event struct {
	Type     EventType
	Content  string
	Thinking string
	Response *Response
	Error    error
}

// Tool 是暴露给模型的工具定义。
type Tool struct {
	Name        string
	Description string
	// InputSchema 是完整的 object JSON Schema（type / properties / required）。
	InputSchema map[string]any
}

// Options 描述一次调用的连接与模型参数。
type Options struct {
	Provider        models.ModelProvider
	BaseURL         string
	APIKey          string
	SystemPrompt    string
	Model           models.Model
	MaxTokens       int64
	ReasoningEffort string
}

// Client 是一个模型厂商的流式调用客户端。
type Client interface {
	// Stream 发起一轮流式调用；事件通道在结束或出错后关闭。
	Stream(ctx context.Context, messages []Message, tools []Tool) <-chan Event
}
