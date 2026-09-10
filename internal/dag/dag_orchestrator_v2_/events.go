package orchestratorv2

import (
	"sync"
	"time"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/event"
)

// EventBridge fans runtime events out to the currently active runs.
//
// The bus has no unsubscribe capability, so subscribing once per run would leak
// one handler (and one goroutine) per submission. Instead the bridge registers
// itself exactly once, lazily, and routes events to per-run buffered channels.
type EventBridge struct {
	bus event.Bus

	mu         sync.Mutex
	subs       map[int64]map[int]*runSubscription
	nextID     int
	registered bool
}

type runSubscription struct {
	events chan dagruntime.RuntimeEvent
}

// NewEventBridge creates a bridge over the given bus. A nil bus disables the
// bridge (events are simply never delivered).
func NewEventBridge(bus event.Bus) *EventBridge {
	return &EventBridge{bus: bus, subs: make(map[int64]map[int]*runSubscription)}
}

// Subscribe returns a channel receiving runtime events for analysisID plus the
// function that must be called when the run ends.
func (b *EventBridge) Subscribe(analysisID int64, buffer int) (<-chan dagruntime.RuntimeEvent, func()) {
	if buffer <= 0 {
		buffer = defaultEventBuffer
	}
	subscription := &runSubscription{events: make(chan dagruntime.RuntimeEvent, buffer)}

	b.mu.Lock()
	if b.subs[analysisID] == nil {
		b.subs[analysisID] = make(map[int]*runSubscription)
	}
	b.nextID++
	id := b.nextID
	b.subs[analysisID][id] = subscription
	register := !b.registered
	b.registered = true
	b.mu.Unlock()

	if register && b.bus != nil {
		b.bus.Subscribe(b)
	}

	return subscription.events, func() { b.unsubscribe(analysisID, id) }
}

// unsubscribe removes a run subscription. The channel is intentionally left
// open: the run loop stops reading as soon as it returns.
func (b *EventBridge) unsubscribe(analysisID int64, id int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	subs, ok := b.subs[analysisID]
	if !ok {
		return
	}
	delete(subs, id)
	if len(subs) == 0 {
		delete(b.subs, analysisID)
	}
}

// Handle implements event.Handler. It never blocks: a slow run drops events and
// relies on the reconciliation watchdog instead of stalling the bus worker.
func (b *EventBridge) Handle(evt event.Event) {
	runtimeEvent, ok := evt.(dagruntime.RuntimeEvent)
	if !ok {
		return
	}

	b.mu.Lock()
	targets := make([]*runSubscription, 0, len(b.subs[runtimeEvent.AnalysisID]))
	for _, subscription := range b.subs[runtimeEvent.AnalysisID] {
		targets = append(targets, subscription)
	}
	b.mu.Unlock()

	for _, subscription := range targets {
		select {
		case subscription.events <- runtimeEvent:
		default:
		}
	}
}

// Publisher emits DAG runtime events through the shared bus.
type Publisher struct {
	bus event.Bus
}

// NewPublisher creates a publisher. A nil bus makes publishing a no-op.
func NewPublisher(bus event.Bus) *Publisher {
	return &Publisher{bus: bus}
}

// PublishDag emits a run level event.
func (p *Publisher) PublishDag(name string, analysisID int64, payload map[string]any) {
	p.publish(dagruntime.RuntimeEvent{
		Name:       name,
		AnalysisID: analysisID,
		OccurredAt: time.Now().UTC(),
		Payload:    payload,
	})
}

// PublishNode emits a node level event.
func (p *Publisher) PublishNode(name string, analysisID int64, nodeID string, analysisNodeID int64) {
	p.publish(dagruntime.RuntimeEvent{
		Name:           name,
		AnalysisID:     analysisID,
		AnalysisNodeID: analysisNodeID,
		NodeID:         nodeID,
		OccurredAt:     time.Now().UTC(),
	})
}

func (p *Publisher) publish(evt dagruntime.RuntimeEvent) {
	if p == nil || p.bus == nil {
		return
	}
	p.bus.Publish(evt)
}
