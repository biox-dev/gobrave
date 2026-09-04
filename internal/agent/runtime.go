package agent

import (
	"context"
	"errors"
	"fmt"
)

// ErrNoPermissionResolver 表示当前 Runtime 没有绑定权限解析器（无法等待人工决策）。
var ErrNoPermissionResolver = errors.New("agent: no permission resolver in runtime")

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

func (r *standaloneRuntime) RequestPermission(ctx context.Context, userID string, operation Operation) (PermissionDecision, error) {
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

// taskRuntime 是绑定到具体任务的 Runtime：事件持久化 + 广播，权限持久化 + 阻塞等待。
type taskRuntime struct {
	svc     *AgentService
	taskID  int64
	handler StreamHandler
}

func NewTaskRuntime(svc *AgentService, taskID int64, handler StreamHandler) Runtime {
	return &taskRuntime{svc: svc, taskID: taskID, handler: handler}
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

func (r *taskRuntime) RequestPermission(ctx context.Context, userID string, operation Operation) (PermissionDecision, error) {
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

	// 3) 持久化 pending 权限请求。
	task, err := r.svc.tasks.Get(ctx, r.taskID)
	if err != nil {
		return "", err
	}
	perm, err := r.svc.perms.Create(ctx, r.taskID, task.SessionID, operation)
	if err != nil {
		return "", err
	}

	// 4) 任务进入等待权限状态，并广播事件。
	if err := r.svc.transitionTask(ctx, task, TaskWaitingPermission, ""); err != nil {
		return "", err
	}
	r.svc.emitEvent(ctx, r.taskID, EventPermissionCreated, perm)

	// 5) 阻塞等待 UI 决策。
	decision, err := r.svc.perms.Wait(ctx, perm.ID)
	if err != nil {
		r.emitPermissionResult(ctx, fmt.Sprintf("error: %v", err))
		return "", err
	}

	// 6) 恢复任务为 running（deny 时 Agent 会返回错误，由 execute 置为 failed）。
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
