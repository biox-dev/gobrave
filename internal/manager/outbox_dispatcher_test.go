package manager

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/types"
)

type mockBus struct {
	publishCount int
	published    []event.Event
}

func (m *mockBus) Publish(e event.Event) {
	m.publishCount++
	m.published = append(m.published, e)
}

func (m *mockBus) Subscribe(h event.Handler) {}

func TestOutboxDispatcher_DispatchOnce_MarksPendingSentAndPublishes(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	bus := &mockBus{published: make([]event.Event, 0)}

	evt1 := &types.ContainerEvent{ContainerInstanceID: 1, Event: "ContainerStarted", Message: "running"}
	payload1, err := json.Marshal(evt1)
	if err != nil {
		t.Fatalf("marshal payload1 failed: %v", err)
	}

	evt2 := &types.ContainerEvent{ContainerInstanceID: 2, Event: "ContainerStopped", Message: "stopped"}
	payload2, err := json.Marshal(evt2)
	if err != nil {
		t.Fatalf("marshal payload2 failed: %v", err)
	}

	if err := repo.CreateOutboxEvent(ctx, &types.OutboxEvent{Type: evt1.Event, Payload: payload1, Status: "pending"}); err != nil {
		t.Fatalf("create pending outbox1 failed: %v", err)
	}
	if err := repo.CreateOutboxEvent(ctx, &types.OutboxEvent{Type: evt2.Event, Payload: payload2, Status: "pending"}); err != nil {
		t.Fatalf("create pending outbox2 failed: %v", err)
	}
	if err := repo.CreateOutboxEvent(ctx, &types.OutboxEvent{Type: "AlreadySent", Payload: payload2, Status: "sent"}); err != nil {
		t.Fatalf("create sent outbox failed: %v", err)
	}

	d := &OutboxDispatcher{
		repo:         repo,
		bus:          bus,
		batchSize:    100,
		pollInterval: 0,
	}

	d.dispatchOnce(ctx)

	if bus.publishCount != 2 {
		t.Fatalf("expected publish count 2, got %d", bus.publishCount)
	}

	pending, err := repo.ListPendingOutboxEvent(ctx, 100)
	if err != nil {
		t.Fatalf("list pending outbox failed: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("expected no pending outbox after dispatch, got %d", len(pending))
	}
}
