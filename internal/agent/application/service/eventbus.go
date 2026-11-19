package service

import (
	"context"
	"sync"

	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// memoryEventBus 是 EventBus 的内存实现（同步分发）。
//
// 总线只做「进程内广播」，不承担路由：任务 / 会话 / 用户的范围过滤由订阅方
// 按 AgentEvent 上的字段自行完成（会话级路由见 handler.sessionEventBridge）。
type memoryEventBus struct {
	mu     sync.RWMutex
	nextID uint64
	subs   map[uint64]EventHandler
}

// NewEventBus 创建一个内存事件总线。
func NewEventBus() EventBus {
	return &memoryEventBus{subs: make(map[uint64]EventHandler)}
}

func (b *memoryEventBus) Publish(ctx context.Context, event AgentEvent) {
	b.mu.RLock()
	handlers := make([]EventHandler, 0, len(b.subs))
	for _, h := range b.subs {
		handlers = append(handlers, h)
	}
	b.mu.RUnlock()

	for _, h := range handlers {
		_ = h(ctx, event)
	}
}

func (b *memoryEventBus) Subscribe(handler EventHandler) func() {
	if handler == nil {
		return func() {}
	}

	b.mu.Lock()
	b.nextID++
	id := b.nextID
	b.subs[id] = handler
	b.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			delete(b.subs, id)
		})
	}
}
