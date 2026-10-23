// Package types 定义 Agent 领域的数据库实体与值对象。
//
// 本包只包含可序列化的领域数据（Task / Session / Message / PermissionRequest /
// QuestionRequest / Profile / Memory 等）以及请求 / 结果 / 事件等值对象，不包含
// 任何持久化或编排实现。Repository 与 Service 接口见 types/interface，
// 实现在 application/repository 与 application/service。
package types

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
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

// ErrNotImplemented 表示某个 Provider 尚未实现真实调用。
var ErrNotImplemented = errors.New("agent: provider not implemented yet")

// Request 描述一次 Agent 调用请求，与具体 Provider 无关。
type Request struct {
	// Provider 可选：强制指定 Provider 名称；为空则使用 Client 的默认 Provider。
	Provider string `json:"provider"`
	// ModelID 可选：模型标识（对应 config.agent.providers 的 key），为空时由 Provider 自行决定。
	ModelID string `json:"model_id"`
	// ProfileID 可选：AgentProfile ID，AgentService 据此解析系统提示词、技能与上下文注入开关。
	ProfileID int64 `json:"profile_id,omitempty,string"`
	// Skills 可选：本次调用启用的技能名列表；为空表示使用默认全部技能。
	Skills []string `json:"skills,omitempty"`
	// RuntimeContext 是本轮调用的业务上下文（业务类型 / 模板变量 / 工作目录）。
	//
	// 由 HTTP Handler 的 RuntimeContextResolver 解析 env(type,id) 产出，随请求一路传到
	// 配置了工具的 Provider；Provider 在每次调用时把它注入工具执行上下文，供
	// builtin 工具（如 run_analysis_node）在执行期读取 SystemPromptVars。
	//
	// 它只承载业务事实；「谁在执行」（任务 / 会话 / 用户 / 项目）由 Task 与 TaskFacts
	// 承载，经 Runtime 注入工具 ctx（见 tool.WithTaskFacts）。它随 Request 一起被 JSON
	// 序列化进 agent_tasks.request（gorm serializer:json），因此任务从 DB 恢复后依然可用。
	RuntimeContext *runtimecontext.RuntimeContext `json:"runtime_context,omitempty"`
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
//     是时间线的 source of truth，由 TimelineRecorder 投影到 agent_messages。
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

// IsBlockEvent 判断事件类型是否应被持久化（作为时间线数据源）。
//
// 只有纯增量事件（text / reasoning_delta）是“临时”的：它们用于前端实时渲染，
// 默认只广播不落库；其余事件（完整块 + done/error/permission 等生命周期信号）都会落库。
func IsBlockEvent(t StreamEventType) bool {
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
