package agent

import (
	"context"
	"errors"
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

func (r *standaloneRuntime) RequestPermission(ctx context.Context, operation Operation) (PermissionDecision, error) {
	decision := r.policy.Check(ctx, operation)
	if decision == DecisionAsk {
		// 无 UI 可确认：宽松放行，避免阻塞无任务语义的调用。
		return DecisionAllow, nil
	}
	return decision, nil
}

func (r *standaloneRuntime) WaitPermission(context.Context, string) (PermissionDecision, error) {
	return "", ErrNoPermissionResolver
}
