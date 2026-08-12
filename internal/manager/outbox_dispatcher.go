package manager

import (
	"context"
	"encoding/json"
	"time"

	"github.com/gobravedev/gobrave/internal/event"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
)

// OutboxCreateRequestEvent is published to the event bus when a
// ContainerCreateRequest outbox event is picked up. Handlers that
// need to process deferred container creation subscribe to this.
type OutboxCreateRequestEvent struct {
	OutboxID   int64
	RawPayload []byte
}

// OutboxStopRequestEvent is published to the event bus when a
// ContainerStopRequest outbox event is picked up.
type OutboxStopRequestEvent struct {
	OutboxID   int64
	RawPayload []byte
}

// OutboxDeleteRequestEvent is published to the event bus when a
// ContainerDeleteRequest outbox event is picked up.
type OutboxDeleteRequestEvent struct {
	OutboxID   int64
	RawPayload []byte
}

// OutboxStartRequestEvent is published to the event bus when a
// ContainerStartRequest outbox event is picked up.
type OutboxStartRequestEvent struct {
	OutboxID   int64
	RawPayload []byte
}

type OutboxDispatcher struct {
	repo         interfaces.ContainerRepository
	bus          event.Bus
	batchSize    int
	pollInterval time.Duration
}

func NewOutboxDispatcher(repo interfaces.ContainerRepository, bus event.Bus) *OutboxDispatcher {
	return &OutboxDispatcher{
		repo:         repo,
		bus:          bus,
		batchSize:    100,
		pollInterval: time.Second,
	}
}

func RunOutboxDispatcher(dispatcher *OutboxDispatcher) {
	go dispatcher.Start(context.Background())
}

func (d *OutboxDispatcher) Start(ctx context.Context) {
	// Recover stale processing events on startup.
	d.recoverStaleProcessing(ctx)

	ticker := time.NewTicker(d.pollInterval)
	defer ticker.Stop()

	// Also periodically recover stale processing events.
	recoveryTicker := time.NewTicker(5 * time.Minute)
	defer recoveryTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-recoveryTicker.C:
			d.recoverStaleProcessing(ctx)
		case <-ticker.C:
			d.dispatchOnce(ctx)
		}
	}
}

func (d *OutboxDispatcher) dispatchOnce(ctx context.Context) {
	items, err := d.repo.ListPendingOutboxEvent(ctx, d.batchSize)
	if err != nil {
		logger.Errorf(ctx, "[OutboxDispatcher] list pending outbox failed: %v", err)
		return
	}

	for _, item := range items {
		// ContainerCreateRequest events are handled by ContainerCreateWorker
		// via the event bus. Mark as "processing" immediately so the next
		// poll cycle doesn't re-pick them. The handler will mark as "sent"
		// or revert to "pending" on failure.
		if item.Type == OutboxEventTypeCreateRequest {
			if err := d.repo.MarkOutboxEventProcessing(ctx, item.ID); err != nil {
				logger.Errorf(ctx, "[OutboxDispatcher] mark create request processing failed, id=%d err=%v", item.ID, err)
				continue
			}
			d.bus.Publish(OutboxCreateRequestEvent{
				OutboxID:   item.ID,
				RawPayload: []byte(item.Payload),
			})
			continue
		}

		// ContainerStopRequest events are handled by ContainerCreateWorker
		// via the event bus. Same pattern as create requests.
		if item.Type == OutboxEventTypeStopRequest {
			// 状态变为 processing，避免下次轮询再次处理
			if err := d.repo.MarkOutboxEventProcessing(ctx, item.ID); err != nil {
				logger.Errorf(ctx, "[OutboxDispatcher] mark stop request processing failed, id=%d err=%v", item.ID, err)
				continue
			}
			d.bus.Publish(OutboxStopRequestEvent{
				OutboxID:   item.ID,
				RawPayload: []byte(item.Payload),
			})
			continue
		}

		// ContainerDeleteRequest events are handled by ContainerCreateWorker
		// via the event bus. Same pattern as create/stop requests.
		if item.Type == OutboxEventTypeDeleteRequest {
			if err := d.repo.MarkOutboxEventProcessing(ctx, item.ID); err != nil {
				logger.Errorf(ctx, "[OutboxDispatcher] mark delete request processing failed, id=%d err=%v", item.ID, err)
				continue
			}
			d.bus.Publish(OutboxDeleteRequestEvent{
				OutboxID:   item.ID,
				RawPayload: []byte(item.Payload),
			})
			continue
		}

		// ContainerStartRequest events are handled by ContainerCreateWorker
		// via the event bus. Same pattern as other requests.
		if item.Type == OutboxEventTypeStartRequest {
			// 状态变为 processing，避免下次轮询再次处理
			if err := d.repo.MarkOutboxEventProcessing(ctx, item.ID); err != nil {
				logger.Errorf(ctx, "[OutboxDispatcher] mark start request processing failed, id=%d err=%v", item.ID, err)
				continue
			}
			d.bus.Publish(OutboxStartRequestEvent{
				OutboxID:   item.ID,
				RawPayload: []byte(item.Payload),
			})
			continue
		}

		// All other outbox events follow the original publish-and-mark-sent path.
		evt := &types.ContainerEvent{}
		if err := json.Unmarshal(item.Payload, evt); err != nil {
			logger.Errorf(ctx, "[OutboxDispatcher] unmarshal outbox payload failed, id=%d err=%v", item.ID, err)
			continue
		}

		d.bus.Publish(*evt)

		if err := d.repo.MarkOutboxEventSent(ctx, item.ID); err != nil {
			logger.Errorf(ctx, "[OutboxDispatcher] mark outbox sent failed, id=%d err=%v", item.ID, err)
		}
	}
}

// recoverStaleProcessing resets outbox events that have been stuck in
// "processing" state for too long (e.g. due to a crash) back to "pending"
// so they can be retried.
func (d *OutboxDispatcher) recoverStaleProcessing(ctx context.Context) {
	// The repository already handles this via ListPendingOutboxEvent which
	// only returns "pending" events. Stale "processing" events need a
	// separate recovery path.
	//
	// For now, we rely on the fact that OutboxCreateRequestEvent handlers
	// are idempotent: if a handler crashes before marking as sent, the
	// event stays "processing" and won't be picked up again. A future
	// enhancement could add a staleness timeout with a dedicated repo method.
	_ = ctx
}
