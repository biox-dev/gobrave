// Package agent 定义 AI Agent 调用的统一抽象层。
//
// 它屏蔽了具体第三方 Agent（Claude Code / Codex / Copilot）与后续自研 Agent 的差异，
// 向上层业务（AISummaryWorker、LLMHandler 等）统一暴露两种调用语义：
//
//   - Invoke：一次性任务调用，同步等待最终结果。适合 AI 摘要、后台任务等场景。
//   - Stream：流式调用，通过 StreamHandler 逐块消费输出。适合前端聊天等场景。
//
// 各 Provider 通过 Registry 注册，运行时按名称解析；Client 是统一门面，负责
// 解析请求应使用的 Provider 并转发 Invoke / Stream。
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Provider 名称常量。新增第三方 Agent 或自研 Agent 时在此登记一个唯一名称。
const (
	ProviderMock       = "mock"        // 自研 mock，用于保证链路可跑通
	ProviderClaudeCode = "claude_code" // Anthropic Claude Code CLI
	ProviderCodex      = "codex"       // OpenAI Codex CLI
	ProviderCopilot    = "copilot"     // GitHub Copilot CLI
	ProviderCustom     = "custom"      // 预留：后续自研 Agent
)

// DefaultProvider 是未配置时的兜底 Provider。
const DefaultProvider = ProviderMock

// MessageRole 常量。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// ErrNotImplemented 表示某个 Provider 尚未实现真实调用。
var ErrNotImplemented = errors.New("agent: provider not implemented yet")

// Message 是一条对话消息。
type Message struct {
	Role    string `json:"role"`    // system / user / assistant / tool
	Content string `json:"content"` // 文本内容
}

// Request 描述一次 Agent 调用请求，与具体 Provider 无关。
type Request struct {
	// Provider 可选：强制指定 Provider 名称；为空则使用 Client 的默认 Provider。
	Provider string `json:"provider"`
	// Model 可选：模型名称，为空时由 Provider 自行决定。
	Model string `json:"model"`
	// SystemPrompt 系统提示词。
	SystemPrompt string `json:"system_prompt"`
	// Messages 对话上下文（不含 SystemPrompt）。
	Messages []Message `json:"messages"`
	// WorkingDir 可选：Agent 执行的工作目录。
	WorkingDir string `json:"working_dir"`
	// Env 可选：额外环境变量。
	Env map[string]string `json:"env"`
	// MaxTokens 可选：最大输出 token 数，0 表示不限制。
	MaxTokens int `json:"max_tokens"`
	// Timeout 可选：单次调用超时，0 表示不限制。
	Timeout time.Duration `json:"-"`
}

// Usage 统计 token 用量。
type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
}

// Result 是一次 Invoke / Stream 调用的最终聚合结果。
type Result struct {
	Content string          `json:"content"`
	Usage   Usage           `json:"usage"`
	Raw     json.RawMessage `json:"raw,omitempty"` // Provider 原始返回，便于调试与扩展
}

// StreamEventType 枚举流式事件类型。
type StreamEventType string

const (
	StreamEventText       StreamEventType = "text"        // 文本增量
	StreamEventReasoning  StreamEventType = "reasoning"   // 思维链/推理增量
	StreamEventToolCall   StreamEventType = "tool_call"   // 发起工具调用
	StreamEventToolResult StreamEventType = "tool_result" // 工具调用结果
	StreamEventDone       StreamEventType = "done"        // 正常结束
	StreamEventError      StreamEventType = "error"       // 出错结束
)

// StreamEvent 是一次流式输出过程中的单个事件。
type StreamEvent struct {
	Type    StreamEventType `json:"type"`
	Content string          `json:"content,omitempty"`
	Err     error           `json:"-"` // 仅当 Type == StreamEventError 时有效
}

// StreamHandler 消费流式事件；返回非 nil 错误时中断流式调用。
type StreamHandler func(ctx context.Context, event StreamEvent) error

// Agent 是统一 Agent 调用接口。
// 每个 Provider（claude_code / codex / copilot / custom ...）都需要实现该接口。
type Agent interface {
	// Name 返回 Provider 唯一标识。
	Name() string
	// Invoke 执行一次性任务并同步返回最终结果。
	Invoke(ctx context.Context, req Request) (*Result, error)
	// Stream 执行流式请求，通过 handler 逐块消费输出，返回最终聚合结果。
	Stream(ctx context.Context, req Request, handler StreamHandler) (*Result, error)
}
