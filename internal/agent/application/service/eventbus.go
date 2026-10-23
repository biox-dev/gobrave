package service

import (
	"context"
	"sync"

	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

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
