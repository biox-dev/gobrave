package orchestratorv2

import (
	"sync"
	"sync/atomic"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/event"
)

// dynamicV2EventBufferSize bounds the per-analysis runtime event buffer.
//
// When the buffer is exhausted the sink degrades into a "dirty" full-reconcile
// signal instead of silently dropping completion events, so scheduling
// correctness never depends on the buffer being large enough.
const dynamicV2EventBufferSize = 256

// analysisEventSink is the per-analysis runtime event mailbox owned by a running
// dynamic DAG. Its lifetime matches exactly one scheduler run: it is created when
// the run is accepted and retired when the run goroutine exits.
//
// Design notes:
//   - The events channel is never closed. Retiring is signalled through the
//     `closed` flag instead, which removes the classic "send on closed channel"
//     race between the bus worker goroutine and the scheduler teardown.
//   - enqueue() is non-blocking by contract because it runs on the shared bus
//     worker goroutine.
type analysisEventSink struct {
	// events carries runtime events in publish order for one analysis.
	events chan dagruntime.RuntimeEvent
	// wake is signalled (non-blocking, capacity 1) when events may have been lost
	// so the scheduler loop performs an immediate full reconcile.
	wake chan struct{}
	// dirty is set when an event could not be enqueued; the scheduler must fall
	// back to a full reconcile instead of trusting the delivered event payload.
	dirty atomic.Bool
	// closed marks the sink as retired. Producers must never write after this flag
	// is set.
	closed atomic.Bool
}

func newAnalysisEventSink() *analysisEventSink {
	return &analysisEventSink{
		events: make(chan dagruntime.RuntimeEvent, dynamicV2EventBufferSize),
		wake:   make(chan struct{}, 1),
	}
}

// enqueue delivers one runtime event without ever blocking the caller.
func (s *analysisEventSink) enqueue(evt dagruntime.RuntimeEvent) {
	if s == nil || s.closed.Load() {
		return
	}
	select {
	case s.events <- evt:
		return
	default:
	}

	// Buffer exhausted: mark the analysis for a full reconcile and wake the
	// scheduler loop so it reacts without waiting for the watchdog tick.
	s.dirty.Store(true)
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// consumeDirty reports whether events may have been lost, clearing the flag.
func (s *analysisEventSink) consumeDirty() bool {
	if s == nil {
		return false
	}
	return s.dirty.Swap(false)
}

// retire stops any future event delivery for this sink. It is safe to call
// concurrently with enqueue and is idempotent.
func (s *analysisEventSink) retire() {
	if s == nil {
		return
	}
	s.closed.Store(true)
}

// dagEventRouter is the single long-lived event.Bus subscriber for the whole
// process. It routes RuntimeEvent values to the sink of the owning analysis.
//
// Why a router instead of per-run subscriptions:
//  1. A per-run bus.Subscribe adds a permanent subscriber goroutine per run
//     because event.Bus has no Unsubscribe.
//  2. OrderedMemoryBus.Publish writes to every subscriber queue with a blocking
//     send, so the blocking surface grows linearly with the number of concurrent
//     runs and every subscriber is woken by every other analysis' events.
//
// With the router there is exactly one subscriber and routing is an O(1) map
// lookup.
type dagEventRouter struct {
	mu    sync.RWMutex
	sinks map[int64]*analysisEventSink
}

var _ event.Handler = (*dagEventRouter)(nil)

func newDagEventRouter() *dagEventRouter {
	return &dagEventRouter{sinks: make(map[int64]*analysisEventSink)}
}

// Handle implements event.Handler.
//
// It must stay O(1) and strictly non-blocking: a slow or wedged analysis must
// never stall the shared bus worker or any other subscriber.
func (r *dagEventRouter) Handle(evt event.Event) {
	runtimeEvt, ok := evt.(dagruntime.RuntimeEvent)
	if !ok {
		return
	}

	r.mu.RLock()
	sink := r.sinks[runtimeEvt.AnalysisID]
	r.mu.RUnlock()
	if sink == nil {
		// No run is listening (not started yet, already finished, or not a V2 run).
		return
	}
	sink.enqueue(runtimeEvt)
}

// register creates the sink for analysisID, or returns the existing one when the
// analysis is already registered.
func (r *dagEventRouter) register(analysisID int64) *analysisEventSink {
	r.mu.Lock()
	defer r.mu.Unlock()

	if sink, ok := r.sinks[analysisID]; ok {
		return sink
	}
	sink := newAnalysisEventSink()
	r.sinks[analysisID] = sink
	return sink
}

// unregister retires and removes the sink for analysisID. It is safe to call more
// than once and safe to call for an unknown analysis.
//
// The map entry is removed before retiring so a concurrent register for the same
// analysis always obtains a fresh, live sink.
func (r *dagEventRouter) unregister(analysisID int64) {
	r.mu.Lock()
	sink, ok := r.sinks[analysisID]
	if ok {
		delete(r.sinks, analysisID)
	}
	r.mu.Unlock()

	if ok {
		sink.retire()
	}
}
