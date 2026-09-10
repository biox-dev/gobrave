package dag

import (
	"context"
	"sync"
)

type queuedNode struct {
	analysisNodeID int64
}

type WorkerPool struct {
	dispatcher *NodeDispatcher
	jobs       chan queuedNode
	workers    int
	wg         sync.WaitGroup
}

func NewWorkerPool(dispatcher *NodeDispatcher, workers int, queueSize int) *WorkerPool {
	if workers <= 0 {
		workers = 1
	}
	if queueSize <= 0 {
		queueSize = 1
	}
	return &WorkerPool{
		dispatcher: dispatcher,
		jobs:       make(chan queuedNode, queueSize),
		workers:    workers,
	}
}

func (p *WorkerPool) Start(ctx context.Context) {
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case job, ok := <-p.jobs:
					if !ok {
						return
					}
					_ = p.dispatcher.Dispatch(ctx, job.analysisNodeID)
				}
			}
		}()
	}
}

// Enqueue is the non-blocking variant: it reports false when the queue has no room
// left, leaving the retry/backpressure policy to the caller.
func (p *WorkerPool) Enqueue(nodeID int64) bool {
	select {
	case p.jobs <- queuedNode{analysisNodeID: nodeID}:
		return true
	default:
		return false
	}
}

// EnqueueWait blocks until nodeID is queued, ctx is cancelled, or the pool stops,
// and reports whether the node made it into the queue.
//
// It must only be called from the goroutine that owns the pool lifecycle (the one
// that calls Stop). Stop closes the job channel, and a send on a closed channel
// panics in select even from the default branch, so a sender running on another
// goroutine could always race with Stop.
func (p *WorkerPool) EnqueueWait(ctx context.Context, nodeID int64) bool {
	select {
	case <-ctx.Done():
		return false
	case p.jobs <- queuedNode{analysisNodeID: nodeID}:
		return true
	}
}

func (p *WorkerPool) QueueLen() int {
	return len(p.jobs)
}

// Cap reports the pool queue capacity, used by callers as their claim-ahead and
// backpressure limit.
func (p *WorkerPool) Cap() int {
	return cap(p.jobs)
}

func (p *WorkerPool) Stop() {
	close(p.jobs)
	p.wg.Wait()
}
