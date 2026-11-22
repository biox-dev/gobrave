package service

import (
	"context"
	"testing"

	. "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/utils"
)

// TestLiveTailKeepsOnlyUnpersistedContent 验证缓存只保留未落库内容：
// 增量累积成段，完整块一到就移除对应段。
func TestLiveTailKeepsOnlyUnpersistedContent(t *testing.T) {
	tail := newLiveTail()

	tail.Observe(1, 10, StreamEvent{Type: StreamEventText, Content: "he"})
	tail.Observe(1, 10, StreamEvent{Type: StreamEventText, Content: "llo"})
	seq := tail.Observe(1, 10, StreamEvent{Type: StreamEventReasoningDelta, Content: "think"})

	live := tail.Snapshot(1)
	if live.Seq != seq {
		t.Fatalf("seq = %d, want %d", live.Seq, seq)
	}
	if len(live.Blocks) != 2 {
		t.Fatalf("blocks = %d, want 2", len(live.Blocks))
	}
	if live.Blocks[0].Kind != KindAssistant || live.Blocks[0].Content != "hello" {
		t.Fatalf("assistant block = %+v", live.Blocks[0])
	}
	if live.Blocks[1].Kind != KindReasoning || live.Blocks[1].Content != "think" {
		t.Fatalf("reasoning block = %+v", live.Blocks[1])
	}
	if !live.Blocks[0].Streaming || live.Blocks[0].TaskID != 10 {
		t.Fatalf("block meta = %+v", live.Blocks[0])
	}
	if live.Blocks[0].ID == "" {
		t.Fatalf("block id should be stable and non-empty")
	}

	// assistant 完整块已落库 → 对应段移除；reasoning 段保留。
	tail.Observe(1, 10, StreamEvent{Type: StreamEventMessage, Data: MessageBlock{Content: "hello"}})
	live = tail.Snapshot(1)
	if len(live.Blocks) != 1 || live.Blocks[0].Kind != KindReasoning {
		t.Fatalf("after message block: %+v", live.Blocks)
	}

	// 同一段落的新增文本会新开一段（上一段已关闭）。
	tail.Observe(1, 10, StreamEvent{Type: StreamEventText, Content: "again"})
	live = tail.Snapshot(1)
	if len(live.Blocks) != 2 || live.Blocks[1].Content != "again" {
		t.Fatalf("after new delta: %+v", live.Blocks)
	}

	// done：本轮尾巴清空，但序号继续单调递增。
	before := live.Seq
	after := tail.Observe(1, 10, StreamEvent{Type: StreamEventDone})
	if after <= before {
		t.Fatalf("seq must keep increasing: %d -> %d", before, after)
	}
	if live = tail.Snapshot(1); len(live.Blocks) != 0 {
		t.Fatalf("done should clear blocks: %+v", live.Blocks)
	}
	if live.Seq != after {
		t.Fatalf("seq = %d, want %d", live.Seq, after)
	}
}

// TestLiveTailResetsOnNewTask 验证同一会话进入新一轮时丢弃上一轮残留。
func TestLiveTailResetsOnNewTask(t *testing.T) {
	tail := newLiveTail()
	tail.Observe(7, 100, StreamEvent{Type: StreamEventText, Content: "old"})

	seq := tail.Observe(7, 200, StreamEvent{Type: StreamEventText, Content: "new"})
	live := tail.Snapshot(7)
	if live.TaskID != 200 {
		t.Fatalf("task id = %d, want 200", live.TaskID)
	}
	if len(live.Blocks) != 1 || live.Blocks[0].Content != "new" {
		t.Fatalf("blocks = %+v", live.Blocks)
	}
	if live.Blocks[0].TaskID != 200 {
		t.Fatalf("block task id = %d, want 200", live.Blocks[0].TaskID)
	}
	if live.Seq != seq {
		t.Fatalf("seq = %d, want %d", live.Seq, seq)
	}
}

// TestLiveTailEndTaskClears 验证终态清理，且不影响其他会话。
func TestLiveTailEndTaskClears(t *testing.T) {
	tail := newLiveTail()
	tail.Observe(1, 10, StreamEvent{Type: StreamEventText, Content: "a"})
	tail.Observe(2, 20, StreamEvent{Type: StreamEventText, Content: "b"})

	tail.EndTask(1, 10)
	if live := tail.Snapshot(1); len(live.Blocks) != 0 {
		t.Fatalf("session 1 should be cleared: %+v", live.Blocks)
	}
	if live := tail.Snapshot(2); len(live.Blocks) != 1 {
		t.Fatalf("session 2 should be kept: %+v", live.Blocks)
	}

	// 任务 ID 不匹配时不清理（避免误清新一轮）。
	tail.Observe(1, 11, StreamEvent{Type: StreamEventText, Content: "c"})
	tail.EndTask(1, 10)
	if live := tail.Snapshot(1); len(live.Blocks) != 1 {
		t.Fatalf("mismatched task must keep blocks: %+v", live.Blocks)
	}
}

// TestPublishFeedsLiveTail 验证事件出口接线：stream 事件写入实时尾巴并分配序号，
// 任务终态清空未落库内容。
func TestPublishFeedsLiveTail(t *testing.T) {
	_ = utils.InitSnowflake(1)
	svc := NewService(ServiceConfig{})
	facts := TaskFacts{SessionID: 9, TaskID: 90, UserID: 1}
	ctx := context.Background()

	svc.publish(ctx, facts, EventStream, StreamEvent{Type: StreamEventText, Content: "hel"}, false)
	svc.publish(ctx, facts, EventStream, StreamEvent{Type: StreamEventText, Content: "lo"}, false)

	live := svc.SessionLive(9)
	if live.Seq != 2 {
		t.Fatalf("seq = %d, want 2", live.Seq)
	}
	if len(live.Blocks) != 1 || live.Blocks[0].Content != "hello" {
		t.Fatalf("blocks = %+v", live.Blocks)
	}

	svc.publish(ctx, facts, EventTaskCompleted, nil, false)
	if live = svc.SessionLive(9); len(live.Blocks) != 0 {
		t.Fatalf("terminal event should clear blocks: %+v", live.Blocks)
	}
	if live.Seq != 2 {
		t.Fatalf("seq must stay monotonic after terminal: %d", live.Seq)
	}
}
