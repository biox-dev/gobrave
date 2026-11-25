package native

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// replayRuntime 是恢复测试用的 Runtime：提供固定的任务消息并记录输出事件。
type replayRuntime struct {
	testRuntime
	msgs   []agenttypes.Message
	events []agenttypes.StreamEvent
}

func (r *replayRuntime) TaskMessages(context.Context) ([]agenttypes.Message, error) {
	return r.msgs, nil
}

func (r *replayRuntime) Emit(_ context.Context, event agenttypes.StreamEvent) error {
	r.events = append(r.events, event)
	return nil
}

func (r *replayRuntime) emittedToolResults() []agenttypes.ToolResultBlock {
	out := make([]agenttypes.ToolResultBlock, 0, len(r.events))
	for _, e := range r.events {
		if e.Type != agenttypes.StreamEventToolResult {
			continue
		}
		if res, ok := agenttypes.ToolResultFromData(e.Data); ok {
			out = append(out, res)
		}
	}
	return out
}

// echoTool 是恢复测试用的工具：返回 "ran:<参数>"，并记录被执行的调用 ID。
type echoTool struct{ calls *[]string }

func (echoTool) Definition() tool.Definition {
	return tool.Definition{Name: "echo", InputSchema: map[string]any{"type": "object"}}
}

func (e echoTool) Execute(_ context.Context, args json.RawMessage) (any, error) {
	*e.calls = append(*e.calls, string(args))
	return "ran:" + string(args), nil
}

func newReplayExecutor(calls *[]string) *tool.Executor {
	return tool.NewExecutor(tool.NewRegistryWith(echoTool{calls: calls}))
}

func replayRequest() agenttypes.Request {
	return agenttypes.Request{Messages: []agenttypes.Message{{Role: agenttypes.RoleUser, Content: "hello"}}}
}

func toolCallMsg(turnID, callID, args string) agenttypes.Message {
	return agenttypes.Message{
		Kind:       agenttypes.KindToolCall,
		TurnID:     turnID,
		ToolCallID: callID,
		Data:       agenttypes.ToolCall{ID: callID, TurnID: turnID, Name: "echo", Arguments: args},
	}
}

func toolResultMsg(turnID, callID, content string) agenttypes.Message {
	return agenttypes.Message{
		Kind:       agenttypes.KindToolResult,
		TurnID:     turnID,
		ToolCallID: callID,
		Data:       agenttypes.ToolResultBlock{ToolCallID: callID, TurnID: turnID, Content: content},
	}
}

// TestRestoreReplaysOnlyPendingCalls 验证恢复时只重放没有结果的工具调用。
func TestRestoreReplaysOnlyPendingCalls(t *testing.T) {
	var ran []string
	rt := &replayRuntime{msgs: []agenttypes.Message{
		{Kind: agenttypes.KindAssistant, TurnID: "t1", Content: "thinking"},
		toolCallMsg("t1", "c1", `{"a":1}`),
		toolResultMsg("t1", "c1", "done-1"),
		toolCallMsg("t1", "c2", `{"a":2}`),
	}}

	history, final, ok := (&Config{}).restore(context.Background(), newReplayExecutor(&ran), rt, replayRequest())
	if !ok || final != "" {
		t.Fatalf("restore = ok:%v final:%q, want ok/empty", ok, final)
	}
	if len(ran) != 1 || ran[0] != `{"a":2}` {
		t.Fatalf("executed = %v, want only the pending call", ran)
	}
	if len(history) != 3 {
		t.Fatalf("history len = %d, want user + assistant + tool", len(history))
	}

	assistant := history[1]
	if assistant.Content != "thinking" || len(assistant.ToolCalls) != 2 {
		t.Fatalf("assistant = %+v, want text + 2 calls", assistant)
	}
	results := history[2].ToolResults
	if len(results) != 2 {
		t.Fatalf("results len = %d, want 2", len(results))
	}
	// 已完成的结果必须原样复用，不能重新执行。
	if results[0].ToolCallID != "c1" || results[0].Content != "done-1" {
		t.Fatalf("results[0] = %+v, want reused c1", results[0])
	}
	if results[1].ToolCallID != "c2" || !strings.Contains(results[1].Content, `"a":2`) {
		t.Fatalf("results[1] = %+v, want executed c2", results[1])
	}

	// 只有重放的调用会产生新的 tool_result 事件；已完成的不会重复落库。
	emitted := rt.emittedToolResults()
	if len(emitted) != 1 || emitted[0].ToolCallID != "c2" || emitted[0].TurnID != "t1" {
		t.Fatalf("emitted = %+v, want one result for c2 with turn id", emitted)
	}
}

// TestRestoreCompletedRoundContinues 验证已完整走完的轮次直接并入历史。
func TestRestoreCompletedRoundContinues(t *testing.T) {
	var ran []string
	rt := &replayRuntime{msgs: []agenttypes.Message{
		toolCallMsg("t1", "c1", `{}`),
		toolResultMsg("t1", "c1", "done-1"),
	}}

	history, final, ok := (&Config{}).restore(context.Background(), newReplayExecutor(&ran), rt, replayRequest())
	if !ok || final != "" {
		t.Fatalf("restore = ok:%v final:%q, want ok/empty", ok, final)
	}
	if len(ran) != 0 {
		t.Fatalf("executed = %v, want none", ran)
	}
	if len(history) != 3 || len(history[2].ToolResults) != 1 {
		t.Fatalf("history = %+v, want completed round appended", history)
	}
}

// TestRestoreReturnsExistingFinalAnswer 验证模型已产出最终回答时直接复用，避免重复生成。
func TestRestoreReturnsExistingFinalAnswer(t *testing.T) {
	var ran []string
	rt := &replayRuntime{msgs: []agenttypes.Message{
		toolCallMsg("t1", "c1", `{}`),
		toolResultMsg("t1", "c1", "done-1"),
		{Kind: agenttypes.KindAssistant, Content: "final answer"},
	}}

	history, final, ok := (&Config{}).restore(context.Background(), newReplayExecutor(&ran), rt, replayRequest())
	if !ok {
		t.Fatal("restore should be resumable")
	}
	if final != "final answer" {
		t.Fatalf("final = %q, want existing answer", final)
	}
	if len(ran) != 0 {
		t.Fatalf("executed = %v, want none", ran)
	}
	// 历史里仍然保留完整对话，供上层（如记忆提取）使用。
	if len(history) != 4 {
		t.Fatalf("history len = %d, want user + assistant + tool + final", len(history))
	}
}

// TestRestoreWithoutMessages 验证没有事实时不接管历史（回退到请求自带历史）。
func TestRestoreWithoutMessages(t *testing.T) {
	var ran []string
	rt := &replayRuntime{msgs: []agenttypes.Message{
		{Kind: agenttypes.KindUser, Content: "hello"},
	}}

	if _, final, ok := (&Config{}).restore(context.Background(), newReplayExecutor(&ran), rt, replayRequest()); ok || final != "" {
		t.Fatalf("restore = ok:%v final:%q, want not resumable", ok, final)
	}
}

// TestGroupRoundsMergesAssistantTextIntoItsTurn 验证同一轮的多段 assistant 文本合并。
func TestGroupRoundsMergesAssistantTextIntoItsTurn(t *testing.T) {
	rounds := groupRounds([]agenttypes.Message{
		{Kind: agenttypes.KindAssistant, TurnID: "t1", Content: "a"},
		toolCallMsg("t1", "c1", `{}`),
		{Kind: agenttypes.KindAssistant, TurnID: "t1", Content: "b"},
		{Kind: agenttypes.KindReasoning, TurnID: "t1", Content: "ignored"},
	})

	if len(rounds) != 1 {
		t.Fatalf("rounds = %d, want 1", len(rounds))
	}
	if rounds[0].Content != "a\nb" {
		t.Fatalf("content = %q, want merged", rounds[0].Content)
	}
	if len(rounds[0].Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(rounds[0].Calls))
	}
}
