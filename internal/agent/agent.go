// Package agent 定义 AI Agent 调用的统一抽象层。
//
// 它屏蔽了具体第三方 Agent（Claude Code / Codex / Copilot）与后续自研 Agent 的差异，
// 向上层业务（AISummaryWorker、LLMHandler 等）统一暴露两种调用语义：
//
//   - Invoke：一次性任务调用，同步等待最终结果。适合 AI 摘要、后台任务等场景。
//   - Stream：流式调用，通过 Runtime.Emit 逐块输出事件。适合前端聊天等场景。
//
// 除“调用”本身外，本包还实现了 design.md 中描述的“执行架构”：
//
//   - Agent：执行层，负责产生文本 / 推理 / 工具调用等事件，并在需要时请求权限。
//   - Runtime：Agent 执行期的运行时环境（Emit / RequestPermission / WaitPermission）。
//   - Operation / PermissionPolicy：描述“要做什么”以及“能不能做”。
//   - PermissionManager / PermissionRequest：持久化的权限请求与批准 / 拒绝。
//   - Task / TaskManager（AgentService）：任务状态机与编排。
//   - EventBus / AgentEvent：实时通知层（带 sequence，可恢复）。
//   - Repository：持久化抽象（默认提供内存实现，后续可替换为 DB）。
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
	// SessionID 可选：会话标识，用于把多次调用关联到同一会话（与权限 / 任务联动）。
	SessionID string `json:"session_id"`
	// UserID 可选：发起调用的用户，用于记忆（memory）的归属隔离与检索。
	UserID string `json:"user_id,omitempty"`
	// Profile 可选：AgentProfile 名称，AgentService 据此解析系统提示词、技能与上下文注入开关。
	Profile string `json:"profile,omitempty"`
	// Skills 可选：本次调用启用的技能名列表；为空表示使用默认全部技能。
	Skills []string `json:"skills,omitempty"`
	// SystemPrompt 系统提示词。
	SystemPrompt string `json:"system_prompt"`
	// Messages 对话上下文（不含 SystemPrompt）。
	Messages []Message `json:"messages"`
	// WorkingDir 可选：Agent 执行的工作目录。
	WorkingDir string `json:"working_dir"`
	// Env 可选：额外环境变量（也可携带 Provider 特有开关）。
	Env map[string]string `json:"env"`
	// MaxTokens 可选：最大输出 token 数，0 表示不限制。
	MaxTokens int `json:"max_tokens"`
	// Stream 可选：任务模式下是否使用流式调用；默认 false 使用 Invoke。
	Stream bool `json:"stream"`
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
//
// 事件分为两类：
//   - 增量（delta）：text / reasoning_delta，用于前端实时渲染，默认不落库（仅走 WS/SSE 广播）；
//   - 完整块（block）：turn_start / turn_end / reasoning / message / tool_call / tool_result，
//     是时间线的 source of truth，会持久化到 EventRepository。
type StreamEventType string

const (
	// —— 增量（delta）——
	StreamEventText           StreamEventType = "text"            // 文本增量
	StreamEventReasoningDelta StreamEventType = "reasoning_delta" // 思维链/推理增量

	// —— 完整块（block）——
	StreamEventTurnStart   StreamEventType = "turn_start"   // 一轮开始（Data 为 TurnBlock）
	StreamEventTurnEnd     StreamEventType = "turn_end"     // 一轮结束（Data 为 TurnBlock）
	StreamEventReasoning   StreamEventType = "reasoning"    // 完整思考块（Data 为 ReasoningBlock）
	StreamEventMessage     StreamEventType = "message"      // 完整 assistant 消息（Data 为 MessageBlock）
	StreamEventToolCall    StreamEventType = "tool_call"    // 完整工具调用（Data 为 ToolCall）
	StreamEventToolResult  StreamEventType = "tool_result"  // 工具调用结果
	StreamEventSkillCall   StreamEventType = "skill_call"   // 完整技能调用（Data 为 SkillCall）
	StreamEventSkillResult StreamEventType = "skill_result" // 技能调用结果

	StreamEventPermission       StreamEventType = "permission"        // 权限请求通知（真正状态见 PermissionRequest）
	StreamEventPermissionResult StreamEventType = "permission_result" // 权限决策结果通知
	StreamEventDone             StreamEventType = "done"              // 正常结束
	StreamEventError            StreamEventType = "error"             // 出错结束
)

// StreamEvent 是一次流式输出过程中的单个事件。
type StreamEvent struct {
	Type    StreamEventType `json:"type"`
	Content string          `json:"content,omitempty"` // 仅增量事件使用
	Data    any             `json:"data,omitempty"`    // 完整块的结构化数据（reasoning/message/tool_call/turn）
	Err     error           `json:"-"`                 // 仅当 Type == StreamEventError 时有效
}

// isBlockEvent 判断事件类型是否应被持久化（作为时间线数据源）。
//
// 只有纯增量事件（text / reasoning_delta）是“临时”的：它们用于前端实时渲染，
// 默认只广播不落库；其余事件（完整块 + done/error/permission 等生命周期信号）都会落库。
func isBlockEvent(t StreamEventType) bool {
	switch t {
	case StreamEventText, StreamEventReasoningDelta:
		return false
	default:
		return true
	}
}

// ReasoningBlock 是一段完整的思考内容（对应 Provider 的完整 reasoning 事件）。
type ReasoningBlock struct {
	ID      string `json:"id"`      // reasoningId
	Content string `json:"content"` // 完整思考文本
}

// ToolCall 是一次完整的工具调用（对应 AssistantMessageToolRequest）。
type ToolCall struct {
	ID        string `json:"id"`   // toolCallId
	Name      string `json:"name"` // 工具名
	Arguments any    `json:"arguments,omitempty"`
}

// MessageBlock 是一条完整的 assistant 消息（对应 AssistantMessageData），
// 包含它发起的工具调用列表。
type MessageBlock struct {
	ID        string     `json:"id"`                // messageId
	TurnID    string     `json:"turn_id,omitempty"` // 所属 turn
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// TurnBlock 描述一轮（turn）的边界（对应 turn_start / turn_end 事件）。
type TurnBlock struct {
	TurnID string `json:"turn_id"`
	Model  string `json:"model,omitempty"`
}

// StreamHandler 消费流式事件；返回非 nil 错误时中断流式调用。
type StreamHandler func(ctx context.Context, event StreamEvent) error

// Runtime 是 Agent 执行期间的运行时环境，由上层（Client 或 AgentService）注入。
//
// Agent 通过它完成三件事：
//   - Emit：向外输出流式事件（文本 / 推理 / 工具调用 / 权限通知等）；
//   - RequestPermission：创建一个权限请求并阻塞等待用户 / 策略决策；
//   - WaitPermission：等待一个已存在的权限请求的决策（用于恢复场景）。
//
// 该接口把 Agent 与“持久化、通知、权限决策”等业务关注点解耦：
// Agent 只描述“要做什么”，具体“能不能做”由 Runtime 背后的 PermissionManager / Policy 决定。
type Runtime interface {
	// Emit 输出一个流式事件。
	Emit(ctx context.Context, event StreamEvent) error
	// RequestPermission 创建权限请求并阻塞等待决策（allow / deny）。
	RequestPermission(ctx context.Context, operation Operation) (PermissionDecision, error)
	// WaitPermission 等待一个已存在权限请求（permissionID）的决策。
	WaitPermission(ctx context.Context, permissionID string) (PermissionDecision, error)
}

// Agent 是统一 Agent 调用接口。
// 每个 Provider（claude_code / codex / copilot / custom ...）都需要实现该接口。
type Agent interface {
	// Name 返回 Provider 唯一标识。
	Name() string
	// Invoke 执行一次性任务并同步返回最终结果。
	Invoke(ctx context.Context, req Request, rt Runtime) (*Result, error)
	// Stream 执行流式请求，通过 rt.Emit 逐块输出事件，返回最终聚合结果。
	Stream(ctx context.Context, req Request, rt Runtime) (*Result, error)
}
