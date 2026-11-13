package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agentruntime "github.com/biox-dev/gobrave/internal/agent/runtime"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// stubAskUserRuntime 只实现 AskUser，其余方法由内嵌 nil 接口占位。
type stubAskUserRuntime struct {
	agentinterfaces.Runtime
	got agenttypes.AskUserInput
}

func (s *stubAskUserRuntime) AskUser(_ context.Context, in agenttypes.AskUserInput) (agenttypes.AskUserAnswer, error) {
	s.got = in
	return agenttypes.AskUserAnswer{Answers: []agenttypes.QuestionAnswer{{ID: "q1", Value: "A"}}}, nil
}

func TestAskUserToolUsesRuntimeFromContext(t *testing.T) {
	exec := tool.NewExecutor(tool.NewRegistryWith(AskUserTool()))
	args, err := json.Marshal(agenttypes.AskUserInput{
		Title: "Pick one",
		Questions: []agenttypes.Question{{
			ID:    "q1",
			Type:  agenttypes.QuestionTypeSingleChoice,
			Title: "Which one?",
		}},
	})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	// ctx 缺 Runtime：转为失败结果（不断言 panic），提示配置问题。
	missing := exec.Execute(context.Background(), tool.Call{Name: "ask_user", Arguments: args})
	if !missing.IsError {
		t.Fatalf("expected error result without runtime, got %q", missing.Content)
	}

	// ctx 注入 Runtime：调用 Runtime.AskUser 并把答案格式化回填。
	rt := &stubAskUserRuntime{}
	ctx := agentruntime.WithToolRuntime(context.Background(), rt)
	res := exec.Execute(ctx, tool.Call{Name: "ask_user", Arguments: args})
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	if rt.got.Title != "Pick one" {
		t.Fatalf("runtime input title = %q, want %q", rt.got.Title, "Pick one")
	}
	if !strings.Contains(res.Content, "Which one?") || !strings.Contains(res.Content, "A") {
		t.Fatalf("content = %q, want question + answer", res.Content)
	}
}
