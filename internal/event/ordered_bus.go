package event

import "sync"

const defaultOrderedHandlerQueueSize = 256

type orderedHandlerWorker struct {
	handler Handler
	queue   chan Event
}

// OrderedMemoryBus delivers events in publish order per handler.
// Each handler owns one buffered queue and one worker goroutine.
type OrderedMemoryBus struct {
	mu        sync.RWMutex
	workers   []*orderedHandlerWorker
	queueSize int
}

func NewOrderedMemoryBus() *OrderedMemoryBus {
	return &OrderedMemoryBus{queueSize: defaultOrderedHandlerQueueSize}
}

func (b *OrderedMemoryBus) Publish(event Event) {
	b.mu.RLock()
	workers := make([]*orderedHandlerWorker, len(b.workers))
	copy(workers, b.workers)
	b.mu.RUnlock()

	for _, worker := range workers {
		worker.queue <- event
	}
}

func (b *OrderedMemoryBus) Subscribe(h Handler) {
	if h == nil {
		return
	}

	worker := &orderedHandlerWorker{
		handler: h,
		queue:   make(chan Event, b.queueSize),
	}

	b.mu.Lock()
	b.workers = append(b.workers, worker)
	b.mu.Unlock()

	go func() {
		for evt := range worker.queue {
			worker.handler.Handle(evt)
		}
	}()
}
