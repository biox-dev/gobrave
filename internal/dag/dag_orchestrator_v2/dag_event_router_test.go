package orchestratorv2
package orchestratorv2

import (
	"sync"
	"testing"
	"time"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
)

func TestDagEventRouterRoutesByAnalysisID(t *testing.T) {
	router := newDagEventRouter()
	sinkA := router.register(1)
	sinkB := router.register(2)

	router.Handle(dagruntime.RuntimeEvent{Name: dagruntime.EventNodeCompleted, AnalysisID: 1, NodeID: "n1"})
	router.Handle(dagruntime.RuntimeEvent{Name: dagruntime.EventNodeCompleted, AnalysisID: 2, NodeID: "n2"})
	// Unknown analysis: must be dropped without panicking.
	router.Handle(dagruntime.RuntimeEvent{Name: dagruntime.EventNodeCompleted, AnalysisID: 3, NodeID: "n3"})

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

func TestDagEventRouterIgnoresNonRuntimeEvents(t *testing.T) {
	router := newDagEventRouter()
	sink := router.register(7)

	router.Handle("not-a-runtime-event")
	router.Handle(struct{ Foo string }{Foo: "bar"})

	if got := len(sink.events); got != 0 {
		t.Fatalf("sink received %d events for non-RuntimeEvent payloads, want 0", got)
	}
}

func TestDagEventRouterRegisterIsIdempotent(t *testing.T) {
	router := newDagEventRouter()
	first := router.register(42)
	second := router.register(42)

	if first != second {
		t.Fatal("register must return the existing sink for an already registered analysis")
	}
}

func TestDagEventRouterUnregisterDropsEventsWithoutPanic(t *testing.T) {
	router := newDagEventRouter()
	sink := router.register(5)

	router.unregister(5)
	// Late events (e.g. deferred container completions) must be discarded safely.
	router.Handle(dagruntime.RuntimeEvent{Name: dagruntime.EventNodeCompleted, AnalysisID: 5})

	if got := len(sink.events); got != 0 {
		t.Fatalf("retired sink received %d events, want 0", got)
	}

	// unregister must be idempotent and safe for unknown analyses.
	router.unregister(5)
	router.unregister(999)

	// Re-registering the same analysis must yield a fresh, live sink.
	fresh := router.register(5)
	if fresh == sink {
		t.Fatal("re-register after unregister must create a new sink")
	}
	router.Handle(dagruntime.RuntimeEvent{Name: dagruntime.EventNodeCompleted, AnalysisID: 5})
	if got := len(fresh.events); got != 1 {
		t.Fatalf("fresh sink event count = %d, want 1", got)
	}
}

func TestAnalysisEventSinkRetireDropsStaleWrites(t *testing.T) {
	sink := newAnalysisEventSink()
	sink.retire()

	// A stale pointer held across teardown must never write after retirement.
	sink.enqueue(dagruntime.RuntimeEvent{AnalysisID: 1})

	if got := len(sink.events); got != 0 {
		t.Fatalf("retired sink accepted %d events, want 0", got)
	}
}

func TestAnalysisEventSinkOverflowMarksDirtyAndWakes(t *testing.T) {
	router := newDagEventRouter()
	sink := router.register(11)

	for i := 0; i < dynamicV2EventBufferSize; i++ {
		router.Handle(dagruntime.RuntimeEvent{Name: dagruntime.EventNodeCompleted, AnalysisID: 11})
	}
	if sink.consumeDirty() {
		t.Fatal("sink must not be dirty while the buffer still has room")
	}

	// One more event overflows the buffer and must degrade to a full reconcile.
	router.Handle(dagruntime.RuntimeEvent{Name: dagruntime.EventNodeCompleted, AnalysisID: 11})

	if !sink.consumeDirty() {
		t.Fatal("buffer overflow must mark the sink dirty")
	}
	if sink.consumeDirty() {
		t.Fatal("dirty flag must be cleared exactly once per consume")
	}

	select {
	case <-sink.wake:
	default:
		t.Fatal("buffer overflow must signal the wake channel")
	}

	if got := len(sink.events); got != dynamicV2EventBufferSize {
		t.Fatalf("buffered event count = %d, want %d", got, dynamicV2EventBufferSize)
	}
}

func TestAnalysisEventSinkEnqueueNeverBlocks(t *testing.T) {
	sink := newAnalysisEventSink()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// Far more events than the buffer holds: the router runs on the shared bus
		// worker, so enqueue must never wait for a consumer.
		for i := 0; i < dynamicV2EventBufferSize*4; i++ {
			sink.enqueue(dagruntime.RuntimeEvent{AnalysisID: 1})
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enqueue blocked; the shared event bus could be stalled")
	}
}

// TestDagEventRouterConcurrentLifecycle exercises the register / Handle /
// unregister interleavings that happen when runs start and finish while the bus
// keeps publishing. Run with -race.
func TestDagEventRouterConcurrentLifecycle(t *testing.T) {
	router := newDagEventRouter()

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
				router.register(id)
				router.Handle(dagruntime.RuntimeEvent{
					Name:       dagruntime.EventNodeCompleted,
					AnalysisID: id,
					NodeID:     "n",
				})
				router.Handle("ignored")
				if i%3 == 0 {
					router.unregister(id)
				}
			}
		}()
	}
	wg.Wait()
}
