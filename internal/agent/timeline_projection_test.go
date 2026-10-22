package agent

import (
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/message"
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

	params, ok := ProjectTimelineEvent(11, 22, event)
	if !ok {
		t.Fatalf("assistant message block should be projected")
	}
	if params.SessionID != 11 || params.TaskID != 22 {
		t.Fatalf("owner = (%d,%d), want (11,22)", params.SessionID, params.TaskID)
	}
	if params.Role != message.RoleAssistant || params.Kind != message.KindAssistant {
		t.Fatalf("role/kind = %s/%s, want %s/%s",
			params.Role, params.Kind, message.RoleAssistant, message.KindAssistant)
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
	if _, ok := ProjectTimelineEvent(1, 2, StreamEvent{
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
		if _, ok := ProjectTimelineEvent(1, 2, StreamEvent{Type: typ}); ok {
			t.Fatalf("%s should not be projected", typ)
		}
	}
}
