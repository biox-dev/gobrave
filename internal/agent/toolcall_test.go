package agent_test

import (
	"context"
	"errors"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

func newEchoTool() tool.Tool {
	type echoIn struct {
		Text string `json:"text"`
	}
	return tool.NewFunc("echo", "echo text", tool.Schema("", map[string]any{
		"text": tool.StringProperty("text to echo"),
	}, "text"), func(_ context.Context, in echoIn) (string, error) {
		return "echo:" + in.Text, nil
	})
}

// collectRuntime 捕获 Emit 的事件，其余方法按默认行为。
type collectRuntime struct {
	events []agent.StreamEvent
}

func (r *collectRuntime) Emit(_ context.Context, ev agent.StreamEvent) error {
	r.events = append(r.events, ev)
	return nil
}

func (r *collectRuntime) RequestPermission(_ context.Context, _ agent.Operation) (agent.PermissionDecision, error) {
	return agent.DecisionAllow, nil
}

func (r *collectRuntime) WaitPermission(context.Context, int64) (agent.PermissionDecision, error) {
	return agent.DecisionAllow, nil
}

// denyRuntime 始终拒绝权限。
type denyRuntime struct{}

func (denyRuntime) Emit(context.Context, agent.StreamEvent) error { return nil }
func (denyRuntime) RequestPermission(context.Context, agent.Operation) (agent.PermissionDecision, error) {
	return agent.DecisionDeny, nil
}
func (denyRuntime) WaitPermission(context.Context, int64) (agent.PermissionDecision, error) {
	return agent.DecisionDeny, nil
}

func TestToolRunnerEmitsEvents(t *testing.T) {
	exec := tool.NewExecutor(tool.NewRegistryWith(newEchoTool()))
	rt := &collectRuntime{}
	runner := agent.NewToolRunner(exec, rt)

	res := runner.Run(context.Background(), agent.ToolCall{
		ID:        "c1",
		Name:      "echo",
		Arguments: map[string]any{"text": "hi"},
	})
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}

	if len(rt.events) != 2 {
		t.Fatalf("events = %d, want 2", len(rt.events))
	}
	if rt.events[0].Type != agent.StreamEventToolCall {
		t.Fatalf("first event = %s, want tool_call", rt.events[0].Type)
	}
	if rt.events[1].Type != agent.StreamEventToolResult {
		t.Fatalf("second event = %s, want tool_result", rt.events[1].Type)
	}
	tr, ok := rt.events[1].Data.(agent.ToolResultEvent)
	if !ok {
		t.Fatalf("tool_result data type = %T", rt.events[1].Data)
	}
	if tr.CallID != "c1" || tr.IsError {
		t.Fatalf("unexpected tool_result = %+v", tr)
	}
}

func TestToolRunnerPermissionDeny(t *testing.T) {
	exec := tool.NewExecutor(tool.NewRegistryWith(newEchoTool()))
	rt := denyRuntime{}
	runner := agent.NewToolRunner(exec, rt).SetPermissionResolver(func(_ context.Context, _ agent.ToolCall) (agent.Operation, bool) {
		return agent.Operation{Type: agent.OperationWrite}, true
	})

	res := runner.Run(context.Background(), agent.ToolCall{ID: "c1", Name: "echo"})
	if !res.IsError {
		t.Fatalf("expected denied result")
	}
	if !errors.Is(res.Err, agent.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", res.Err)
	}
}

func TestToolLoop(t *testing.T) {
	exec := tool.NewExecutor(tool.NewRegistryWith(newEchoTool()))
	runner := agent.NewToolRunner(exec, &collectRuntime{})
	loop := agent.NewToolLoop(runner)

	// Provider 提供两批工具调用，第二批结束后返回空（结束循环）。
	batches := [][]agent.ToolCall{
		{{ID: "c1", Name: "echo", Arguments: map[string]any{"text": "a"}}},
		{{ID: "c2", Name: "echo", Arguments: map[string]any{"text": "b"}}},
	}
	i := 0
	results, err := loop.Run(context.Background(), func(_ context.Context, _ []tool.Result) ([]agent.ToolCall, error) {
		if i >= len(batches) {
			return nil, nil
		}
		b := batches[i]
		i++
		return b, nil
	})
	if err != nil {
		t.Fatalf("loop error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if results[0].Content != "echo:a" || results[1].Content != "echo:b" {
		t.Fatalf("results = %v", results)
	}
}
