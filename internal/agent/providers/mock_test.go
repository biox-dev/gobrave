package providers_test

import (
	"context"
	"strings"
	"testing"

	agentclient "github.com/biox-dev/gobrave/internal/agent/client"
	agentproviders "github.com/biox-dev/gobrave/internal/agent/providers"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// recordingRuntime 是测试用的最小 Runtime：事件追加到 events，权限一律放行。
type recordingRuntime struct {
	events *[]agenttypes.StreamEvent
}

func (r recordingRuntime) Facts() agenttypes.TaskFacts { return agenttypes.TaskFacts{} }

func (r recordingRuntime) Emit(_ context.Context, ev agenttypes.StreamEvent) error {
	*r.events = append(*r.events, ev)
	return nil
}

func (r recordingRuntime) RequestPermission(context.Context, agenttypes.Operation) (agenttypes.PermissionDecision, error) {
	return agenttypes.DecisionAllow, nil
}

func (r recordingRuntime) WaitPermission(context.Context, int64) (agenttypes.PermissionDecision, error) {
	return agenttypes.DecisionAllow, nil
}

func (r recordingRuntime) AskUser(context.Context, agenttypes.AskUserInput) (agenttypes.AskUserAnswer, error) {
	return agenttypes.AskUserAnswer{}, agenttypes.ErrNoUserInputResolver
}

func resolveMock(t *testing.T, opts agentclient.Options) agentinterfaces.Agent {
	t.Helper()
	reg := agentclient.NewRegistry(agentproviders.NewMock())
	a, err := reg.Resolve(agenttypes.ProviderMock, opts)
	if err != nil {
		t.Fatalf("resolve mock: %v", err)
	}
	return a
}

func TestMockToolCallDefault(t *testing.T) {
	a := resolveMock(t, agentclient.Options{})

	var events []agenttypes.StreamEvent
	rt := recordingRuntime{events: &events}

	req := agenttypes.Request{
		Env:      map[string]string{"MOCK_TOOL_DEMO": "true"},
		Messages: []agenttypes.Message{{Role: agenttypes.RoleUser, Content: "hello"}},
	}

	res, err := a.Stream(context.Background(), req, rt)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	// 应输出 tool_call 与 tool_result 两个事件。
	var sawCall, sawResult bool
	for _, ev := range events {
		switch ev.Type {
		case agenttypes.StreamEventToolCall:
			sawCall = true
		case agenttypes.StreamEventToolResult:
			sawResult = true
		}
	}
	if !sawCall || !sawResult {
		t.Fatalf("missing tool events: call=%v result=%v (events=%d)", sawCall, sawResult, len(events))
	}

	// 最终内容应包含默认 echo 工具的调用结果。
	if !strings.Contains(res.Content, "[tool=echo]") {
		t.Fatalf("content missing tool marker: %q", res.Content)
	}
}

func TestMockToolCallCustomTool(t *testing.T) {
	// 注入自定义工具注册表，并指定工具名。
	reg := tool.NewRegistryWith(
		tool.NewFunc("greet", "greet a name", tool.Schema("", map[string]any{
			"text": tool.StringProperty("name to greet"),
		}, "text"), func(_ context.Context, in struct {
			Text string `json:"text"`
		}) (string, error) {
			return "hi " + in.Text, nil
		}),
	)
	a := resolveMock(t, agentclient.Options{Tools: reg})

	var events []agenttypes.StreamEvent
	rt := recordingRuntime{events: &events}

	req := agenttypes.Request{
		Env: map[string]string{
			"MOCK_TOOL_DEMO": "true",
			"MOCK_TOOL_NAME": "greet",
		},
		Messages: []agenttypes.Message{{Role: agenttypes.RoleUser, Content: "bob"}},
	}

	res, err := a.Stream(context.Background(), req, rt)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	if !strings.Contains(res.Content, "[tool=greet] hi bob") {
		t.Fatalf("content missing custom tool result: %q", res.Content)
	}
}
