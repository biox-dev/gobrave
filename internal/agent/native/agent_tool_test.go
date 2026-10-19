package native

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/llm/provider"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// fakeClient 是 provider.Client 的测试实现：按预置响应逐轮返回 complete 事件。
type fakeClient struct {
	responses   []*provider.Response
	seenMsgs    [][]provider.Message
	seenTools   [][]provider.Tool
	streamCalls int
}

func (f *fakeClient) Stream(_ context.Context, messages []provider.Message, tools []provider.Tool) <-chan provider.Event {
	f.streamCalls++
	f.seenMsgs = append(f.seenMsgs, append([]provider.Message(nil), messages...))
	f.seenTools = append(f.seenTools, append([]provider.Tool(nil), tools...))

	ch := make(chan provider.Event, 1)
	response := &provider.Response{Content: "noop"}
	if f.streamCalls <= len(f.responses) {
		response = f.responses[f.streamCalls-1]
	}
	ch <- provider.Event{Type: provider.EventComplete, Response: response}
	close(ch)
	return ch
}

// stubTool 是仅用于验证子 Agent 工具集合的最小工具。
type stubTool struct{ name string }

func (s stubTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        s.name,
		Description: "stub",
		InputSchema: tool.Schema("stub", map[string]any{}),
	}
}

func (s stubTool) Execute(context.Context, json.RawMessage) (any, error) { return "stub", nil }

func TestNewAgentToolRunsNestedAgent(t *testing.T) {
	client := &fakeClient{responses: []*provider.Response{{Content: "found config in internal/config"}}}
	created := 0

	agentTool := NewAgentTool(AgentToolConfig{
		NewClient: func(context.Context) (provider.Client, error) {
			created++
			return client, nil
		},
		Tools:        tool.NewRegistryWith(stubTool{name: "probe"}),
		SystemPrompt: "test task prompt",
	})

	out, err := agentTool.Execute(context.Background(), json.RawMessage(`{"prompt":"where is config?"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if out != "found config in internal/config" {
		t.Fatalf("Execute() = %v, want nested agent content", out)
	}
	if created != 1 {
		t.Fatalf("NewClient called %d times, want 1", created)
	}
	if client.streamCalls != 1 {
		t.Fatalf("Stream called %d times, want 1", client.streamCalls)
	}
	if got := client.seenMsgs[0]; len(got) != 1 || got[0].Role != provider.RoleUser || got[0].Content != "where is config?" {
		t.Fatalf("nested messages = %+v, want single user message", got)
	}
	// 子 Agent 只能拿到注入的只读工具集合，不能看到父 Agent 的完整工具表。
	if got := client.seenTools[0]; len(got) != 1 || got[0].Name != "probe" {
		t.Fatalf("nested tools = %+v, want only probe", got)
	}
}

func TestAgentToolRequiresPrompt(t *testing.T) {
	called := false
	agentTool := NewAgentTool(AgentToolConfig{
		NewClient: func(context.Context) (provider.Client, error) {
			called = true
			return &fakeClient{}, nil
		},
	})
	for _, args := range []string{`{}`, `{"prompt":"   "}`} {
		if _, err := agentTool.Execute(context.Background(), json.RawMessage(args)); err == nil {
			t.Fatalf("Execute(%s) expected error", args)
		}
	}
	if called {
		t.Fatal("NewClient must not be called when prompt is empty")
	}
}

func TestAgentToolRejectsEmptyResponse(t *testing.T) {
	agentTool := NewAgentTool(AgentToolConfig{
		NewClient: func(context.Context) (provider.Client, error) {
			return &fakeClient{responses: []*provider.Response{{Content: "   "}}}, nil
		},
	})
	if _, err := agentTool.Execute(context.Background(), json.RawMessage(`{"prompt":"hi"}`)); err == nil {
		t.Fatal("expected error for empty nested response")
	}
}

func TestTaskAgentToolsAreReadOnly(t *testing.T) {
	names := make(map[string]bool)
	for _, item := range TaskAgentTools() {
		names[item.Definition().Name] = true
	}
	for _, want := range []string{"glob", "grep", "ls", "view", "sourcegraph"} {
		if !names[want] {
			t.Fatalf("TaskAgentTools() missing %q: %v", want, names)
		}
	}
	for _, forbidden := range []string{"bash", "edit", "write", "patch", AgentToolName} {
		if names[forbidden] {
			t.Fatalf("TaskAgentTools() must not expose %q", forbidden)
		}
	}
}
