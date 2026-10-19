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

	"github.com/biox-dev/gobrave/internal/agent/message"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// Provider 名称常量。新增第三方 Agent 或自研 Agent 时在此登记一个唯一名称。
const (
	ProviderMock       = "mock"        // 自研 mock，用于保证链路可跑通
	ProviderClaudeCode = "claude_code" // Anthropic Claude Code CLI
	ProviderCodex      = "codex"       // OpenAI Codex CLI
	ProviderCopilot    = "copilot"     // GitHub Copilot CLI
	ProviderCustom     = "custom"      // 自研 Agent（内置 LLM 协议客户端 + 工具循环）
)

// DefaultProvider 是未配置时的兜底 Provider。
const DefaultProvider = ProviderMock

// Message 是对话消息的类型别名：持久化模型与 Agent 请求消息统一到 message 包。
type Message = message.Message

// MessageRole 常量（别名到 message 包）。
const (
	RoleSystem    = message.RoleSystem
	RoleUser      = message.RoleUser
	RoleAssistant = message.RoleAssistant
	RoleTool      = message.RoleTool
)

// MessageKind 定义会话层可回放的消息类型（别名到 message 包）。
const (
	MessageKindUser           = message.KindUser
	MessageKindAssistant      = message.KindAssistant
	MessageKindAssistantFinal = message.KindAssistantFinal
	MessageKindReasoning      = message.KindReasoning
	MessageKindToolCall       = message.KindToolCall
	MessageKindToolResult     = message.KindToolResult
)

// ErrNotImplemented 表示某个 Provider 尚未实现真实调用。
var ErrNotImplemented = errors.New("agent: provider not implemented yet")

// Request 描述一次 Agent 调用请求，与具体 Provider 无关。
type Request struct {
	// Provider 可选：强制指定 Provider 名称；为空则使用 Client 的默认 Provider。
	Provider string `json:"provider"`
	// ModelID 可选：模型标识（对应 config.agent.providers 的 key），为空时由 Provider 自行决定。
	ModelID string `json:"model_id"`
	// SessionID 可选：会话主键（雪花 ID），用于把多次调用关联到同一会话（与权限 / 任务联动）。
	SessionID int64 `json:"session_id,string,omitempty"`
	// ProfileID 可选：AgentProfile ID，AgentService 据此解析系统提示词、技能与上下文注入开关。
	ProfileID int64 `json:"profile_id,omitempty,string"`
	// Skills 可选：本次调用启用的技能名列表；为空表示使用默认全部技能。
	Skills []string `json:"skills,omitempty"`
	// RuntimeContext 是本轮调用的运行时上下文（业务类型 / 变量 / 工作目录 / 归属用户）。
	//
	// 由 HTTP Handler 的 RuntimeContextResolver 解析 env(type,id) 产出，随请求一路传到
	// 配置了工具的 Provider；Provider 在每次调用时把它注入工具执行上下文，供
	// builtin 工具（如 run_analysis_node）在执行期读取 SystemPromptVars。
	//
	// 它同时是「请求级用户事实」的唯一来源：UserID / SessionID 都在这里，
	// 权限策略、ask_user 归属、记忆检索与提取统一从它读取（见 RequestUserID）。
	//
	// 它只承载可序列化的业务事实，并随 Request 一起被 JSON 序列化进 agent_tasks.request
	// （Task.Request 为 gorm serializer:json），因此在任务从 DB 恢复后依然可用
	// （见 ToolRuntimeContextFromRequest）。工具执行期所需的服务能力（如
	// AnalysisNodeRunner）不在此处，而由 agent.Options 承载。
	RuntimeContext *tool.RuntimeContext `json:"runtime_context,omitempty"`
	// SystemPrompt 是最终发送给 Provider 的系统提示词。
	//
	// 注意：它不再由调用方直接设置，而是 AgentService 依据 Profile.SystemPrompt 模板 +
	// RuntimeContext.SystemPromptVars 渲染后写入（见 applyProfile）；Provider 只读该字段。
	SystemPrompt string `json:"system_prompt,omitempty"`
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

// RequestUserID 返回请求归属用户 ID（来自 RuntimeContext）；无上下文时返回 0。
//
// 用户 ID 不再是 Request 的独立字段：RuntimeContext 是请求级事实的唯一载体（随请求
// 序列化进 agent_tasks.request，任务恢复后仍可用），权限策略、ask_user 归属、记忆
// 检索与提取统一通过本函数读取，避免两处来源不一致。
func (r Request) RequestUserID() int64 {
	return r.RuntimeContext.UserIDValue()
}

// RequestSessionID 返回请求所属会话 ID（会话主键）：优先取 RuntimeContext.SessionID，
// 回退到 Request.SessionID；都没有时返回 0。
func (r Request) RequestSessionID() int64 {
	if r.RuntimeContext != nil && r.RuntimeContext.SessionID > 0 {
		return r.RuntimeContext.SessionID
	}
	return r.SessionID
}

// Result 是一次 Invoke / Stream 调用的最终聚合结果。
type Result struct {
	// TaskID 本次调用对应的 Agent 任务 ID（agent_tasks 表），仅任务模式（RunTaskSync）下返回。
	TaskID  int64           `json:"task_id,string,omitempty"`
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
	StreamEventTurnStart  StreamEventType = "turn_start"  // 一轮开始（Data 为 TurnBlock）
	StreamEventTurnEnd    StreamEventType = "turn_end"    // 一轮结束（Data 为 TurnBlock）
	StreamEventReasoning  StreamEventType = "reasoning"   // 完整思考块（Data 为 ReasoningBlock）
	StreamEventMessage    StreamEventType = "message"     // 完整 assistant 消息（Data 为 MessageBlock）
	StreamEventToolCall   StreamEventType = "tool_call"   // 完整工具调用（Data 为 ToolCall）
	StreamEventToolResult StreamEventType = "tool_result" // 工具调用结果

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
	// userID 用于按用户读取其自定义的许可策略（空表示无用户，走默认策略）。
	RequestPermission(ctx context.Context, userID int64, operation Operation) (PermissionDecision, error)
	// WaitPermission 等待一个已存在权限请求（permissionID）的决策。
	WaitPermission(ctx context.Context, permissionID int64) (PermissionDecision, error)
	// AskUser 向用户提问并阻塞等待作答（提问卡片由 UI 渲染）。
	// userID 用于提问归属；无 UI 的独立调用返回 ErrNoUserInputResolver。
	AskUser(ctx context.Context, userID int64, input AskUserInput) (AskUserAnswer, error)
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
