package agent

import (
	"context"
	"sync"
	"time"
)

// AgentEventType 枚举任务生命周期与实时通知事件。
//
// 流式内容事件统一使用 EventStream，具体细分见 StreamEvent.Type；
// 权限与任务状态变化使用独立类型，便于前端按类型订阅与恢复。
type AgentEventType string

const (
	EventTaskCreated        AgentEventType = "task.created"        // 任务已创建
	EventTaskStarted        AgentEventType = "task.started"        // 任务开始执行
	EventTaskWaiting        AgentEventType = "task.waiting"        // 任务进入等待权限状态
	EventTaskCompleted      AgentEventType = "task.completed"      // 任务完成
	EventTaskFailed         AgentEventType = "task.failed"         // 任务失败
	EventTaskCanceled       AgentEventType = "task.canceled"       // 任务取消
	EventPermissionCreated  AgentEventType = "permission.created"  // 新增待确认权限
	EventPermissionResolved AgentEventType = "permission.resolved" // 权限已被批准 / 拒绝
	EventQuestionCreated    AgentEventType = "question.created"    // 新增待作答提问（Payload 为 QuestionRequest）
	EventQuestionResolved   AgentEventType = "question.resolved"   // 提问已作答 / 取消（Payload 为 QuestionRequest）
	EventMemorySaved        AgentEventType = "memory.saved"        // 记忆已保存（Payload 为 Memory）
	EventMemoryDeleted      AgentEventType = "memory.deleted"      // 记忆已删除（Payload 为 Memory）
	EventStream             AgentEventType = "stream"              // 透传的流式事件（Payload 为 StreamEvent）
)

// AgentEvent 是发布到 EventBus 的实时通知事件。
//
// 它只承担实时推送职责（WS / SSE），**不落库**：任务时间线的持久化由
// TimelineRecorder 投影到 agent_messages（见 timeline.go）。
// 前端刷新 / 恢复历史请读会话消息，而不是事件流。
type AgentEvent struct {
	ID        int64          `json:"id,string"`
	TaskID    int64          `json:"task_id,string"`
	Type      AgentEventType `json:"type"`
	Payload   any            `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// EventHandler 是事件订阅回调。
// 返回 error 表示处理失败；EventBus 目前采用同步分发，回调应避免阻塞。
type EventHandler func(ctx context.Context, event AgentEvent) error

// EventBus 是实时通知层接口：只负责“告诉 UI 状态发生了变化”，不承担状态存储。
//
// 状态源是 agent_tasks（任务）与 agent_messages（时间线）；EventBus 之上接
// WS / SSE 做实时推送。
type EventBus interface {
	// Publish 发布一个事件给订阅了该任务（或全部任务）的订阅者。
	Publish(ctx context.Context, event AgentEvent)
	// Subscribe 订阅某任务的事件；taskID 为空表示订阅全部任务。
	// 返回的 unsubscribe 用于取消订阅。
	Subscribe(taskID int64, handler EventHandler) (unsubscribe func())
}

// memoryEventBus 是 EventBus 的内存实现（同步分发）。
type memoryEventBus struct {
	mu     sync.RWMutex
	nextID uint64
	subs   map[uint64]subscription
	byTask map[int64]map[uint64]struct{} // taskID → 订阅 ID 集合
}

type subscription struct {
	taskID  int64
	handler EventHandler
}

// NewEventBus 创建一个内存事件总线。
func NewEventBus() EventBus {
	return &memoryEventBus{
		subs:   make(map[uint64]subscription),
		byTask: make(map[int64]map[uint64]struct{}),
	}
}

func (b *memoryEventBus) Publish(ctx context.Context, event AgentEvent) {
	b.mu.RLock()
	handlers := make([]EventHandler, 0, len(b.subs))
	for _, sub := range b.subs {
		if sub.taskID == 0 || sub.taskID == event.TaskID {
			handlers = append(handlers, sub.handler)
		}
	}
	b.mu.RUnlock()

	for _, h := range handlers {
		_ = h(ctx, event)
	}
}

func (b *memoryEventBus) Subscribe(taskID int64, handler EventHandler) func() {
	if handler == nil {
		return func() {}
	}

	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.subs[id] = subscription{taskID: taskID, handler: handler}
	if b.byTask[taskID] == nil {
		b.byTask[taskID] = make(map[uint64]struct{})
	}
	b.byTask[taskID][id] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			delete(b.subs, id)
			if set, ok := b.byTask[taskID]; ok {
				delete(set, id)
				if len(set) == 0 {
					delete(b.byTask, taskID)
				}
			}
		})
	}
}
