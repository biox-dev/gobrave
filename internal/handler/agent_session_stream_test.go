package handler

import (
	"context"
	"testing"

	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// TestSessionEventBridgeSubscribe 覆盖订阅登记 / 撤销的幂等与清理语义。
func TestSessionEventBridgeSubscribe(t *testing.T) {
	b := newSessionEventBridge(nil)

	b.subscribe(10, 1)
	b.subscribe(10, 1) // 幂等
	b.subscribe(10, 2)
	if got := len(b.subscribers(10)); got != 2 {
		t.Fatalf("subscribers = %d, want 2", got)
	}

	// 只移除指定用户；会话下还有订阅者时记录保留。
	b.unsubscribe(10, 1)
	if got := len(b.subscribers(10)); got != 1 {
		t.Fatalf("subscribers after unsubscribe = %d, want 1", got)
	}

	// 最后一个用户退订后整条会话记录删除。
	b.unsubscribe(10, 2)
	if got := len(b.subscribers(10)); got != 0 {
		t.Fatalf("subscribers after all unsubscribe = %d, want 0", got)
	}
	if len(b.subs) != 0 {
		t.Fatalf("session entry not cleaned up: %v", b.subs)
	}
}

// TestSessionEventBridgeDispatchTerminalDropsSubscriptions 验证任务终态会清理会话订阅，
// 而非终态事件不会。
func TestSessionEventBridgeDispatchTerminalDropsSubscriptions(t *testing.T) {
	b := newSessionEventBridge(nil)
	ctx := context.Background()

	b.subscribe(10, 1)
	b.dispatch(ctx, agenttypes.AgentEvent{SessionID: 10, Type: agenttypes.EventStream})
	if got := len(b.subscribers(10)); got != 1 {
		t.Fatalf("non-terminal event dropped subscription, subscribers = %d", got)
	}

	b.dispatch(ctx, agenttypes.AgentEvent{SessionID: 10, Type: agenttypes.EventTaskCompleted})
	if got := len(b.subscribers(10)); got != 0 {
		t.Fatalf("terminal event did not drop subscription, subscribers = %d", got)
	}
}

// TestSessionEventBridgeIgnoresSessionlessEvents 验证没有会话的事件不进入会话路由。
func TestSessionEventBridgeIgnoresSessionlessEvents(t *testing.T) {
	b := newSessionEventBridge(nil)
	b.dispatch(context.Background(), agenttypes.AgentEvent{UserID: 1, Type: agenttypes.EventTaskCompleted})
	if len(b.subs) != 0 {
		t.Fatalf("unexpected subscription entries: %v", b.subs)
	}
}
