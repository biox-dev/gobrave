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
// standalone Runtime 不实现该接口；需要用户 / 会话 / 项目事实的 Provider 可通过
// agentruntime.FactsOf 读取，未实现时得到零值。
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

// CheckpointRuntime 是 Runtime 的可选扩展：Agent 循环通过它读写任务的持久化检查点。
//
// 独立 Runtime（NewStandaloneRuntime）不实现该接口，此时 Agent 循环退化为纯内存执行。
// taskRuntime 实现了该接口；native 包通过类型断言使用它，避免把持久化细节耦合进
// Agent 循环本身。
type CheckpointRuntime interface {
	Runtime
	// LoadCheckpoint 读取当前任务的检查点；不存在时返回 nil。
	LoadCheckpoint(ctx context.Context) *types.TaskResumeState
	// SaveCheckpoint 覆盖写入检查点（检查点必须可以在一次写入中完整表达）。
	SaveCheckpoint(ctx context.Context, state *types.TaskResumeState) error
	// ClearCheckpoint 在任务正常结束后清理检查点。
	ClearCheckpoint(ctx context.Context) error
}

// EventBus 是实时通知层接口：只负责“告诉 UI 状态发生了变化”，不承担状态存储。
//
// 状态源是 agent_tasks（任务）与 agent_messages（时间线）；EventBus 之上接
// WS / SSE 做实时推送。
type EventBus interface {
	// Publish 发布一个事件给订阅了该任务（或全部任务）的订阅者。
	Publish(ctx context.Context, event types.AgentEvent)
	// Subscribe 订阅某任务的事件；taskID 为空表示订阅全部任务。
	// 返回的 unsubscribe 用于取消订阅。
	Subscribe(taskID int64, handler types.EventHandler) (unsubscribe func())
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
