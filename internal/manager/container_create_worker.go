package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gobravedev/gobrave/internal/event"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
)

const (
	// OutboxEventTypeCreateRequest is the outbox event type for container creation requests.
	OutboxEventTypeCreateRequest = "ContainerCreateRequest"
)

// Ensure ContainerCreateWorker implements event.Handler.
var _ event.Handler = (*ContainerCreateWorker)(nil)

// containerCreatePayload is stored in OutboxEvent.Payload for deferred container creation.
type containerCreatePayload struct {
	ContainerInstanceID int64  `json:"container_instance_id"`
	RuntimeName         string `json:"runtime_name"`
	TemplateID          int64  `json:"template_id"`
	OwnerType           string `json:"owner_type"`
	OwnerID             int64  `json:"owner_id"`
	Name                string `json:"name"`
	UserID              string `json:"user_id,omitempty"`
}

// ContainerCreateWorker subscribes to OutboxCreateRequestEvent and ContainerEvent
// from the event bus. For creation requests it executes rt.Create + rt.Start,
// then holds the semaphore until the container reaches a terminal state
// (Stopped/Failed/Exited) — signalled by ContainerEvent from the bus.
// This ensures the semaphore tracks actual resource usage, not just creation rate.
type ContainerCreateWorker struct {
	mgr            *ContainerManager
	repo           interfaces.ContainerRepository
	maxConcurrency int
	maxPending     int
	startTimeout   time.Duration
	sem            chan struct{}
	tracking       sync.Map // instanceID → chan struct{}
	activeCount    atomic.Int64
}

// NewContainerCreateWorker creates a new worker.
func NewContainerCreateWorker(
	mgr *ContainerManager,
	repo interfaces.ContainerRepository,
	maxConcurrency int,
	maxPending int,
) *ContainerCreateWorker {
	if maxConcurrency <= 0 {
		maxConcurrency = 3
	}
	if maxPending <= 0 {
		maxPending = 50
	}
	return &ContainerCreateWorker{
		mgr:            mgr,
		repo:           repo,
		maxConcurrency: maxConcurrency,
		maxPending:     maxPending,
		startTimeout:   5 * time.Minute,
		sem:            make(chan struct{}, maxConcurrency),
	}
}

// Handle dispatches events from the event bus. It handles two event types:
//   - OutboxCreateRequestEvent: executes deferred container creation
//   - types.ContainerEvent: releases semaphore when a tracked container reaches a stable state
func (w *ContainerCreateWorker) Handle(evt event.Event) {
	switch e := evt.(type) {
	case OutboxCreateRequestEvent:
		// Acquire semaphore before processing — this blocks if at max concurrency.
		w.sem <- struct{}{}
		w.activeCount.Add(1)
		w.handleCreateRequest(context.Background(), e)
		w.activeCount.Add(-1)
		<-w.sem

	case types.ContainerEvent:
		w.handleContainerEvent(e)
	}
}

// handleCreateRequest unmarshals the payload, executes creation, then waits
// for the container to reach a stable state before marking the outbox as sent.
// The semaphore is held for the entire duration (including the wait).
func (w *ContainerCreateWorker) handleCreateRequest(ctx context.Context, req OutboxCreateRequestEvent) {
	var payload containerCreatePayload
	if err := json.Unmarshal(req.RawPayload, &payload); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] unmarshal create payload failed, outbox_id=%d err=%v", req.OutboxID, err)
		_ = w.repo.MarkOutboxEventSent(ctx, req.OutboxID)
		return
	}

	logger.Infof(ctx, "[ContainerCreateWorker] handling create request, outbox_id=%d instance_id=%d name=%s",
		req.OutboxID, payload.ContainerInstanceID, payload.Name)

	execCtx := ctx
	if payload.UserID != "" {
		execCtx = context.WithValue(ctx, types.UserIDContextKey, payload.UserID)
	}

	ownerType := types.ContainerOwnerType(payload.OwnerType)

	if err := w.mgr.executeCreate(
		execCtx,
		payload.RuntimeName,
		payload.TemplateID,
		ownerType,
		payload.OwnerID,
		payload.Name,
		payload.ContainerInstanceID,
	); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] execute create failed, instance_id=%d err=%v", payload.ContainerInstanceID, err)
		_ = w.repo.MarkOutboxEventPending(ctx, req.OutboxID)
		return
	}

	// Register a completion channel and wait for the container to reach
	// a stable state (via handleContainerEvent) or timeout.
	done := make(chan struct{}, 1)
	w.tracking.Store(payload.ContainerInstanceID, done)
	defer w.tracking.Delete(payload.ContainerInstanceID)

	// Check current status — the runtime event may have arrived before we registered.
	inst, _ := w.repo.GetContainerInstanceByID(ctx, payload.ContainerInstanceID)
	if inst != nil && isStableContainerStatus(inst.Status) {
		logger.Infof(ctx, "[ContainerCreateWorker] container already stable, instance_id=%d status=%s",
			payload.ContainerInstanceID, inst.Status)
	} else {
		logger.Debugf(ctx, "[ContainerCreateWorker] waiting for container to stabilize, instance_id=%d timeout=%s",
			payload.ContainerInstanceID, w.startTimeout)

		select {
		case <-done:
			logger.Infof(ctx, "[ContainerCreateWorker] container stabilized via event, instance_id=%d",
				payload.ContainerInstanceID)
		case <-time.After(w.startTimeout):
			logger.Warnf(ctx, "[ContainerCreateWorker] timed out waiting for container, instance_id=%d timeout=%s",
				payload.ContainerInstanceID, w.startTimeout)
		}
	}

	// Mark outbox as sent regardless of how we exited. The container lifecycle
	// continues independently via runtime events.
	if err := w.repo.MarkOutboxEventSent(ctx, req.OutboxID); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] mark outbox sent failed, outbox_id=%d err=%v", req.OutboxID, err)
	}
}

// handleContainerEvent checks if a ContainerEvent corresponds to a tracked
// in-flight container. If the container reached a stable status, it signals
// the waiting handleCreateRequest goroutine to release the semaphore.
func (w *ContainerCreateWorker) handleContainerEvent(ce types.ContainerEvent) {
	chRaw, ok := w.tracking.Load(ce.ContainerInstanceID)
	if !ok {
		return
	}

	if !isStableContainerEvent(ce.Event) {
		return
	}

	// Delete first to prevent double-close, then signal.
	w.tracking.Delete(ce.ContainerInstanceID)
	ch := chRaw.(chan struct{})

	select {
	case ch <- struct{}{}:
	default:
		// Already signaled (e.g., duplicate event).
	}
}

// Enqueue writes a container creation request to the outbox. It returns an error
// if the pending queue is full.
func (w *ContainerCreateWorker) Enqueue(ctx context.Context, req containerCreatePayload) error {
	if w.maxPending > 0 {
		count, err := w.repo.CountPendingOutboxEventsByType(ctx, OutboxEventTypeCreateRequest)
		if err != nil {
			return fmt.Errorf("count pending create requests: %w", err)
		}
		if count >= int64(w.maxPending) {
			return fmt.Errorf("container create queue is full (%d/%d pending), please try again later",
				count, w.maxPending)
		}
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal create payload: %w", err)
	}

	return w.repo.CreateOutboxEvent(ctx, &types.OutboxEvent{
		Type:    OutboxEventTypeCreateRequest,
		Payload: payload,
		Status:  "pending",
	})
}

// PendingCount returns the number of pending create requests in the queue.
func (w *ContainerCreateWorker) PendingCount(ctx context.Context) (int64, error) {
	return w.repo.CountPendingOutboxEventsByType(ctx, OutboxEventTypeCreateRequest)
}

// ActiveCount returns the number of currently executing create requests.
func (w *ContainerCreateWorker) ActiveCount() int64 {
	return w.activeCount.Load()
}

// MaxConcurrency returns the maximum number of concurrent container creations.
func (w *ContainerCreateWorker) MaxConcurrency() int {
	return w.maxConcurrency
}

// MaxPending returns the maximum number of queued creation requests.
func (w *ContainerCreateWorker) MaxPending() int {
	return w.maxPending
}

// isStableContainerStatus returns true when a container has reached a terminal
// (finished) state. Running is NOT considered terminal — the container still
// consumes resources while running, so the semaphore stays held.
func isStableContainerStatus(s types.ContainerStatus) bool {
	switch s {
	case types.ContainerStopped, types.ContainerFailed, types.ContainerExited:
		return true
	default:
		return false
	}
}

// isStableContainerEvent returns true for ContainerEvent names that indicate
// the container has finished (successfully or not). ContainerStarted is NOT
// included — the container is still running and consuming resources.
func isStableContainerEvent(eventName string) bool {
	switch eventName {
	case "ContainerStopped", "ContainerFailed", "ContainerExited", "ContainerDeleted":
		return true
	default:
		return false
	}
}
