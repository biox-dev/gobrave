package handler

import (
	"context"
	"testing"
	"time"

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
	if len(b.byUser) != 0 {
		t.Fatalf("byUser index not cleaned up: %v", b.byUser)
	}
}

// TestSessionEventBridgeKeepsSubscriptionAcrossTurns 验证本轮任务结束不会踢掉订阅者：
// 停留在会话页的用户应该继续收到该会话后续每一轮的实时流。
func TestSessionEventBridgeKeepsSubscriptionAcrossTurns(t *testing.T) {
	b := newSessionEventBridge(nil)
	ctx := context.Background()

	b.subscribe(10, 1)
	for _, typ := range []agenttypes.AgentEventType{
		agenttypes.EventTaskStarted,
		agenttypes.EventTaskWaiting,
		agenttypes.EventTaskCompleted,
		agenttypes.EventTaskFailed,
		agenttypes.EventTaskCanceled,
	} {
		b.dispatch(ctx, agenttypes.AgentEvent{SessionID: 10, Type: typ})
	}
	if got := len(b.subscribers(10)); got != 1 {
		t.Fatalf("subscription dropped on task end, subscribers = %d", got)
	}
}

// TestSessionEventBridgeOfflineGrace 验证离线宽限期的两个方向：
// 宽限期内重连保留订阅；超时未重连则清理该用户的全部会话订阅。
func TestSessionEventBridgeOfflineGrace(t *testing.T) {
	b := newSessionEventBridge(nil)
	b.grace = 20 * time.Millisecond
	b.subscribe(10, 1)
	b.subscribe(11, 1)

	// 刷新 / 重连：连接短暂归零后又恢复，不应清理。
	b.setOnline(1, false)
	b.setOnline(1, true)
	time.Sleep(60 * time.Millisecond)
	if got := len(b.subscribers(10)); got != 1 {
		t.Fatalf("reconnected user lost subscription, subscribers = %d", got)
	}

	// 真正离开：宽限期后按用户一次性清理全部会话订阅。
	b.setOnline(1, false)
	time.Sleep(80 * time.Millisecond)
	if got := len(b.subscribers(10)) + len(b.subscribers(11)); got != 0 {
		t.Fatalf("offline user subscriptions not cleaned, remaining = %d", got)
	}
	if len(b.byUser) != 0 {
		t.Fatalf("byUser index not cleaned up: %v", b.byUser)
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
