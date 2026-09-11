package dag

import (
	"sync"
	"testing"
	"time"
)

func TestEventRouterRoutesByAnalysisID(t *testing.T) {
	router := NewEventRouter()
	sinkA := router.Register(1)
	sinkB := router.Register(2)

	router.Handle(RuntimeEvent{Name: EventNodeCompleted, AnalysisID: 1, NodeID: "n1"})
	router.Handle(RuntimeEvent{Name: EventNodeCompleted, AnalysisID: 2, NodeID: "n2"})
	// Unknown analysis: must be dropped without panicking.
	router.Handle(RuntimeEvent{Name: EventNodeCompleted, AnalysisID: 3, NodeID: "n3"})

	if got, want := len(sinkA.events), 1; got != want {
		t.Fatalf("sink A event count = %d, want %d", got, want)
	}
	if got, want := len(sinkB.events), 1; got != want {
		t.Fatalf("sink B event count = %d, want %d", got, want)
	}

	evt := <-sinkA.events
	if evt.NodeID != "n1" || evt.AnalysisID != 1 {
		t.Fatalf("sink A received %+v, want analysis 1 / node n1", evt)
	}
}

func TestEventRouterIgnoresNonRuntimeEvents(t *testing.T) {
	router := NewEventRouter()
	sink := router.Register(7)

	router.Handle("not-a-runtime-event")
	router.Handle(struct{ Foo string }{Foo: "bar"})

	if got := len(sink.events); got != 0 {
		t.Fatalf("sink received %d events for non-RuntimeEvent payloads, want 0", got)
	}
}

func TestEventRouterRegisterIsIdempotent(t *testing.T) {
	router := NewEventRouter()
	first := router.Register(42)
	second := router.Register(42)

	if first != second {
		t.Fatal("register must return the existing sink for an already registered analysis")
	}
}

func TestEventRouterUnregisterDropsEventsWithoutPanic(t *testing.T) {
	router := NewEventRouter()
	sink := router.Register(5)

	router.Unregister(5)
	// Late events (e.g. deferred container completions) must be discarded safely.
	router.Handle(RuntimeEvent{Name: EventNodeCompleted, AnalysisID: 5})

	if got := len(sink.events); got != 0 {
		t.Fatalf("retired sink received %d events, want 0", got)
	}

	// unregister must be idempotent and safe for unknown analyses.
	router.Unregister(5)
	router.Unregister(999)

	// Re-registering the same analysis must yield a fresh, live sink.
	fresh := router.Register(5)
	if fresh == sink {
		t.Fatal("re-register after unregister must create a new sink")
	}
	router.Handle(RuntimeEvent{Name: EventNodeCompleted, AnalysisID: 5})
	if got := len(fresh.events); got != 1 {
		t.Fatalf("fresh sink event count = %d, want 1", got)
	}
}

func TestAnalysisEventSinkRetireDropsStaleWrites(t *testing.T) {
	sink := NewAnalysisEventSink()
	sink.retire()

	// A stale pointer held across teardown must never write after retirement.
	sink.Enqueue(RuntimeEvent{AnalysisID: 1})

	if got := len(sink.events); got != 0 {
		t.Fatalf("retired sink accepted %d events, want 0", got)
	}
}

func TestAnalysisEventSinkOverflowMarksDirtyAndWakes(t *testing.T) {
	router := NewEventRouter()
	sink := router.Register(11)

	for i := 0; i < eventSinkBufferSize; i++ {
		router.Handle(RuntimeEvent{Name: EventNodeCompleted, AnalysisID: 11})
	}
	if sink.ConsumeDirty() {
		t.Fatal("sink must not be dirty while the buffer still has room")
	}

	// One more event overflows the buffer and must degrade to a full reconcile.
	router.Handle(RuntimeEvent{Name: EventNodeCompleted, AnalysisID: 11})

	if !sink.ConsumeDirty() {
		t.Fatal("buffer overflow must mark the sink dirty")
	}
	if sink.ConsumeDirty() {
		t.Fatal("dirty flag must be cleared exactly once per consume")
	}

	select {
	case <-sink.Wake():
	default:
		t.Fatal("buffer overflow must signal the wake channel")
	}

	if got := len(sink.events); got != eventSinkBufferSize {
		t.Fatalf("buffered event count = %d, want %d", got, eventSinkBufferSize)
	}
}

func TestAnalysisEventSinkEnqueueNeverBlocks(t *testing.T) {
	sink := NewAnalysisEventSink()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more events than the buffer holds: the router runs on the shared bus
		// worker, so enqueue must never wait for a consumer.
		for i := 0; i < eventSinkBufferSize*4; i++ {
			sink.Enqueue(RuntimeEvent{AnalysisID: 1})
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueue blocked; the shared event bus could be stalled")
	}
}

// TestEventRouterConcurrentLifecycle exercises the register / Handle / unregister
// interleavings that happen when runs start and finish while the bus keeps
// publishing. Run with -race.
func TestEventRouterConcurrentLifecycle(t *testing.T) {
	router := NewEventRouter()

	const (
		workers = 8
		iters   = 300
		keys    = 4
	)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				id := int64(i % keys)
				router.Register(id)
				router.Handle(RuntimeEvent{
					Name:       EventNodeCompleted,
					AnalysisID: id,
					NodeID:     "n",
				})
				router.Handle("ignored")
				if i%3 == 0 {
					router.Unregister(id)
				}
			}
		}()
	}
	wg.Wait()
}

// TestAnalysisEventSinkFilterDropsNoiseBeforeBuffer pins the property the
// scheduler relies on: filtered-out events never reach the buffer, so even a
// flood of them cannot overflow it, mark it dirty, or wake the loop.
func TestAnalysisEventSinkFilterDropsNoiseBeforeBuffer(t *testing.T) {
	router := NewEventRouter()
	sink := router.RegisterWithFilter(21, SchedulerEventFilter)

	// Far more noise than the buffer holds.
	for i := 0; i < eventSinkBufferSize*4; i++ {
		router.Handle(RuntimeEvent{Name: EventNodeRunning, AnalysisID: 21})
		router.Handle(RuntimeEvent{Name: EventNodeSubmitted, AnalysisID: 21})
		router.Handle(RuntimeEvent{Name: EventDagStarted, AnalysisID: 21})
		router.Handle(RuntimeEvent{Name: EventNodeStateChange, AnalysisID: 21})
	}

	if got := len(sink.events); got != 0 {
		t.Fatalf("filtered sink buffered %d noise events, want 0", got)
	}
	if sink.ConsumeDirty() {
		t.Fatal("noise must not overflow the buffer and mark the sink dirty")
	}
	select {
	case <-sink.Wake():
		t.Fatal("noise must not wake the scheduler loop")
	default:
	}

	// Terminal transitions still pass through, in publish order.
	router.Handle(RuntimeEvent{Name: EventNodeCompleted, AnalysisID: 21, NodeID: "n1"})
	router.Handle(RuntimeEvent{Name: EventNodeFailed, AnalysisID: 21, NodeID: "n2"})

	if got, want := len(sink.events), 2; got != want {
		t.Fatalf("filtered sink buffered %d events, want %d", got, want)
	}
	if evt := <-sink.events; evt.NodeID != "n1" || evt.Name != EventNodeCompleted {
		t.Fatalf("first delivered event = %+v, want completed/n1", evt)
	}
	if evt := <-sink.events; evt.NodeID != "n2" || evt.Name != EventNodeFailed {
		t.Fatalf("second delivered event = %+v, want failed/n2", evt)
	}
}

// TestEventRouterRegisterAcceptsEveryEvent guards the unfiltered default: a plain
// Register must stay permissive so the filter is strictly opt-in.
func TestEventRouterRegisterAcceptsEveryEvent(t *testing.T) {
	router := NewEventRouter()
	sink := router.Register(22)

	router.Handle(RuntimeEvent{Name: EventNodeRunning, AnalysisID: 22, NodeID: "n"})

	if got, want := len(sink.events), 1; got != want {
		t.Fatalf("unfiltered sink buffered %d events, want %d", got, want)
	}
}
