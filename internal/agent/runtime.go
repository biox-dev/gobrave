package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
)

// ErrNoPermissionResolver 表示当前 Runtime 没有绑定权限解析器（无法等待人工决策）。
var ErrNoPermissionResolver = errors.New("agent: no permission resolver in runtime")

// sessionRef 把 Task 上的 int64 会话主键转为权限 / 提问子系统使用的字符串会话标识；
// 0 表示不关联会话。
func sessionRef(sessionID int64) string {
	if sessionID <= 0 {
		return ""
	}
	return strconv.FormatInt(sessionID, 10)
}

// NewStandaloneRuntime 返回一个独立于任务上下文的 Runtime，用于 Client.Invoke / Client.Stream
// 这类“无任务、无 UI”的一次性调用。
//
// 行为约定：
//   - Emit：直接把事件交给 handler（handler 为 nil 时丢弃）；
//   - RequestPermission：应用默认策略，ask 降级为 allow（宽松放行），保证链路可跑通；
//   - WaitPermission：无解析器，直接返回 ErrNoPermissionResolver。
func NewStandaloneRuntime(handler StreamHandler) Runtime {
	return &standaloneRuntime{handler: handler, policy: DefaultPermissionPolicy()}
}

// standaloneRuntime 是 Runtime 的独立实现。
type standaloneRuntime struct {
	handler StreamHandler
	policy  PermissionPolicy
}

func (r *standaloneRuntime) Emit(ctx context.Context, event StreamEvent) error {
	if r.handler != nil {
		return r.handler(ctx, event)
	}
	return nil
}

func (r *standaloneRuntime) RequestPermission(ctx context.Context, userID int64, operation Operation) (PermissionDecision, error) {
	decision := r.policy.Check(ctx, userID, operation)
	if decision == DecisionAsk {
		// 无 UI 可确认：宽松放行，避免阻塞无任务语义的调用。
		return DecisionAllow, nil
	}
	return decision, nil
}

func (r *standaloneRuntime) WaitPermission(context.Context, int64) (PermissionDecision, error) {
	return "", ErrNoPermissionResolver
}

// AskUser 在无 UI 的独立调用中无法向用户提问，直接返回错误，由工具层转为失败结果。
func (r *standaloneRuntime) AskUser(context.Context, int64, AskUserInput) (AskUserAnswer, error) {
	return AskUserAnswer{}, ErrNoUserInputResolver
}

// taskRuntime 是绑定到具体任务的 Runtime：事件持久化 + 广播，权限持久化 + 阻塞等待。
type taskRuntime struct {
	svc     *AgentService
	taskID  int64
	handler StreamHandler
	// autoApprove 为真表示“无人值守”执行：权限 ask 降级为 allow，不落 pending、
	// 不进入 waiting_permission、不阻塞等待人工决策（见 WithAutoApprove）。
	autoApprove bool
}

// RuntimeOption 定制 TaskRuntime 的行为（如权限是否自动放行）。
type RuntimeOption func(*taskRuntime)

// WithAutoApprove 开关「无人值守权限放行」。
//
// 开启后：策略判定为 ask（需人工确认）的操作直接按 allow 放行，既不持久化 pending
// 权限请求，也不把任务置为 waiting_permission；策略明确判定为 deny 的操作仍然拒绝。
// 关闭（默认）时完全保留原有行为：ask → 持久化 pending → 阻塞等待 UI 决策。
//
// 适用场景：没有 UI 承接权限确认的同步调用（RunTaskSync / RunTaskSyncByID）。
func WithAutoApprove(auto bool) RuntimeOption {
	return func(r *taskRuntime) { r.autoApprove = auto }
}

func NewTaskRuntime(svc *AgentService, taskID int64, handler StreamHandler, opts ...RuntimeOption) Runtime {
	r := &taskRuntime{svc: svc, taskID: taskID, handler: handler}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	return r
}

func (r *taskRuntime) Emit(ctx context.Context, event StreamEvent) error {
	// delta / block 分流：
	//   - 完整块与生命周期事件：持久化 + 广播，作为时间线数据源；
	//   - 纯增量（text / reasoning_delta）：仅广播，用于前端实时渲染，不落库。
	//
	// 判断留在 Emit（只有这里能拿到 StreamEvent.Type），但两条分支都走同一个 publish 出口。
	if isBlockEvent(event.Type) {
		r.svc.emitEvent(ctx, r.taskID, EventStream, event)
	} else {
		r.svc.broadcastEvent(ctx, r.taskID, EventStream, event)
	}
	if r.handler != nil {
		return r.handler(ctx, event)
	}
	return nil
}

func (r *taskRuntime) RequestPermission(ctx context.Context, userID int64, operation Operation) (PermissionDecision, error) {
	// 1) 通知 UI：Agent 请求了对某个操作的权限确认。
	// _ = r.Emit(ctx, StreamEvent{Type: StreamEventPermission, Content: marshalOperation(operation)})

	// 2) 策略先行：直接放行 / 直接拒绝。
	if decision := r.svc.policy.Check(ctx, userID, operation); decision != DecisionAsk {
		// r.emitPermissionResult(ctx, string(decision))
		if decision == DecisionDeny {
			return DecisionDeny, ErrPermissionDenied
		}
		return DecisionAllow, nil
	}

	// 3) 无人值守执行（同步调用 / 无 UI）：ask 降级为 allow，直接放行——
	// 不产生 pending 权限请求，也不阻塞等待人工决策。策略 deny 已在上面拒绝。
	if r.autoApprove {
		return DecisionAllow, nil
	}

	// 4) 持久化 pending 权限请求。
	task, err := r.svc.tasks.Get(ctx, r.taskID)
	if err != nil {
		return "", err
	}
	perm, err := r.svc.perms.Create(ctx, r.taskID, sessionRef(task.SessionID), operation)
	if err != nil {
		return "", err
	}

	// 5) 任务进入等待权限状态，并广播事件。
	if err := r.svc.transitionTask(ctx, task, TaskWaitingPermission, ""); err != nil {
		return "", err
	}
	r.svc.emitEvent(ctx, r.taskID, EventPermissionCreated, perm)

	// 6) 阻塞等待 UI 决策。
	decision, err := r.svc.perms.Wait(ctx, perm.ID)
	if err != nil {
		r.emitPermissionResult(ctx, fmt.Sprintf("error: %v", err))
		return "", err
	}

	// 7) 恢复任务为 running（deny 时 Agent 会返回错误，由 execute 置为 failed）。
	_ = r.svc.transitionTask(ctx, task, TaskRunning, "")
	// r.emitPermissionResult(ctx, string(decision))
	return decision, nil
}

// emitPermissionResult 把权限决策结果透传给 UI（StreamEventPermissionResult）。
func (r *taskRuntime) emitPermissionResult(ctx context.Context, content string) {
	_ = r.Emit(ctx, StreamEvent{Type: StreamEventPermissionResult, Content: content})
}

// marshalOperation 把 Operation 序列化为 JSON 字符串，作为权限通知事件的 content。
// func marshalOperation(op Operation) string {
// 	data, err := json.Marshal(op)
// 	if err != nil {
// 		return fmt.Sprintf("type=%s", op.Type)
// 	}
// 	return string(data)
// }

func (r *taskRuntime) WaitPermission(ctx context.Context, permissionID int64) (PermissionDecision, error) {
	return r.svc.perms.Wait(ctx, permissionID)
}

// AskUser 创建提问请求（pending）→ 任务置为等待 → 广播事件 → 阻塞等待用户作答。
//
// 与 RequestPermission 同构：持久化 pending 提问后，Agent 阻塞在 QuestionManager.Wait，
// 用户在 UI 作答 → AnswerQuestion 更新 DB 并唤醒 Agent → 这里返回答案继续执行。
func (r *taskRuntime) AskUser(ctx context.Context, _ int64, input AskUserInput) (AskUserAnswer, error) {
	// 无人值守执行（同步调用 / 无 UI）：没人会作答，快速失败而不是永久阻塞在 Wait 上。
	if r.autoApprove {
		return AskUserAnswer{}, ErrNoUserInputResolver
	}

	task, err := r.svc.tasks.Get(ctx, r.taskID)
	if err != nil {
		return AskUserAnswer{}, err
	}
	question, err := r.svc.questions.Create(ctx, r.taskID, sessionRef(task.SessionID), input)
	if err != nil {
		return AskUserAnswer{}, err
	}

	// 任务进入等待状态，并广播提问事件（UI 据此渲染提问卡片）。
	if err := r.svc.transitionTask(ctx, task, TaskWaitingPermission, ""); err != nil {
		return AskUserAnswer{}, err
	}
	r.svc.emitEvent(ctx, r.taskID, EventQuestionCreated, question)

	// 阻塞等待用户作答。
	answer, err := r.svc.questions.Wait(ctx, question.ID)
	if err != nil {
		return AskUserAnswer{}, err
	}

	// 恢复任务为 running（后续结果由 run 决定终态）。
	_ = r.svc.transitionTask(ctx, task, TaskRunning, "")
	return answer, nil
}
