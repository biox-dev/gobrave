package dag

import (
	"strings"
	"sync"
	"sync/atomic"

	"github.com/biox-dev/gobrave/internal/event"
)

// eventSinkBufferSize bounds the per-analysis runtime event buffer.
//
// When the buffer is exhausted the sink degrades into a "dirty" full-reconcile
// signal instead of silently dropping completion events, so scheduling
// correctness never depends on the buffer being large enough.
const eventSinkBufferSize = 256

// EventSinkFilter decides whether a runtime event is worth delivering to a sink.
//
// It runs on the shared bus worker goroutine, so it must stay cheap and must
// never block.
type EventSinkFilter func(RuntimeEvent) bool

// SchedulerEventFilter is the delivery whitelist for dynamic scheduler sinks.
//
// Only node terminal transitions can change dependency state, so only they can
// produce new schedulable work. Everything else the bus carries for the same
// analysis - node.submitted (the scheduler's own echo from pumpReadyQueue),
// node.running, node.state_changed, and the dag.* lifecycle events the scheduler
// publishes to itself - would only wake the loop for a no-op iteration that
// re-reads the runtime snapshot. Dropping them before the buffer also keeps the
// 256-entry window free for the events that actually matter.
//
// This only narrows what a sink receives; the bus is untouched, so other
// subscribers (for example the realtime notifier) still observe every event.
func SchedulerEventFilter(evt RuntimeEvent) bool {
	switch strings.TrimSpace(evt.Name) {
	case EventNodeCompleted, EventNodeFailed:
		return true
	default:
		return false
	}
}

// AnalysisEventSink is the per-analysis runtime event mailbox owned by one
// running scheduler.
//
// Its lifetime matches exactly one scheduler run: EventRouter.Register creates it
// when a run is accepted and EventRouter.Unregister retires it when the run
// goroutine exits.
//
// Design notes:
//   - The events channel is never closed. Retiring is signalled through the
//     `closed` flag instead, which removes the classic "send on closed channel"
//     race between the bus worker goroutine and the scheduler teardown.
//   - Enqueue is non-blocking by contract because it runs on the shared bus
//     worker goroutine.
type AnalysisEventSink struct {
	// events carries runtime events in publish order for one analysis.
	events chan RuntimeEvent
	// wake is signalled (non-blocking, capacity 1) when events may have been lost
	// so the scheduler loop performs an immediate full reconcile.
	wake chan struct{}
	// dirty is set when an event could not be enqueued; the scheduler must fall
	// back to a full reconcile instead of trusting the delivered event payload.
	dirty atomic.Bool
	// closed marks the sink as retired. Producers must never write after this flag
	// is set.
	closed atomic.Bool
	// keep is the optional delivery whitelist. It is set once at construction and
	// never mutated, so the bus goroutine can read it without synchronization.
	// A nil keep accepts every event.
	keep EventSinkFilter
}

// NewAnalysisEventSink builds a detached sink that accepts every runtime event.
//
// Schedulers normally obtain their sink from EventRouter.Register. This
// constructor exists for tests and for schedulers that run without a registered
// router.
func NewAnalysisEventSink() *AnalysisEventSink {
	return newAnalysisEventSink(nil)
}

// newAnalysisEventSink is the single constructor, so the keep predicate can never
// be silently omitted by one of the entrypoints above.
func newAnalysisEventSink(keep EventSinkFilter) *AnalysisEventSink {
	return &AnalysisEventSink{
		events: make(chan RuntimeEvent, eventSinkBufferSize),
		wake:   make(chan struct{}, 1),
		keep:   keep,
	}
}

// Events exposes the receive side of the event mailbox.
//
// A nil sink returns a nil channel so a select arm on it blocks forever instead
// of panicking; callers are expected to have substituted a live sink.
func (s *AnalysisEventSink) Events() <-chan RuntimeEvent {
	if s == nil {
		return nil
	}
	return s.events
}

// Wake exposes the overflow wake signal. It is closed-never and only ever carries
// a single pending notification.
func (s *AnalysisEventSink) Wake() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.wake
}

// Enqueue delivers one runtime event without ever blocking the caller.
//
// Events rejected by the sink's keep predicate are dropped before they can take a
// buffer slot, so they neither displace a real event nor trip the overflow path.
func (s *AnalysisEventSink) Enqueue(evt RuntimeEvent) {
	if s == nil || s.closed.Load() {
		return
	}
	if s.keep != nil && !s.keep(evt) {
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

// ConsumeDirty reports whether events may have been lost, clearing the flag.
func (s *AnalysisEventSink) ConsumeDirty() bool {
	if s == nil {
		return false
	}
	return s.dirty.Swap(false)
}

// retire stops any future event delivery for this sink. It is safe to call
// concurrently with Enqueue and is idempotent.
func (s *AnalysisEventSink) retire() {
	if s == nil {
		return
	}
	s.closed.Store(true)
}

// EventRouter is the single long-lived event.Bus subscriber for the whole
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
//
// The instance is created once by the DI container (internal/container) and
// injected into every scheduler that needs runtime events.
type EventRouter struct {
	mu    sync.RWMutex
	sinks map[int64]*AnalysisEventSink
}

var _ event.Handler = (*EventRouter)(nil)

// NewEventRouter builds an empty router. The caller is responsible for
// subscribing it to the event bus exactly once.
func NewEventRouter() *EventRouter {
	return &EventRouter{sinks: make(map[int64]*AnalysisEventSink)}
}

// Handle implements event.Handler.
//
// It must stay O(1) and strictly non-blocking: a slow or wedged analysis must
// never stall the shared bus worker or any other subscriber.
func (r *EventRouter) Handle(evt event.Event) {
	if r == nil {
		return
	}
	runtimeEvt, ok := evt.(RuntimeEvent)
	if !ok {
		return
	}

	r.mu.RLock()
	sink := r.sinks[runtimeEvt.AnalysisID]
	r.mu.RUnlock()
	if sink == nil {
		// No run is listening (not started yet, already finished, or not a
		// dynamically scheduled run).
		return
	}
	sink.Enqueue(runtimeEvt)
}

// Register creates the sink for analysisID, or returns the existing one when the
// analysis is already registered. The sink accepts every runtime event.
func (r *EventRouter) Register(analysisID int64) *AnalysisEventSink {
	return r.RegisterWithFilter(analysisID, nil)
}

// RegisterWithFilter is Register plus a delivery filter: the sink only ever
// receives events for which keep returns true. A nil keep accepts everything,
// which is exactly Register's behaviour.
//
// Filtering happens before the buffer, so filtered-out events never occupy a slot
// and the scheduler loop is never woken for them. The bus itself is not touched,
// so other subscribers still see every event.
func (r *EventRouter) RegisterWithFilter(analysisID int64, keep EventSinkFilter) *AnalysisEventSink {
	if r == nil {
		// Defensive: without a router the caller still gets a usable sink, it just
		// never receives events (same contract as an unregistered analysis).
		return newAnalysisEventSink(keep)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if sink, ok := r.sinks[analysisID]; ok {
		return sink
	}
	sink := newAnalysisEventSink(keep)
	r.sinks[analysisID] = sink
	return sink
}

// Unregister retires and removes the sink for analysisID. It is safe to call more
// than once and safe to call for an unknown analysis.
//
// The map entry is removed before retiring so a concurrent Register for the same
// analysis always obtains a fresh, live sink.
func (r *EventRouter) Unregister(analysisID int64) {
	if r == nil {
		return
	}

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
