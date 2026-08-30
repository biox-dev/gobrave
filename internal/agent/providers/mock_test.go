package providers_test

import (
	"context"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent"
	agentproviders "github.com/biox-dev/gobrave/internal/agent/providers"
	"github.com/biox-dev/gobrave/internal/agent/skill"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

func resolveMock(t *testing.T, opts agent.Options) agent.Agent {
	t.Helper()
	reg := agent.NewRegistry(agentproviders.NewMock())
	a, err := reg.Resolve(agent.ProviderMock, opts)
	if err != nil {
		t.Fatalf("resolve mock: %v", err)
	}
	return a
}

func TestMockToolCallDefault(t *testing.T) {
	a := resolveMock(t, agent.Options{Model: "demo"})

	var events []agent.StreamEvent
	rt := agent.NewStandaloneRuntime(func(_ context.Context, ev agent.StreamEvent) error {
		events = append(events, ev)
		return nil
	})

	req := agent.Request{
		Env:      map[string]string{"MOCK_TOOL_DEMO": "true"},
		Messages: []agent.Message{{Role: agent.RoleUser, Content: "hello"}},
	}

	res, err := a.Stream(context.Background(), req, rt)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	// 应输出 tool_call 与 tool_result 两个事件。
	var sawCall, sawResult bool
	for _, ev := range events {
		switch ev.Type {
		case agent.StreamEventToolCall:
			sawCall = true
		case agent.StreamEventToolResult:
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
	a := resolveMock(t, agent.Options{Model: "demo", Tools: reg})

	var events []agent.StreamEvent
	rt := agent.NewStandaloneRuntime(func(_ context.Context, ev agent.StreamEvent) error {
		events = append(events, ev)
		return nil
	})

	req := agent.Request{
		Env: map[string]string{
			"MOCK_TOOL_DEMO": "true",
			"MOCK_TOOL_NAME": "greet",
		},
		Messages: []agent.Message{{Role: agent.RoleUser, Content: "bob"}},
	}

	res, err := a.Stream(context.Background(), req, rt)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	if !strings.Contains(res.Content, "[tool=greet] hi bob") {
		t.Fatalf("content missing custom tool result: %q", res.Content)
	}
}

func TestMockSkillCallDefault(t *testing.T) {
	a := resolveMock(t, agent.Options{Model: "demo"})

	var events []agent.StreamEvent
	rt := agent.NewStandaloneRuntime(func(_ context.Context, ev agent.StreamEvent) error {
		events = append(events, ev)
		return nil
	})

	req := agent.Request{
		Env:      map[string]string{"MOCK_SKILL_DEMO": "true"},
		Messages: []agent.Message{{Role: agent.RoleUser, Content: "hello"}},
	}

	res, err := a.Stream(context.Background(), req, rt)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	// 应输出 skill_call 与 skill_result 两个事件。
	var sawCall, sawResult bool
	for _, ev := range events {
		switch ev.Type {
		case agent.StreamEventSkillCall:
			sawCall = true
		case agent.StreamEventSkillResult:
			sawResult = true
		}
	}
	if !sawCall || !sawResult {
		t.Fatalf("missing skill events: call=%v result=%v (events=%d)", sawCall, sawResult, len(events))
	}

	// 最终内容应包含默认 echo 技能的调用结果。
	if !strings.Contains(res.Content, "[skill=echo]") {
		t.Fatalf("content missing skill marker: %q", res.Content)
	}
}

func TestMockSkillCallCustomSkill(t *testing.T) {
	// 注入自定义技能注册表，并指定技能名。
	reg := skill.NewRegistryWith(
		skill.NewFunc(skill.Manifest{
			Definition: skill.Definition{
				Name:        "greet",
				Description: "greet a name",
				InputSchema: skill.Schema("", map[string]any{
					"text": skill.StringProperty("name to greet"),
				}, "text"),
			},
			Version:      "1.0.0",
			Instructions: "greet a name.",
		}, func(_ context.Context, in struct {
			Text string `json:"text"`
		}) (string, error) {
			return "hi " + in.Text, nil
		}),
	)
	a := resolveMock(t, agent.Options{Model: "demo", Skills: reg})

	var events []agent.StreamEvent
	rt := agent.NewStandaloneRuntime(func(_ context.Context, ev agent.StreamEvent) error {
		events = append(events, ev)
		return nil
	})

	req := agent.Request{
		Env: map[string]string{
			"MOCK_SKILL_DEMO": "true",
			"MOCK_SKILL_NAME": "greet",
		},
		Messages: []agent.Message{{Role: agent.RoleUser, Content: "bob"}},
	}

	res, err := a.Stream(context.Background(), req, rt)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	if !strings.Contains(res.Content, "[skill=greet] hi bob") {
		t.Fatalf("content missing custom skill result: %q", res.Content)
	}
}
