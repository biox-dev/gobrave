package manager

import (
	"context"
	"encoding/json"
	"time"

	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
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

type OutboxRecreateRequestEvent struct {
	OutboxID   int64
	RawPayload []byte
}

// OutboxStartRequestEvent is published to the event bus when a
// ContainerStartRequest outbox event is picked up.
type OutboxStartRequestEvent struct {
	OutboxID   int64
	RawPayload []byte
}

// AISummaryGenerateRequestEvent is published to the event bus when an
// AISummaryGenerateRequest outbox event is picked up.
type AISummaryGenerateRequestEvent struct {
	OutboxID   int64
	RawPayload []byte
}

type OutboxDispatcher struct {
	repo         interfaces.ContainerRepository
	bus          event.Bus
	batchSize    int
	pollInterval time.Duration
	// recoverAfter 是 processing 事件被判定为「卡死」的超时阈值。
	// 必须大于任何 handler 的最长执行时间，否则可能重置正在处理的事件。
	recoverAfter time.Duration
}

// defaultOutboxStaleAfter 默认的 processing 超时阈值：创建链路在冷启动拉镜像时
// 可能持续数分钟，取 10 分钟留足余量。
const defaultOutboxStaleAfter = 10 * time.Minute

func NewOutboxDispatcher(repo interfaces.ContainerRepository, bus event.Bus) *OutboxDispatcher {
	return &OutboxDispatcher{
		repo:         repo,
		bus:          bus,
		batchSize:    100,
		pollInterval: time.Second,
		recoverAfter: defaultOutboxStaleAfter,
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

		if item.Type == OutboxEventTypeRecreateRequest {
			if err := d.repo.MarkOutboxEventProcessing(ctx, item.ID); err != nil {
				logger.Errorf(ctx, "[OutboxDispatcher] mark recreate request processing failed, id=%d err=%v", item.ID, err)
				continue
			}
			d.bus.Publish(OutboxRecreateRequestEvent{
				OutboxID:   item.ID,
				RawPayload: []byte(item.Payload),
			})
			continue
		}

		// AISummaryGenerateRequest events are handled by AISummaryWorker
		// via the event bus. Same pattern as other request events.
		if item.Type == OutboxEventTypeAISummaryGenerateRequest {
			if err := d.repo.MarkOutboxEventProcessing(ctx, item.ID); err != nil {
				logger.Errorf(ctx, "[OutboxDispatcher] mark ai summary request processing failed, id=%d err=%v", item.ID, err)
				continue
			}
			d.bus.Publish(AISummaryGenerateRequestEvent{
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
//
// ListPendingOutboxEvent only selects "pending" rows, so an event whose handler
// died between MarkOutboxEventProcessing and MarkOutboxEventSent/Pending would
// otherwise stay stuck forever. The staleness window must be longer than the
// slowest handler (container create/start can legitimately take minutes on a
// cold image pull), which is why it is measured from updated_at and kept well
// above the per-request wait budget.
func (d *OutboxDispatcher) recoverStaleProcessing(ctx context.Context) {
	// 阈值取得比任何 handler 的最长执行时间都大：创建/启动链路在冷启动拉镜像时
	// 可能持续数分钟，误重置正在处理的事件会导致重复投递。
	staleAfter := d.recoverAfter
	if staleAfter <= 0 {
		staleAfter = defaultOutboxStaleAfter
	}
	reset, err := d.repo.ResetStaleProcessingOutboxEvent(ctx, staleAfter)
	if err != nil {
		logger.Errorf(ctx, "[OutboxDispatcher] reset stale processing outbox failed: %v", err)
		return
	}
	if reset > 0 {
		logger.Warnf(ctx, "[OutboxDispatcher] reset %d stale processing outbox events back to pending, stale_after=%s", reset, staleAfter)
	}
}
