package agentruntime

import (
	"context"
	"errors"

	agentpolicy "github.com/biox-dev/gobrave/internal/agent/policy"
	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
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
	return &standaloneRuntime{handler: handler, policy: agentpolicy.DefaultPermissionPolicy()}
}

// NewStandaloneRuntimeWithFacts 创建带执行事实的独立 Runtime。
//
// 无任务的直接调用（如 LLM 桥接）也要让工具拿到归属用户 / 项目，因此事实由调用方
// 显式传入，与任务模式下的 Task.Facts() 语义一致。
func NewStandaloneRuntimeWithFacts(handler StreamHandler, facts TaskFacts) Runtime {
	return &standaloneRuntime{handler: handler, policy: agentpolicy.DefaultPermissionPolicy(), facts: facts}
}

// standaloneRuntime 是 Runtime 的独立实现。
type standaloneRuntime struct {
	handler StreamHandler
	policy  PermissionPolicy
	facts   TaskFacts
}

// Facts 实现 TaskAware：返回调用方传入的执行事实（可能为零值）。
func (r *standaloneRuntime) Facts() TaskFacts { return r.facts }

func (r *standaloneRuntime) Emit(ctx context.Context, event StreamEvent) error {
	if r.handler != nil {
		return r.handler(ctx, event)
	}
	return nil
}

func (r *standaloneRuntime) RequestPermission(ctx context.Context, operation Operation) (PermissionDecision, error) {
	// 独立调用没有归属用户，按 0（默认策略）判定。
	decision := r.policy.Check(ctx, 0, operation)
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
func (r *standaloneRuntime) AskUser(context.Context, AskUserInput) (AskUserAnswer, error) {
	return AskUserAnswer{}, ErrNoUserInputResolver
}
