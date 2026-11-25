package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/agent/types"
)

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
	Emit(ctx context.Context, event types.StreamEvent) error
	// RequestPermission 创建权限请求并阻塞等待决策（allow / deny）。
	// 归属用户是执行环境属性，由 Runtime 自己持有（见 TaskAware），不再逐层传参。
	RequestPermission(ctx context.Context, operation types.Operation) (types.PermissionDecision, error)
	// WaitPermission 等待一个已存在权限请求（permissionID）的决策。
	WaitPermission(ctx context.Context, permissionID int64) (types.PermissionDecision, error)
	// AskUser 向用户提问并阻塞等待作答（提问卡片由 UI 渲染）。
	// 无 UI 的独立调用返回 ErrNoUserInputResolver。
	AskUser(ctx context.Context, input types.AskUserInput) (types.AskUserAnswer, error)
}

// TaskAware 是 Runtime 的可选扩展：暴露本次执行绑定的任务事实快照（只读）。
//
// 需要用户 / 会话 / 项目事实的 Provider 可通过 agentruntime.FactsOf 读取，
// Runtime 未实现该接口时得到零值。
type TaskAware interface {
	Facts() types.TaskFacts
}

// Agent 是统一 Agent 调用接口。
// 每个 Provider（claude_code / codex / copilot / custom ...）都需要实现该接口。
type Agent interface {
	// Name 返回 Provider 唯一标识。
	Name() string
	// Invoke 执行一次性任务并同步返回最终结果。
	Invoke(ctx context.Context, req types.Request, rt Runtime) (*types.Result, error)
	// Stream 执行流式请求，通过 rt.Emit 逐块输出事件，返回最终聚合结果。
	Stream(ctx context.Context, req types.Request, rt Runtime) (*types.Result, error)
}

// MessageReplay 是 Runtime 的可选扩展：Agent 循环据此读取本任务已落库的会话消息，
// 在进程重启后重建执行现场。
//
// 恢复所需的事实只有一份：agent_messages。native 循环按 turn_id 把 assistant 文本、
// 工具调用与工具结果归为一轮，已完成的工具不重放，未完成的重新执行（权限 / 提问按
// tool_call_id 复用已落库的决策）。Runtime 未实现该接口时，Agent 循环退化为纯内存执行。
type MessageReplay interface {
	Runtime
	// TaskMessages 返回本任务已落库的消息（按 Seq 升序，含该轮的 user 消息）。
	TaskMessages(ctx context.Context) ([]types.Message, error)
}

// EventBus 是实时通知层接口：只负责“告诉 UI 状态发生了变化”，不承担状态存储。
//
// 状态源是 agent_tasks（任务）与 agent_messages（时间线）；EventBus 之上接
// WS / SSE 做实时推送。
type EventBus interface {
	// Publish 发布一个事件给全部订阅者。
	Publish(ctx context.Context, event types.AgentEvent)
	// Subscribe 订阅全部事件，返回的 unsubscribe 用于取消订阅。
	//
	// 事件范围（任务 / 会话 / 用户）由订阅方按 AgentEvent 上的字段自行过滤，
	// 总线只做进程内广播：会话级订阅与路由见 handler.sessionEventBridge。
	Subscribe(handler types.EventHandler) (unsubscribe func())
}

// PermissionPolicy 判断一个 Operation 应被允许、拒绝，还是需要人工确认。
//
// Agent 在执行前把 Operation 交给策略；策略返回 ask 时，由 PermissionManager
// 持久化一个 pending 的 PermissionRequest 并等待 UI 确认。
type PermissionPolicy interface {
	Check(ctx context.Context, userID int64, operation types.Operation) types.PermissionDecision
}

// UserAgentConfigProvider 按 userID 读取用户的 Agent 配置（用于权限决策）。
type UserAgentConfigProvider interface {
	GetAgentConfig(ctx context.Context, userID int64) (types.UserAgentConfig, error)
}
