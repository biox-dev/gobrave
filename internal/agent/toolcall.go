package agent

import (
	"context"
	"encoding/json"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// 本文件把 tool 包（工具定义 / 注册 / 执行）桥接到 Agent 的运行时：
//
//   - ToolRunner：执行一次工具调用，并对外输出 tool_call / tool_result 事件
//     （tool_call 为完整块事件，会落库；tool_result 作为结果回传）。
//   - ToolLoop：驱动标准的 tool-call 循环（执行 → 回传结果 → 再执行）。
//
// 具体 Agent / Provider 只负责"从模型拿到工具调用并最终产出文本"，工具的执行
// 与事件、权限门禁统一收敛到此处，避免每个 Provider 重复实现。

// ToolRunner 把 tool.Executor 与 Runtime 桥接：执行工具调用并输出事件，
// 同时承载可选的权限门禁（把工具调用映射为 Operation，执行前请求授权）。
type ToolRunner struct {
	exec     *tool.Executor
	rt       Runtime
	resolver ToolPermissionResolver
}

// ToolPermissionResolver 把一次工具调用映射为需要授权的 Operation。
//
// 返回 (op, true) 表示该工具需要权限确认（执行前阻塞等待决策）；
// 返回 (op, false) 表示直接放行。
type ToolPermissionResolver func(ctx context.Context, call ToolCall) (Operation, bool)

// NewToolRunner 创建工具执行器；exec 为 nil 时使用空注册表的执行器。
func NewToolRunner(exec *tool.Executor, rt Runtime) *ToolRunner {
	if exec == nil {
		exec = tool.NewExecutor(tool.NewRegistry())
	}
	return &ToolRunner{exec: exec, rt: rt}
}

// SetPermissionResolver 设置权限映射（可选），返回自身便于链式调用。
func (r *ToolRunner) SetPermissionResolver(resolver ToolPermissionResolver) *ToolRunner {
	r.resolver = resolver
	return r
}

// Run 执行一次工具调用：
//
//  1. 输出 tool_call 事件（完整块，落库）；
//  2. 若配置了权限映射，执行前通过 Runtime.RequestPermission 阻塞等待决策；
//  3. 执行工具；
//  4. 输出 tool_result 事件并返回结果。
func (r *ToolRunner) Run(ctx context.Context, call ToolCall) tool.Result {
	if r.rt != nil {
		_ = r.rt.Emit(ctx, StreamEvent{Type: StreamEventToolCall, Data: call})
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

	res := r.exec.Execute(ctx, tool.Call{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: marshalToolArgs(call.Arguments),
	})
	r.emitResult(ctx, call, res)
	return res
}

// RunAll 顺序执行一批工具调用。
func (r *ToolRunner) RunAll(ctx context.Context, calls []ToolCall) []tool.Result {
	results := make([]tool.Result, 0, len(calls))
	for _, c := range calls {
		results = append(results, r.Run(ctx, c))
	}
	return results
}

// requestPermission 通过 Runtime 请求权限；deny 或出错时返回失败结果。
func (r *ToolRunner) requestPermission(ctx context.Context, op Operation) tool.Result {
	if r.rt == nil {
		return tool.Failure(ErrNoPermissionResolver)
	}
	decision, err := r.rt.RequestPermission(ctx, op)
	if err != nil {
		return tool.Failure(err)
	}
	if decision == DecisionDeny {
		return tool.Failure(ErrPermissionDenied)
	}
	return tool.Success(nil)
}

// emitResult 输出 tool_result 事件。
func (r *ToolRunner) emitResult(ctx context.Context, call ToolCall, res tool.Result) {
	if r.rt == nil {
		return
	}
	_ = r.rt.Emit(ctx, StreamEvent{Type: StreamEventToolResult, Data: ToolResultEvent{
		CallID:  call.ID,
		Name:    call.Name,
		Content: res.Content,
		IsError: res.IsError,
	}})
}

// ToolResultEvent 是 tool_result 事件的结构化载荷。
type ToolResultEvent struct {
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	IsError bool   `json:"is_error"`
}

// ToolCallProvider 由 Provider 提供：根据上一轮结果决定下一批待执行的工具调用。
// 返回空切片表示不再有工具调用，循环结束。
type ToolCallProvider func(ctx context.Context, results []tool.Result) ([]ToolCall, error)

// ToolLoop 驱动标准的 tool-call 循环：执行 → 回传结果 → 再执行，直至无更多工具调用。
//
// Provider 侧通常这样配合：
//
//	resp := model(messages, tools)
//	for resp.hasToolCalls() {
//	    results := loop.Run(ctx, func(ctx, last) ([]agent.ToolCall, error) {
//	        messages = appendToolResults(messages, last) // 把结果回填
//	        resp = model(messages, tools)
//	        return resp.toolCalls(), nil
//	    })
//	}
type ToolLoop struct {
	runner *ToolRunner
}

// NewToolLoop 创建工具调用循环。
func NewToolLoop(runner *ToolRunner) *ToolLoop {
	return &ToolLoop{runner: runner}
}

// Run 驱动循环并返回累积的全部工具结果。
func (l *ToolLoop) Run(ctx context.Context, next ToolCallProvider) ([]tool.Result, error) {
	var all []tool.Result
	var last []tool.Result
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

// marshalToolArgs 把 ToolCall.Arguments(any) 序列化为 json.RawMessage。
func marshalToolArgs(args any) json.RawMessage {
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
