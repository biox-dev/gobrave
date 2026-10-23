package native

import (
	"context"

	agentpolicy "github.com/biox-dev/gobrave/internal/agent/policy"
	agentruntime "github.com/biox-dev/gobrave/internal/agent/runtime"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// memoryRuntime 是 native 循环内部使用的最小 Runtime（无任务、无 UI）。
//
// 它只服务一个确实没有任务承接的场景：子 Agent 没有可落库的子会话
// （SessionStore 未注入 / 父会话缺失 / 创建子会话失败）。
//
// 任务模式下的正常路径始终由 AgentService 注入 taskRuntime；native.Config.Run
// 也已明确要求 Runtime 非 nil，不会走到这里。
// 行为与已删除的 agentruntime.standaloneRuntime 保持一致：事件丢弃、ask 宽松放行、
// 无法等待权限或向用户提问。
type memoryRuntime struct {
	facts  agenttypes.TaskFacts
	policy agentinterfaces.PermissionPolicy
}

func newMemoryRuntime(facts agenttypes.TaskFacts) *memoryRuntime {
	return &memoryRuntime{facts: facts, policy: agentpolicy.DefaultPermissionPolicy()}
}

// Facts 实现 TaskAware：返回构造时绑定的执行事实。
func (r *memoryRuntime) Facts() agenttypes.TaskFacts { return r.facts }

func (r *memoryRuntime) Emit(context.Context, agenttypes.StreamEvent) error { return nil }

func (r *memoryRuntime) RequestPermission(ctx context.Context, op agenttypes.Operation) (agenttypes.PermissionDecision, error) {
	decision := r.policy.Check(ctx, 0, op)
	if decision == agenttypes.DecisionAsk {
		// 无 UI 可确认：宽松放行，避免阻塞无任务语义的调用。
		return agenttypes.DecisionAllow, nil
	}
	return decision, nil
}

func (r *memoryRuntime) WaitPermission(context.Context, int64) (agenttypes.PermissionDecision, error) {
	return "", agentruntime.ErrNoPermissionResolver
}

func (r *memoryRuntime) AskUser(context.Context, agenttypes.AskUserInput) (agenttypes.AskUserAnswer, error) {
	return agenttypes.AskUserAnswer{}, agenttypes.ErrNoUserInputResolver
}
