package agent

import (
	"context"
	"encoding/json"

	"github.com/biox-dev/gobrave/internal/agent/skill"
)

// 本文件把 skill 包（技能定义 / 注册 / 执行）桥接到 Agent 的运行时：
//
//   - SkillRunner：执行一次技能调用，并对外输出 skill_call / skill_result 事件
//     （skill_call 为完整块事件，会落库；skill_result 作为结果回传）。
//   - SkillLoop：驱动标准的 skill-call 循环（执行 → 回传结果 → 再执行）。
//
// 它与 toolcall.go 的 ToolRunner / ToolLoop 平行：技能是比工具更高一层的可复用
// 能力单元（额外携带指令正文），但执行、事件、权限门禁的桥接方式保持一致。
// 具体 Agent / Provider 只负责"从模型拿到技能调用并最终产出文本"，技能的执行
// 与事件、权限门禁统一收敛到此处，避免每个 Provider 重复实现。

// SkillCall 是一次完整的技能调用（对应模型发起的 skill 调用）。
type SkillCall struct {
	ID        string `json:"id"`   // skillCallId
	Name      string `json:"name"` // 技能名
	Arguments any    `json:"arguments,omitempty"`
}

// SkillResultEvent 是 skill_result 事件的结构化载荷。
type SkillResultEvent struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

// SkillPermissionResolver 把一次技能调用映射为需要授权的 Operation。
//
// 返回 (op, true) 表示该技能需要权限确认（执行前阻塞等待决策）；
// 返回 (op, false) 表示直接放行。
type SkillPermissionResolver func(ctx context.Context, call SkillCall) (Operation, bool)

// SkillRunner 把 skill.Invoker 与 Runtime 桥接：执行技能调用并输出事件，
// 同时承载可选的权限门禁（把技能调用映射为 Operation，执行前请求授权）。
type SkillRunner struct {
	inv      *skill.Invoker
	rt       Runtime
	resolver SkillPermissionResolver
	userID   string
}

// NewSkillRunner 创建技能执行器；inv 为 nil 时使用空注册表的执行器。
func NewSkillRunner(inv *skill.Invoker, rt Runtime) *SkillRunner {
	if inv == nil {
		inv = skill.NewInvoker(skill.NewRegistry())
	}
	return &SkillRunner{inv: inv, rt: rt}
}

// SetPermissionResolver 设置权限映射（可选），返回自身便于链式调用。
func (r *SkillRunner) SetPermissionResolver(resolver SkillPermissionResolver) *SkillRunner {
	r.resolver = resolver
	return r
}

// SetUserID 设置权限决策所使用的用户标识（用于按用户读取许可策略）。
func (r *SkillRunner) SetUserID(userID string) *SkillRunner {
	r.userID = userID
	return r
}

// Run 执行一次技能调用：
//
//  1. 输出 skill_call 事件（完整块，落库）；
//  2. 若配置了权限映射，执行前通过 Runtime.RequestPermission 阻塞等待决策；
//  3. 执行技能；
//  4. 输出 skill_result 事件并返回结果。
func (r *SkillRunner) Run(ctx context.Context, call SkillCall) skill.Result {
	if r.rt != nil {
		_ = r.rt.Emit(ctx, StreamEvent{Type: StreamEventSkillCall, Data: call})
	}

	// 权限门禁（可选）。
	if r.resolver != nil {
		if op, need := r.resolver(ctx, call); need {
			res := r.requestPermission(ctx, op)
			if res.IsError {
				r.emitResult(ctx, call, res)
				return res
			}
		}
	}

	res := r.inv.Invoke(ctx, skill.Call{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: marshalSkillArgs(call.Arguments),
	})
	r.emitResult(ctx, call, res)
	return res
}

// RunAll 顺序执行一批技能调用。
func (r *SkillRunner) RunAll(ctx context.Context, calls []SkillCall) []skill.Result {
	results := make([]skill.Result, 0, len(calls))
	for _, c := range calls {
		results = append(results, r.Run(ctx, c))
	}
	return results
}

// requestPermission 通过 Runtime 请求权限；deny 或出错时返回失败结果。
func (r *SkillRunner) requestPermission(ctx context.Context, op Operation) skill.Result {
	if r.rt == nil {
		return skill.Failure(ErrNoPermissionResolver)
	}
	decision, err := r.rt.RequestPermission(ctx, r.userID, op)
	if err != nil {
		return skill.Failure(err)
	}
	if decision == DecisionDeny {
		return skill.Failure(ErrPermissionDenied)
	}
	return skill.Success(nil)
}

// emitResult 输出 skill_result 事件。
func (r *SkillRunner) emitResult(ctx context.Context, call SkillCall, res skill.Result) {
	if r.rt == nil {
		return
	}
	_ = r.rt.Emit(ctx, StreamEvent{Type: StreamEventSkillResult, Data: SkillResultEvent{
		CallID:  call.ID,
		Name:    call.Name,
		Content: res.Content,
		IsError: res.IsError,
	}})
}

// SkillCallProvider 由 Provider 提供：根据上一轮结果决定下一批待执行的技能调用。
// 返回空切片表示不再有技能调用，循环结束。
type SkillCallProvider func(ctx context.Context, results []skill.Result) ([]SkillCall, error)

// SkillLoop 驱动标准的 skill-call 循环：执行 → 回传结果 → 再执行，直至无更多技能调用。
//
// 用法与 ToolLoop 对称：
//
//	resp := model(messages, skills)
//	for resp.hasSkillCalls() {
//	    results := loop.Run(ctx, func(ctx, last) ([]agent.SkillCall, error) {
//	        messages = appendSkillResults(messages, last)
//	        resp = model(messages, skills)
//	        return resp.skillCalls(), nil
//	    })
//	}
type SkillLoop struct {
	runner *SkillRunner
}

// NewSkillLoop 创建技能调用循环。
func NewSkillLoop(runner *SkillRunner) *SkillLoop {
	return &SkillLoop{runner: runner}
}

// Run 驱动循环并返回累积的全部技能结果。
func (l *SkillLoop) Run(ctx context.Context, next SkillCallProvider) ([]skill.Result, error) {
	var all []skill.Result
	var last []skill.Result
	for {
		calls, err := next(ctx, last)
		if err != nil {
			return all, err
		}
		if len(calls) == 0 {
			return all, nil
		}
		last = l.runner.RunAll(ctx, calls)
		all = append(all, last...)
	}
}

// marshalSkillArgs 把 SkillCall.Arguments(any) 序列化为 json.RawMessage。
func marshalSkillArgs(args any) json.RawMessage {
	if args == nil {
		return nil
	}
	switch v := args.(type) {
	case json.RawMessage:
		return v
	case []byte:
		return json.RawMessage(v)
	case string:
		return json.RawMessage(v)
	default:
		b, err := json.Marshal(args)
		if err != nil {
			return nil
		}
		return b
	}
}
