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
	EventStream             AgentEventType = "stream"              // 透传的流式事件（Payload 为 StreamEvent）
)

// AgentEvent 是发布到 EventBus 的单个事件，带单调递增的 sequence。
//
// sequence 使事件可被“增量拉取”（如 GET /tasks/{id}/events?after=3），
// 因此浏览器刷新后无需依赖 WS 历史即可恢复。
type AgentEvent struct {
	ID        string         `json:"id"`
	TaskID    string         `json:"task_id"`
	Sequence  int64          `json:"sequence"`
	Type      AgentEventType `json:"type"`
	Payload   any            `json:"payload,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// EventHandler 是事件订阅回调。
// 返回 error 表示处理失败；EventBus 目前采用同步分发，回调应避免阻塞。
type EventHandler func(ctx context.Context, event AgentEvent) error

// EventBus 是实时通知层接口：只负责“告诉 UI 状态发生了变化”，不承担状态存储。
//
// 数据库（Repository）才是状态源；EventBus 之上可以接 WS / SSE 做实时推送。
type EventBus interface {
	// Publish 发布一个事件给订阅了该任务（或全部任务）的订阅者。
	Publish(ctx context.Context, event AgentEvent)
	// Subscribe 订阅某任务的事件；taskID 为空表示订阅全部任务。
	// 返回的 unsubscribe 用于取消订阅。
	Subscribe(taskID string, handler EventHandler) (unsubscribe func())
}

// memoryEventBus 是 EventBus 的内存实现（同步分发）。
type memoryEventBus struct {
	mu     sync.RWMutex
	nextID uint64
	subs   map[uint64]subscription
	byTask map[string]map[uint64]struct{} // taskID → 订阅 ID 集合
}

type subscription struct {
	taskID  string
	handler EventHandler
}

// NewEventBus 创建一个内存事件总线。
func NewEventBus() EventBus {
	return &memoryEventBus{
		subs:   make(map[uint64]subscription),
		byTask: make(map[string]map[uint64]struct{}),
	}
}

func (b *memoryEventBus) Publish(ctx context.Context, event AgentEvent) {
	b.mu.RLock()
	handlers := make([]EventHandler, 0, len(b.subs))
	for _, sub := range b.subs {
		if sub.taskID == "" || sub.taskID == event.TaskID {
			handlers = append(handlers, sub.handler)
		}
	}
	b.mu.RUnlock()

	for _, h := range handlers {
		_ = h(ctx, event)
	}
}

func (b *memoryEventBus) Subscribe(taskID string, handler EventHandler) func() {
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
