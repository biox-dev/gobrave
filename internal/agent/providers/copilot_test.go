package providers

import (
	"context"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	copilot "github.com/github/copilot-sdk/go"
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

func TestBuildTools(t *testing.T) {
	reg := tool.NewRegistryWith(newEchoTool())
	a := &copilotAgent{opts: agent.Options{Tools: reg}}

	tools := a.buildTools(nil)
	if len(tools) != 1 {
		t.Fatalf("tools = %d, want 1", len(tools))
	}
	ct := tools[0]
	if ct.Name != "echo" {
		t.Fatalf("tool name = %q, want echo", ct.Name)
	}
	if ct.Description == "" {
		t.Fatalf("tool description is empty")
	}
	if ct.Handler == nil {
		t.Fatalf("tool handler is nil")
	}

	res, err := ct.Handler(copilot.ToolInvocation{
		ToolCallID:   "call_1",
		ToolName:     "echo",
		Arguments:    map[string]any{"text": "hi"},
		TraceContext: context.Background(),
	})
	if err != nil {
		t.Fatalf("handler error: %v", err)
	}
	if res.ResultType != "success" {
		t.Fatalf("result type = %q, want success", res.ResultType)
	}
	if res.TextResultForLLM != "echo:hi" {
		t.Fatalf("result content = %q, want echo:hi", res.TextResultForLLM)
	}
}

func TestBuildToolsNilRegistry(t *testing.T) {
	a := &copilotAgent{opts: agent.Options{}}
	if tools := a.buildTools(nil); tools != nil {
		t.Fatalf("tools = %v, want nil", tools)
	}
}

func TestToCopilotToolResult(t *testing.T) {
	ok := toCopilotToolResult(tool.Success("done"))
	if ok.ResultType != "success" || ok.TextResultForLLM != "done" || ok.Error != "" {
		t.Fatalf("success result = %+v", ok)
	}

	fail := toCopilotToolResult(tool.Failure(agent.ErrPermissionDenied))
	if fail.ResultType != "failure" || fail.TextResultForLLM != agent.ErrPermissionDenied.Error() {
		t.Fatalf("failure result = %+v", fail)
	}
}
