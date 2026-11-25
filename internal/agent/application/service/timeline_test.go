package service

import (
	"testing"

	. "github.com/biox-dev/gobrave/internal/agent/types"
)

// TestProjectTimelineEventMessageBlock 验证 assistant 文本块也会在轮内被投影为消息，
// 并保留事件原始 Data（后续恢复可据此重建结构化历史）。
func TestProjectTimelineEventMessageBlock(t *testing.T) {
	event := StreamEvent{
		Type: StreamEventMessage,
		Data: MessageBlock{
			ID:        "m1",
			Content:   "done",
			ToolCalls: []ToolCall{{ID: "t1", Name: "echo"}},
		},
	}

	params, ok := ProjectTimelineEvent(11, 22, 33, event)
	if !ok {
		t.Fatalf("assistant message block should be projected")
	}
	if params.SessionID != 11 || params.TaskID != 22 {
		t.Fatalf("owner = (%d,%d), want (11,22)", params.SessionID, params.TaskID)
	}
	if params.UserID != 33 {
		t.Fatalf("user id = %d, want 33", params.UserID)
	}
	if params.Role != RoleAssistant || params.Kind != KindAssistant {
		t.Fatalf("role/kind = %s/%s, want %s/%s",
			params.Role, params.Kind, RoleAssistant, KindAssistant)
	}
	if params.Content != "done" {
		t.Fatalf("content = %q, want done", params.Content)
	}
	if params.Data == nil {
		t.Fatalf("data should keep the original event payload")
	}
}

// TestProjectTimelineEventSkipsEmptyMessageBlock 验证「只发起工具调用、正文为空」的块
// 不产生时间线消息，避免历史里出现空气泡。
func TestProjectTimelineEventSkipsEmptyMessageBlock(t *testing.T) {
	if _, ok := ProjectTimelineEvent(1, 2, 3, StreamEvent{
		Type: StreamEventMessage,
		Data: MessageBlock{ID: "m1", ToolCalls: []ToolCall{{ID: "t1", Name: "echo"}}},
	}); ok {
		t.Fatalf("empty assistant message should not be projected")
	}
}

// TestProjectTimelineEventIgnoresLifecycleEvents 验证增量与生命周期事件不产生时间线消息。
func TestProjectTimelineEventIgnoresLifecycleEvents(t *testing.T) {
	for _, typ := range []StreamEventType{
		StreamEventText, StreamEventReasoningDelta,
		StreamEventTurnStart, StreamEventTurnEnd,
		StreamEventDone, StreamEventError,
	} {
		if _, ok := ProjectTimelineEvent(1, 2, 3, StreamEvent{Type: typ}); ok {
			t.Fatalf("%s should not be projected", typ)
		}
	}
}

// TestProjectTimelineEventCarriesRoundIdentity 验证工具调用 / 结果 / 思考块都带上
// 轮次与调用标识：这是「任务中断后按轮重组、只重放未完成调用」的全部依据。
func TestProjectTimelineEventCarriesRoundIdentity(t *testing.T) {
	cases := []struct {
		name       string
		event      StreamEvent
		wantKind   string
		wantTurnID string
		wantCallID string
	}{
		{
			name: "reasoning",
			event: StreamEvent{Type: StreamEventReasoning, Data: ReasoningBlock{
				ID: "r1", TurnID: "turn-1", Content: "thinking",
			}},
			wantKind:   KindReasoning,
			wantTurnID: "turn-1",
		},
		{
			name: "message",
			event: StreamEvent{Type: StreamEventMessage, Data: MessageBlock{
				ID: "m1", TurnID: "turn-1", Content: "hello",
			}},
			wantKind:   KindAssistant,
			wantTurnID: "turn-1",
		},
		{
			name: "tool_call",
			event: StreamEvent{Type: StreamEventToolCall, Data: ToolCall{
				ID: "call-1", TurnID: "turn-1", Name: "echo", Arguments: `{}`,
			}},
			wantKind:   KindToolCall,
			wantTurnID: "turn-1",
			wantCallID: "call-1",
		},
		{
			name: "tool_result",
			event: StreamEvent{Type: StreamEventToolResult, Data: ToolResultBlock{
				ToolCallID: "call-1", TurnID: "turn-1", Content: "ok",
			}},
			wantKind:   KindToolResult,
			wantTurnID: "turn-1",
			wantCallID: "call-1",
		},
	}

	for _, tc := range cases {
		params, ok := ProjectTimelineEvent(1, 2, 3, tc.event)
		if !ok {
			t.Fatalf("%s: should be projected", tc.name)
		}
		if params.Kind != tc.wantKind || params.TurnID != tc.wantTurnID || params.ToolCallID != tc.wantCallID {
			t.Fatalf("%s: kind/turn/call = %s/%q/%q, want %s/%q/%q", tc.name,
				params.Kind, params.TurnID, params.ToolCallID, tc.wantKind, tc.wantTurnID, tc.wantCallID)
		}
	}
}
