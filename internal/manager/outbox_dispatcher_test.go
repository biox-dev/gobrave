package manager

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

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

// 进程在 MarkOutboxEventProcessing 之后崩溃时，事件会永远停在 processing。
// recoverStaleProcessing 必须把它放回 pending，否则该事件再也不会被投递。
func TestOutboxDispatcher_RecoverStaleProcessing_ResetsToPending(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	bus := &mockBus{published: make([]event.Event, 0)}

	stuck := &types.OutboxEvent{Type: OutboxEventTypeCreateRequest, Payload: []byte(`{}`), Status: "processing"}
	if err := repo.CreateOutboxEvent(ctx, stuck); err != nil {
		t.Fatalf("create stuck outbox failed: %v", err)
	}

	d := &OutboxDispatcher{repo: repo, bus: bus, batchSize: 100, recoverAfter: defaultOutboxStaleAfter}
	d.recoverStaleProcessing(ctx)

	if stuck.Status != "pending" {
		t.Fatalf("expected stale processing event reset to pending, got %s", stuck.Status)
	}
}

// 正在处理的事件（updated_at 在阈值内）不能被重置，否则会重复投递。
func TestOutboxDispatcher_RecoverStaleProcessing_KeepsFreshProcessing(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	bus := &mockBus{published: make([]event.Event, 0)}

	fresh := &types.OutboxEvent{
		Type:      OutboxEventTypeCreateRequest,
		Payload:   []byte(`{}`),
		Status:    "processing",
		UpdatedAt: time.Now(),
	}
	if err := repo.CreateOutboxEvent(ctx, fresh); err != nil {
		t.Fatalf("create fresh outbox failed: %v", err)
	}

	d := &OutboxDispatcher{repo: repo, bus: bus, batchSize: 100, recoverAfter: defaultOutboxStaleAfter}
	d.recoverStaleProcessing(ctx)

	if fresh.Status != "processing" {
		t.Fatalf("expected fresh processing event to stay processing, got %s", fresh.Status)
	}
}

// 删除命令必须幂等：实例记录已不存在时应返回 errWorkerInstanceGone，
// 由 handleDeleteRequest 标记 sent，而不是无限重试。
func TestContainerCreateWorker_ExecuteDelete_InstanceGone(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	rt := &dockerMockRuntime{}
	mgr := newTestManager(repo, rt)

	err := mgr.createWorker.executeDelete(ctx, 424242)
	if !errors.Is(err, errWorkerInstanceGone) {
		t.Fatalf("expected errWorkerInstanceGone, got %v", err)
	}
}

// 删除命令成功时必须自己删行（OnEvent 已不再删行）。
func TestContainerCreateWorker_ExecuteDelete_DeletesInstanceRow(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	rt := &dockerMockRuntime{}
	mgr := newTestManager(repo, rt)

	inst := &types.ContainerInstance{RuntimeID: "docker-del-1", Status: types.ContainerRunning, Name: "demo"}
	if err := repo.CreateContainerInstance(ctx, inst); err != nil {
		t.Fatalf("seed instance failed: %v", err)
	}
	// CreateContainerInstance 与真实仓储一致：owner 为空时按 ID 回填，这里显式设回状态。
	inst.Status = types.ContainerRunning

	if err := mgr.createWorker.executeDelete(ctx, inst.ID); err != nil {
		t.Fatalf("executeDelete failed: %v", err)
	}
	if _, err := repo.GetContainerInstanceByID(ctx, inst.ID); err == nil {
		t.Fatalf("expected instance row deleted by executeDelete")
	}
}

// 重建命令同样必须幂等：实例记录已不存在时应返回 errWorkerInstanceGone，
// 由 handleRecreateRequest 标记 sent，而不是每秒重试 record not found。
func TestContainerCreateWorker_ExecuteRecreate_InstanceGone(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	rt := &dockerMockRuntime{}
	_, worker := newTestManagerWithWorker(repo, rt)

	err := worker.executeRecreate(ctx, 424242)
	if !errors.Is(err, errWorkerInstanceGone) {
		t.Fatalf("expected errWorkerInstanceGone, got %v", err)
	}
}

// 重复投递的重建命令不能再次删除运行时资源：此时新的容器可能已经建好，
// 再删一次会把刚重建出来的容器杀掉。非 recreate_pending 状态按无操作处理。
func TestContainerCreateWorker_ExecuteRecreate_SkipsWhenAlreadyAdvanced(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	rt := &dockerMockRuntime{}
	_, worker := newTestManagerWithWorker(repo, rt)

	inst := &types.ContainerInstance{RuntimeID: "docker-new-1", Status: types.ContainerRunning, Name: "demo"}
	if err := repo.CreateContainerInstance(ctx, inst); err != nil {
		t.Fatalf("seed instance failed: %v", err)
	}
	inst.Status = types.ContainerRunning

	if err := worker.executeRecreate(ctx, inst.ID); err != nil {
		t.Fatalf("executeRecreate should be idempotent no-op, got %v", err)
	}
	if len(repo.outbox) != 0 {
		t.Fatalf("expected no outbox event when recreate already advanced, got %d", len(repo.outbox))
	}
	latest, err := repo.GetContainerInstanceByID(ctx, inst.ID)
	if err != nil {
		t.Fatalf("get instance failed: %v", err)
	}
	if latest.Status != types.ContainerRunning {
		t.Fatalf("expected instance status untouched, got %s", latest.Status)
	}
}

// 正常入口 recreate_pending：迁移到 recreating 并写 ContainerReCreating 事件。
func TestContainerCreateWorker_ExecuteRecreate_TransitionsFromRecreatePending(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	rt := &dockerMockRuntime{}
	_, worker := newTestManagerWithWorker(repo, rt)

	inst := &types.ContainerInstance{RuntimeID: "docker-old-1", Status: types.ContainerReCreatePending, Name: "demo"}
	if err := repo.CreateContainerInstance(ctx, inst); err != nil {
		t.Fatalf("seed instance failed: %v", err)
	}
	inst.Status = types.ContainerReCreatePending

	if err := worker.executeRecreate(ctx, inst.ID); err != nil {
		t.Fatalf("executeRecreate failed: %v", err)
	}
	latest, err := repo.GetContainerInstanceByID(ctx, inst.ID)
	if err != nil {
		t.Fatalf("get instance failed: %v", err)
	}
	if latest.Status != types.ContainerReCreating {
		t.Fatalf("expected status %s, got %s", types.ContainerReCreating, latest.Status)
	}

	found := false
	for _, evt := range repo.outbox {
		if evt.Type == "ContainerReCreating" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected ContainerReCreating outbox event, got %d events", len(repo.outbox))
	}
}
