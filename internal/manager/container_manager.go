package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/fsm"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

var (
	errWorkerConcurrencyLimit = errors.New("worker concurrency limit reached")
)

type ContainerManager struct {
	containerService interfaces.ContainerService
	containerRepo    interfaces.ContainerRepository
	projectRepo      interfaces.ProjectRepository
	analysisRepo     interfaces.AnalysisRepository
	workflowService  interfaces.WorkflowService
	reg              *containerruntime.Registry
	bus              event.Bus
	res              ContainerRuntimeResolver
	img              *ImageManager
	cfg              *config.Config
	monitorOnce      sync.Once
}

func NewContainerManager(
	containerRepo interfaces.ContainerRepository,
	analysisRepo interfaces.AnalysisRepository,
	projectRepo interfaces.ProjectRepository,
	workflowService interfaces.WorkflowService,
	reg *containerruntime.Registry,
	bus event.Bus,
	res ContainerRuntimeResolver,
	img *ImageManager,
	cfg *config.Config,
) *ContainerManager {
	// if res == nil {
	// 	res = NewDefaultContainerRuntimeResolver()
	// }
	if img == nil {
		img = NewImageManager(containerRepo, reg)
	}
	return &ContainerManager{
		containerRepo: containerRepo, projectRepo: projectRepo, analysisRepo: analysisRepo, workflowService: workflowService, reg: reg, bus: bus, res: res, img: img, cfg: cfg}
}

func (s *ContainerManager) TransitionContainerAndEnqueueOutbox(ctx context.Context, inst *types.ContainerInstance, to types.ContainerStatus, eventType string) error {
	if inst == nil {
		return errors.New("container instance is nil")
	}

	return s.containerRepo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
		latest, err := tx.GetContainerInstanceByID(ctx, inst.ID)
		if err != nil {
			return err
		}
		if to == types.ContainerCreating || to == types.ContainerStarting {
			if s.GetMaxConcurrency() > 0 {
				active, err := tx.CountContainerInstanceByStatuses(ctx, concurrencyOccupiedStatuses())
				if err != nil {
					return err
				}
				if active >= int64(s.GetMaxConcurrency()) {
					return fmt.Errorf("%w: active=%d max=%d", errWorkerConcurrencyLimit, active, s.GetMaxConcurrency())
				}
			}
		}

		if latest.Status == to {
			inst.Status = latest.Status
			return nil
		}

		f := &fsm.FSM{}
		if err := f.Transition(latest.Status, to); err != nil {
			return err
		}

		inst.Status = to
		if err := tx.UpdateContainerInstance(ctx, inst); err != nil {
			return err
		}

		domainEvent := &types.ContainerEvent{
			ContainerInstanceID: inst.ID,
			Event:               eventType,
			Message:             string(to),
		}
		// if err := tx.CreateContainerEvent(ctx, domainEvent); err != nil {
		// 	return err
		// }

		payload, err := json.Marshal(domainEvent)
		if err != nil {
			return err
		}

		return tx.CreateOutboxEvent(ctx, &types.OutboxEvent{
			Type:    eventType,
			Payload: payload,
			Status:  "pending",
		})
	})
}

func concurrencyOccupiedStatuses() []types.ContainerStatus {
	return []types.ContainerStatus{
		types.ContainerCreating,
		types.ContainerRunning,
		types.ContainerStarting,
		types.ContainerStopping,
		types.ContainerStopPending,
	}
}

func (s *ContainerManager) GetMaxConcurrency() int {
	maxConcurrency := 3
	if s.cfg != nil && s.cfg.Container != nil && s.cfg.Container.CreateQueueMaxConcurrency > 0 {
		maxConcurrency = s.cfg.Container.CreateQueueMaxConcurrency
	}

	if maxConcurrency <= 0 {
		maxConcurrency = 3
	}
	return maxConcurrency
}

// func (m *ContainerManager) Create(ctx context.Context, spec Spec) error {

// 	// 1. FSM: pending -> creating
// 	inst := m.createInstance(spec)

// 	_ = m.transition(ctx, inst, Creating, "ContainerCreating")

// 	// 2. Runtime
// 	runtimeID, err := m.runtime.Create(ctx, spec)
// 	if err != nil {
// 		_ = m.transition(ctx, inst, Failed, "ContainerFailed")
// 		return err
// 	}

// 	inst.RuntimeID = runtimeID

// 	// 3. FSM: creating -> running
// 	_ = m.transition(ctx, inst, Running, "ContainerStarted")

//		return nil
//	}
func (m *ContainerManager) CreateByTemplate(
	ctx context.Context,
	templateID int64,
	ownerType types.ContainerOwnerType,
	ownerID int64,
	name string,
) (*types.ContainerInstance, error) {

	tpl, err := m.containerRepo.GetContainerTemplateByID(ctx, templateID)
	if err != nil {
		return nil, err
	}

	_, err = m.containerRepo.GetContainerImageByID(ctx, tpl.ImageID)
	if err != nil {
		return nil, err
	}

	_, err = m.getRuntime()
	if err != nil {
		return nil, err
	}

	inst := &types.ContainerInstance{
		TemplateID: templateID,
		OwnerType:  ownerType,
		OwnerID:    ownerID,
		Name:       name,
		Status:     types.ContainerCreatePending,
	}

	// Always enqueue the create request through the worker.
	// userID := ""
	// if ctx != nil {
	// 	if uid, ok := ctx.Value(types.UserIDContextKey).(string); ok {
	// 		userID = uid
	// 	} else if uid, ok := ctx.Value(types.UserIDContextKey.String()).(string); ok {
	// 		userID = uid
	// 	}
	// }

	// req := containerCreatePayload{
	// 	// RuntimeName: runtimeName,
	// 	TemplateID: templateID,
	// 	OwnerType:  string(ownerType),
	// 	OwnerID:    ownerID,
	// 	Name:       name,
	// 	UserID:     userID,
	// }
	req := types.ContainerEvent{
		ContainerInstanceID: inst.ID,
		Event:               string(types.ContainerCreatePending),
		Message:             string(types.ContainerCreatePending),
	}
	maxPending := m.getCreateQueueMaxPending()

	err = m.containerRepo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
		if maxPending > 0 {
			count, err := m.countPendingCreateQueueRequests(ctx, tx)
			if err != nil {
				return fmt.Errorf("count pending create/start requests: %w", err)
			}
			if count >= int64(maxPending) {
				return fmt.Errorf("container create queue is full (%d/%d pending), please try again later",
					count, maxPending)
			}
		}

		if err := tx.CreateContainerInstance(ctx, inst); err != nil {
			return err
		}
		// if err := tx.CreateContainerEvent(ctx, &types.ContainerEvent{
		// 	ContainerInstanceID: inst.ID,
		// 	Event:               "ContainerPending",
		// 	Message:             "container instance created",
		// }); err != nil {
		// 	return err
		// }

		req.ContainerInstanceID = inst.ID
		payload, err := json.Marshal(req)
		if err != nil {
			return fmt.Errorf("marshal create payload: %w", err)
		}

		return tx.CreateOutboxEvent(ctx, &types.OutboxEvent{
			Type:    OutboxEventTypeCreateRequest,
			Payload: payload,
			Status:  "pending",
		})
	})
	if err != nil {
		return nil, err
	}

	logger.Infof(ctx, "[ContainerManager] enqueued create request, instance_id=%d name=%s", inst.ID, name)
	return inst, nil
}

func (m *ContainerManager) getCreateQueueMaxPending() int {
	if m.cfg != nil && m.cfg.Container != nil && m.cfg.Container.CreateQueueMaxPending > 0 {
		return m.cfg.Container.CreateQueueMaxPending
	}
	return 50
}

// func (m *ContainerManager) getCreateQueueMaxConcurrency() int {
// 	if m.cfg != nil && m.cfg.Container != nil && m.cfg.Container.CreateQueueMaxConcurrency > 0 {
// 		return m.cfg.Container.CreateQueueMaxConcurrency
// 	}
// 	return 3
// }

// func (m *ContainerManager) CreateQueueMaxConcurrency() int {
// 	return m.getCreateQueueMaxConcurrency()
// }

func (m *ContainerManager) CreateQueueMaxPending() int {
	return m.getCreateQueueMaxPending()
}

func (m *ContainerManager) QueueStatus(ctx context.Context) (*CreateQueueStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	status := &CreateQueueStatus{
		MaxConcurrency: m.GetMaxConcurrency(),
		MaxPending:     m.getCreateQueueMaxPending(),
	}

	err := m.containerRepo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
		active, err := tx.CountContainerInstanceByStatuses(ctx, concurrencyOccupiedStatuses())
		if err != nil {
			return err
		}
		pending, err := m.countPendingCreateQueueRequests(ctx, tx)
		if err != nil {
			return err
		}
		status.ActiveCount = active
		status.PendingCount = pending
		return nil
	})
	if err != nil {
		return nil, err
	}

	return status, nil
}

func (m *ContainerManager) Start(ctx context.Context, id int64) error {
	maxPending := m.getCreateQueueMaxPending()
	if maxPending > 0 {
		count, err := m.countPendingCreateQueueRequests(ctx, m.containerRepo)
		if err != nil {
			return fmt.Errorf("count pending create/start requests: %w", err)
		}
		if count >= int64(maxPending) {
			return fmt.Errorf("container start queue is full (%d/%d pending), please try again later",
				count, maxPending)
		}
	}

	// userID := ""
	// if ctx != nil {
	// 	if uid, ok := ctx.Value(types.UserIDContextKey).(string); ok {
	// 		userID = uid
	// 	} else if uid, ok := ctx.Value(types.UserIDContextKey.String()).(string); ok {
	// 		userID = uid
	// 	}
	// }

	// req := containerStartPayload{
	// 	ContainerInstanceID: id,
	// 	UserID:              userID,
	// }
	instance, err := m.containerRepo.GetContainerInstanceByID(ctx, id)
	if err != nil {
		return err
	}
	if err := m.TransitionContainerAndEnqueueOutbox(ctx, instance, types.ContainerStartPending, OutboxEventTypeStartRequest); err != nil {
		logger.Errorf(ctx, "[ContainerManager] enqueue start failed, instance_id=%d err=%v", id, err)

		return err
	}

	// if err := m.enqueueLifecycleRequest(ctx, id, fsm.StartPending, OutboxEventTypeStartRequest, req); err != nil {
	// 	logger.Errorf(ctx, "[ContainerManager] enqueue start failed, instance_id=%d err=%v", id, err)
	// 	return err
	// }

	logger.Infof(ctx, "[ContainerManager] enqueued start request, instance_id=%d", id)
	return nil
}

func (m *ContainerManager) countPendingCreateQueueRequests(ctx context.Context, repo interfaces.ContainerRepository) (int64, error) {
	return repo.CountPendingOutboxEvents(ctx, OutboxEventTypeCreateRequest, OutboxEventTypeStartRequest)
}

func (m *ContainerManager) Stop(ctx context.Context, id int64) error {
	inst, err := m.containerRepo.GetContainerInstanceByID(ctx, id)
	if err != nil {
		return err
	}

	// If already in a terminal state, nothing to do.
	switch strings.TrimSpace(strings.ToLower(string(inst.Status))) {
	case string(types.ContainerStopped), string(types.ContainerFailed), string(types.ContainerExited):
		return types.ErrContainerAlreadyStopped
	}
	instaince, err := m.containerRepo.GetContainerInstanceByID(ctx, id)
	if err != nil {
		return err
	}
	if err := m.TransitionContainerAndEnqueueOutbox(ctx, instaince, types.ContainerStopPending, OutboxEventTypeStopRequest); err != nil {
		logger.Errorf(ctx, "[ContainerManager] enqueue stop failed, instance_id=%d err=%v", id, err)
		return err
	}

	// userID := ""
	// if ctx != nil {
	// 	if uid, ok := ctx.Value(types.UserIDContextKey).(string); ok {
	// 		userID = uid
	// 	} else if uid, ok := ctx.Value(types.UserIDContextKey.String()).(string); ok {
	// 		userID = uid
	// 	}
	// }

	// req := containerStopPayload{
	// 	ContainerInstanceID: inst.ID,
	// 	UserID:              userID,
	// }

	// if err := m.enqueueLifecycleRequest(ctx, inst.ID, fsm.StopPending, OutboxEventTypeStopRequest, req); err != nil {
	// 	logger.Errorf(ctx, "[ContainerManager] enqueue stop failed, instance_id=%d err=%v", inst.ID, err)
	// 	return err
	// }

	logger.Infof(ctx, "[ContainerManager] enqueued stop request, instance_id=%d", inst.ID)
	return nil
}

func (m *ContainerManager) StopByOwner(ctx context.Context, ownerType types.ContainerOwnerType, ownerID int64) error {
	inst, err := m.containerRepo.GetContainerInstanceByOwner(ctx, ownerType, ownerID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if inst == nil || inst.ID == 0 {
		return nil
	}
	switch strings.TrimSpace(strings.ToLower(string(inst.Status))) {
	case string(types.ContainerStopped), string(types.ContainerFailed), string(types.ContainerExited),
		string(types.ContainerStopPending), string(types.ContainerStopping),
		string(types.ContainerStartPending), string(types.ContainerStarting):
		return nil
	}
	return m.Stop(ctx, inst.ID)
}

func (m *ContainerManager) Delete(ctx context.Context, id int64) error {
	inst, err := m.containerRepo.GetContainerInstanceByID(ctx, id)
	if err != nil {
		return err
	}

	// If already deleted, nothing to do.
	if inst == nil || inst.ID == 0 {
		return nil
	}
	instance, err := m.containerRepo.GetContainerInstanceByID(ctx, id)
	if err != nil {
		return err
	}
	if err := m.TransitionContainerAndEnqueueOutbox(ctx, instance, types.ContainerDeletePending, OutboxEventTypeDeleteRequest); err != nil {
		logger.Errorf(ctx, "[ContainerManager] enqueue delete failed, instance_id=%d err=%v", id, err)
		return err
	}

	// userID := ""
	// if ctx != nil {
	// 	if uid, ok := ctx.Value(types.UserIDContextKey).(string); ok {
	// 		userID = uid
	// 	} else if uid, ok := ctx.Value(types.UserIDContextKey.String()).(string); ok {
	// 		userID = uid
	// 	}
	// }

	// req := containerDeletePayload{
	// 	ContainerInstanceID: inst.ID,
	// 	UserID:              userID,
	// }

	// if err := m.enqueueLifecycleRequest(ctx, inst.ID, fsm.DeletePending, OutboxEventTypeDeleteRequest, req); err != nil {
	// 	logger.Errorf(ctx, "[ContainerManager] enqueue delete failed, instance_id=%d err=%v", inst.ID, err)
	// 	return err
	// }

	logger.Infof(ctx, "[ContainerManager] enqueued delete request, instance_id=%d", inst.ID)
	return nil
}

func (m *ContainerManager) GetLogs(ctx context.Context, id int64, tail int) (string, error) {
	if tail <= 0 {
		tail = 200
	}
	inst, err := m.containerRepo.GetContainerInstanceByID(ctx, id)
	if err != nil {
		return "", err
	}
	rt, err := m.getRuntimeByInstance(inst)
	if err != nil {
		return "", err
	}

	return rt.Logs(ctx, inst.RuntimeID, tail)
}

// func (m *ContainerManager) OnRuntimeEvent(e RuntimeEvent) {

// 	inst := m.repo.FindByRuntimeID(e.RuntimeID)

// 	switch e.Type {

// 	case "OOMKilled":

// 		_ = m.transition(
// 			context.Background(),
// 			inst,
// 			Failed,
// 			"ContainerOOM",
// 		)

// 	case "Exited":

//		_ = m.transition(
//			context.Background(),
//			inst,
//			Stopped,
//			"ContainerStopped",
//		)
//	}

// 如果容器运行太快，ContainerStarted与 ContainerExited可能会竞争
func (m *ContainerManager) OnEvent(e containerruntime.RuntimeEvent) {
	ctx := context.Background()
	inst, err := m.containerRepo.GetContainerInstanceByRuntimeID(ctx, e.RuntimeID)
	if err != nil {
		logger.Warnf(ctx, "[ContainerManager] OnEvent: GetContainerInstanceByRuntimeID failed, runtime_id=%s event=%s err=%v", e.RuntimeID, e.Type, err)
		return
	}

	// 防御性检查：如果收到终端事件但容器状态仍为 Creating，
	// 说明 ContainerStarted 事件可能丢失，先补偿 started 时间戳再处理。
	currentStatus := strings.ToLower(strings.TrimSpace(string(inst.Status)))
	isCreating := currentStatus == string(types.ContainerCreating)
	isTerminalEvent := e.Type == "ContainerExited" || e.Type == "ContainerFailed"
	if isCreating && isTerminalEvent {
		logger.Warnf(ctx, "[ContainerManager] received terminal event while container still creating, runtime_id=%s event=%s status=%s, compensating started_at", e.RuntimeID, e.Type, inst.Status)
		now := time.Now()
		inst.StartedAt = &now
		inst.FinishedAt = nil
		if rt, rtErr := m.getRuntimeByInstance(inst); rtErr == nil {
			m.syncInstanceIPAddress(ctx, rt, inst)
		}
		// 先尝试转 Running，失败也无妨（FSM 现在允许 Creating→Stopped）
		// _ = m.transition(ctx, inst, fsm.Running, "ContainerStarted")
		_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerRunning, "ContainerStarted")
	}

	switch e.Type {

	case "ContainerStarted":
		now := time.Now()
		inst.StartedAt = &now
		inst.FinishedAt = nil
		if rt, rtErr := m.getRuntimeByInstance(inst); rtErr == nil {
			m.syncInstanceIPAddress(ctx, rt, inst)
		}
		// _ = m.transition(ctx, inst, fsm.Running, "ContainerStarted")
		_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerRunning, "ContainerStarted")

	case "ContainerPaused":
		// _ = m.transition(ctx, inst, fsm.Paused, "ContainerPaused")
		_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerPaused, "ContainerPaused")

	case "ContainerResumed":
		now := time.Now()
		inst.StartedAt = &now
		inst.FinishedAt = nil
		if rt, rtErr := m.getRuntimeByInstance(inst); rtErr == nil {
			m.syncInstanceIPAddress(ctx, rt, inst)
		}
		// _ = m.transition(ctx, inst, fsm.Running, "ContainerResumed")
		_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerRunning, "ContainerResumed")

	case "ContainerStopped":
		now := time.Now()
		inst.FinishedAt = &now
		if code, ok := parseRuntimeExitCode(e.Message); ok {
			inst.ExitCode = &code
		}
		// _ = m.transition(ctx, inst, fsm.Stopped, "ContainerStopped")
		_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerStopped, "ContainerStopped")

	case "ContainerExited":
		now := time.Now()
		inst.FinishedAt = &now
		if code, ok := parseRuntimeExitCode(e.Message); ok {
			inst.ExitCode = &code
		}
		// _ = m.transition(ctx, inst, fsm.Stopped, "ContainerStopped")
		_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerStopped, "ContainerStopped")

	case "ContainerFailed":
		now := time.Now()
		inst.FinishedAt = &now
		if code, ok := parseRuntimeExitCode(e.Message); ok {
			inst.ExitCode = &code
		}
		// _ = m.transition(ctx, inst, fsm.Failed, "ContainerFailed")
		_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerFailed")

	case "ContainerDeleted":
		now := time.Now()
		inst.FinishedAt = &now
		// _ = m.transition(ctx, inst, fsm.Stopped, "ContainerStopped")

		// Publish delete event immediately so current subscribers can react in this callback flow.
		// if m.bus != nil {
		// 	m.bus.Publish(types.ContainerEvent{
		// 		ContainerInstanceID: inst.ID,
		// 		Event:               "ContainerDeleted",
		// 		Message:             e.Message,
		// 	})
		// }
		if inst.Status == types.ContainerReCreating {
			_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerCreatePending, OutboxEventTypeCreateRequest)

		} else {
			_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerDeleted, "ContainerDeleted")

			if err := m.containerRepo.DeleteContainerInstance(ctx, inst.ID); err != nil {
				logger.Warnf(ctx, "[ContainerManager] delete container instance failed, instance_id=%d runtime_id=%s err=%v", inst.ID, e.RuntimeID, err)
			}
		}

		// default:
		// _ = m.createContainerEvent(context.Background(), inst.ID, e.Type, e.Message)
	}
}

func parseRuntimeExitCode(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	code, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return code, true
}

// func (m *ContainerManager) transition(
// 	ctx context.Context,
// 	inst *types.ContainerInstance,
// 	to fsm.State,
// 	eventType string,
// ) error {
// 	if inst == nil {
// 		return errors.New("container instance is nil")
// 	}

// 	return m.repo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
// 		latest, err := tx.GetContainerInstanceByID(ctx, inst.ID)
// 		if err != nil {
// 			return err
// 		}

// 		if latest.Status == types.ContainerStatus(to) {
// 			inst.Status = latest.Status
// 			return nil
// 		}

// 		f := &fsm.FSM{}
// 		if err := f.Transition(fsm.State(latest.Status), to); err != nil {
// 			return err
// 		}

// 		inst.Status = types.ContainerStatus(to)
// 		if err := tx.UpdateContainerInstance(ctx, inst); err != nil {
// 			return err
// 		}

// 		domainEvent := &types.ContainerEvent{
// 			ContainerInstanceID: inst.ID,
// 			Event:               eventType,
// 			Message:             string(to),
// 		}
// 		// if err := tx.CreateContainerEvent(ctx, domainEvent); err != nil {
// 		// 	return err
// 		// }

// 		payload, err := json.Marshal(domainEvent)
// 		if err != nil {
// 			return err
// 		}

// 		return tx.CreateOutboxEvent(ctx, &types.OutboxEvent{
// 			Type:    eventType,
// 			Payload: payload,
// 			Status:  "pending",
// 		})
// 	})
// }

func (m *ContainerManager) getRuntime() (containerruntime.Runtime, error) {
	runtimeName := m.resolveRuntimeName()
	if runtimeName == "" {
		return nil, errors.New("runtime name is required")
	}
	rt := m.reg.Get(runtimeName)
	if rt == nil {
		return nil, fmt.Errorf("runtime not found: %s", runtimeName)
	}
	return rt, nil
}

func (m *ContainerManager) resolveRuntimeName() string {
	// runtimeName = strings.TrimSpace(runtimeName)
	// if runtimeName != "" {
	// 	normalized := strings.ToLower(runtimeName)
	// 	switch normalized {
	// 	case "kubernetes":
	// 		normalized = "k8s"
	// 	}

	// 	if m.reg != nil {
	// 		if m.reg.Get(normalized) != nil {
	// 			return normalized
	// 		}
	// 		if normalized == "k8s" && m.reg.Get("k3s") != nil {
	// 			return "k3s"
	// 		}
	// 		if normalized == "k3s" && m.reg.Get("k8s") != nil {
	// 			return "k8s"
	// 		}
	// 	}

	// 	return normalized
	// }

	if m.cfg != nil {
		resolved := config.ResolveContainerRuntime(m.cfg)
		if strings.TrimSpace(resolved) != "" {
			return resolved
		}
	}

	runtimes := m.reg.List()
	if len(runtimes) == 1 {
		return runtimes[0].Name()
	}

	return "docker"
}

func (m *ContainerManager) getRuntimeByInstance(inst *types.ContainerInstance) (containerruntime.Runtime, error) {
	if inst == nil {
		return nil, errors.New("container instance is nil")
	}

	for _, item := range m.reg.List() {
		if strings.HasPrefix(inst.RuntimeID, item.Name()+"-") {
			return item, nil
		}
	}

	items := m.reg.List()
	if len(items) == 1 {
		return items[0], nil
	}

	return nil, fmt.Errorf("failed to resolve runtime for instance %d", inst.ID)
}

// func (m *ContainerManager) getInstanceAndRuntime(ctx context.Context, id int64) (*types.ContainerInstance, containerruntime.Runtime, error) {
// inst, err := m.repo.GetContainerInstanceByID(ctx, id)
// if err != nil {
// 	return nil, nil, err
// }
// rt, err := m.getRuntimeByInstance(inst)
// if err != nil {
// 	return nil, nil, err
// }
// 	return inst, rt, nil
// }

// func (m *ContainerManager) createContainerEvent(ctx context.Context, instanceID int64, evt string, msg string) error {
// 	return m.repo.CreateContainerEvent(ctx, &types.ContainerEvent{
// 		ContainerInstanceID: instanceID,
// 		Event:               evt,
// 		Message:             msg,
// 	})
// // // }

// func (m *ContainerManager) enqueueLifecycleRequest(
// 	ctx context.Context,
// 	instanceID int64,
// 	to fsm.State,
// 	requestType string,
// 	requestPayload interface{},
// ) error {
// 	return m.repo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
// 		latest, err := tx.GetContainerInstanceByID(ctx, instanceID)
// 		if err != nil {
// 			return err
// 		}
// 		// ctx, id, fsm.StartPending, OutboxEventTypeStartRequest, req

// 		if latest.Status != types.ContainerStatus(to) {
// 			f := &fsm.FSM{}
// 			if err := f.Transition(fsm.State(latest.Status), to); err != nil {
// 				return err
// 			}

// 			latest.Status = types.ContainerStatus(to)
// 			if err := tx.UpdateContainerInstance(ctx, latest); err != nil {
// 				return err
// 			}

// 			// domainEvent := &types.ContainerEvent{
// 			// 	ContainerInstanceID: latest.ID,
// 			// 	Event:               transitionEventType,
// 			// 	Message:             string(to),
// 			// }
// 			// if err := tx.CreateContainerEvent(ctx, domainEvent); err != nil {
// 			// 	return err
// 			// }
// 		}

// 		payload, err := json.Marshal(requestPayload)
// 		if err != nil {
// 			return err
// 		}

// 		return tx.CreateOutboxEvent(ctx, &types.OutboxEvent{
// 			Type:    requestType,
// 			Payload: payload,
// 			Status:  "pending",
// 		})
// 	})
// }

func setRuntimeVar(vars map[string]string, key string, value string) {
	if vars == nil {
		return
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	vars[key] = value
}

func ensureEmptyFileIfNotExists(ctx context.Context, filePath string) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		logger.Warnf(ctx, "[ContainerManager] create profile directory failed, path=%s err=%v", filePath, err)
		return
	}

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return
		}
		logger.Warnf(ctx, "[ContainerManager] create empty profile failed, path=%s err=%v", filePath, err)
		return
	}
	_ = file.Close()
}

func resolvePathGID(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}

	return strconv.FormatUint(uint64(stat.Gid), 10), true
}

func parseCommand(raw string) []string {
	cmd := strings.TrimSpace(raw)
	if cmd == "" {
		return nil
	}
	return strings.Fields(cmd)
}

func applyDagNodeTaskSpec(spec *types.ContainerSpec, logPath string) {
	if spec == nil {
		return
	}

	quotedLogPath := shellSingleQuote(strings.TrimSpace(logPath))
	script := "bash ./run.sh 2>&1 | tee " + quotedLogPath + "; exit ${PIPESTATUS[0]}"

	spec.Entrypoint = []string{"bash"}
	spec.Command = []string{"-c", script}
}

func (m *ContainerManager) applyDagNodeRuntimeSpec(spec *types.ContainerSpec, resolveVars map[string]string) {
	if spec == nil {
		return
	}

	logPath := strings.TrimSpace(resolveVars["LOG_PATH"])
	if logPath == "" {
		if envLogPath, ok := os.LookupEnv("LOG_PATH"); ok {
			logPath = strings.TrimSpace(envLogPath)
		}
	}
	applyDagNodeTaskSpec(spec, logPath)

	if strings.TrimSpace(spec.WorkDir) == "" {
		if workspacePath := strings.TrimSpace(resolveVars["WORKSPACE_PATH"]); workspacePath != "" {
			spec.WorkDir = workspacePath
		}
	}

	uid := strings.TrimSpace(resolveVars["USERID"])
	if uid == "" {
		return
	}
	gid := strings.TrimSpace(resolveVars["GROUPID"])
	runtimeName := m.resolveRuntimeName()
	switch strings.ToLower(strings.TrimSpace(runtimeName)) {
	case "docker":
		if gid != "" {
			spec.User = uid + ":" + gid
			return
		}
		spec.User = uid
	case "k8s", "k3s", "kubernetes":
		// Kubernetes runtime currently maps ContainerSpec.User to runAsUser,
		// so set a plain uid value here.
		spec.User = uid
	}
}

func shellSingleQuote(text string) string {
	value := strings.TrimSpace(text)
	if value == "" {
		value = "./run.log"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func sanitizeKubernetesResourceName(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}

	b := strings.Builder{}
	b.Grow(len(raw))
	lastDash := false
	for _, r := range raw {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}

	value := strings.Trim(b.String(), "-")
	if len(value) > 50 {
		value = strings.Trim(value[:50], "-")
	}
	return value
}

func (m *ContainerManager) syncInstanceIPAddress(ctx context.Context, rt containerruntime.Runtime, inst *types.ContainerInstance) {
	if inst == nil || rt == nil {
		return
	}

	inspector, ok := rt.(containerruntime.RuntimeInspector)
	if !ok {
		return
	}

	inspection, err := inspector.Inspect(ctx, inst.RuntimeID)
	if err != nil {
		logger.Warnf(ctx, "[ContainerManager] inspect runtime ip failed, instance_id=%d err=%v", inst.ID, err)
		return
	}
	if inspection == nil {
		return
	}

	ip := strings.TrimSpace(inspection.IPAddress)
	nodeName := strings.TrimSpace(inspection.NodeName)

	changed := false
	if ip != "" && ip != inst.IPAddress {
		inst.IPAddress = ip
		changed = true
	}
	if nodeName != "" && nodeName != inst.RuntimeNodeName {
		inst.RuntimeNodeName = nodeName
		changed = true
	}

	if !changed {
		return
	}

	if err := m.containerRepo.UpdateContainerInstance(ctx, inst); err != nil {
		logger.Warnf(ctx, "[ContainerManager] persist runtime inspect failed, instance_id=%d ip=%s node=%s err=%v", inst.ID, inst.IPAddress, inst.RuntimeNodeName, err)
	}
}

func parseEnv(raw []byte) map[string]string {
	if len(raw) == 0 {
		return map[string]string{}
	}

	obj := map[string]string{}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj
	}

	mixedObj := map[string]interface{}{}
	if err := json.Unmarshal(raw, &mixedObj); err == nil {
		out := map[string]string{}
		for rawKey, val := range mixedObj {
			k := strings.TrimSpace(rawKey)
			if k == "" {
				continue
			}
			if text, ok := normalizeScalarValue(val); ok {
				out[k] = text
			}
		}
		return out
	}

	pairs := []map[string]string{}
	if err := json.Unmarshal(raw, &pairs); err == nil {
		out := map[string]string{}
		for _, kv := range pairs {
			k := strings.TrimSpace(kv["key"])
			if k == "" {
				continue
			}
			out[k] = kv["value"]
		}
		return out
	}

	return map[string]string{}
}

func parseVolumes(raw []byte, ownerType types.ContainerOwnerType) []types.ContainerVolume {
	if len(raw) == 0 {
		return nil
	}

	// obj := map[string]map[string]interface{}{}
	// if err := json.Unmarshal(raw, &obj); err == nil {
	// 	out := make([]types.ContainerVolume, 0, len(obj))
	// 	for rawTarget, item := range obj {
	// 		target := strings.TrimSpace(rawTarget)
	// 		if target == "" {
	// 			continue
	// 		}

	// 		source := target
	// 		if bind, ok := item["bind"]; ok {
	// 			if text, ok := normalizeScalarValue(bind); ok {
	// 				source = strings.TrimSpace(text)
	// 			}
	// 		}
	// 		mode := ""
	// 		if rawMode, ok := item["mode"]; ok {
	// 			if text, ok := normalizeScalarValue(rawMode); ok {
	// 				mode = strings.TrimSpace(text)
	// 			}
	// 		}
	// 		volType := ""
	// 		if rawType, ok := item["type"]; ok {
	// 			if text, ok := normalizeScalarValue(rawType); ok {
	// 				volType = strings.TrimSpace(text)
	// 			}
	// 		}
	// 		if rawOwner, ok := item["owner"]; ok {
	// 			if text, ok := normalizeScalarValue(rawOwner); ok {
	// 				if text != string(ownerType) {
	// 					continue
	// 				}
	// 			}
	// 		}

	// 		if source == "" || target == "" {
	// 			continue
	// 		}
	// 		out = append(out, types.ContainerVolume{Source: source, Target: target, Mode: mode, Type: volType})
	// 	}
	// 	return out
	// }

	volumes := []types.ContainerVolume{}
	if err := json.Unmarshal(raw, &volumes); err != nil {
		return nil
	}

	out := make([]types.ContainerVolume, 0, len(volumes))
	for _, vol := range volumes {
		source := strings.TrimSpace(vol.Source)
		target := strings.TrimSpace(vol.Target)
		if source == "" || target == "" {
			continue
		}
		if vol.Owner != "" && strings.TrimSpace(vol.Owner) != string(ownerType) {
			continue
		}
		out = append(out, types.ContainerVolume{
			Source: source,
			Target: target,
			Mode:   strings.TrimSpace(vol.Mode),
			Type:   strings.TrimSpace(vol.Type),
		})
	}

	return out
}

func parseSchedulingConstraint(raw []byte) *types.ContainerSchedulingSelector {
	if len(raw) == 0 {
		return nil
	}

	parsed := &types.ContainerSchedulingSelector{}
	if err := json.Unmarshal(raw, parsed); err != nil {
		return nil
	}

	constraints := make([]types.ContainerSchedulingConstraint, 0, len(parsed.Constraints))
	for _, item := range parsed.Constraints {
		constraintType := strings.TrimSpace(item.Type)
		key := strings.TrimSpace(item.Key)
		operator := strings.TrimSpace(item.Operator)
		if constraintType == "" || key == "" || operator == "" {
			continue
		}

		values := make([]string, 0, len(item.Values))
		seen := make(map[string]struct{}, len(item.Values))
		for _, rawValue := range item.Values {
			value := strings.TrimSpace(rawValue)
			if value == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			values = append(values, value)
		}

		constraints = append(constraints, types.ContainerSchedulingConstraint{
			Type:     constraintType,
			Key:      key,
			Operator: operator,
			Values:   values,
		})
	}

	if len(constraints) == 0 {
		return nil
	}

	return &types.ContainerSchedulingSelector{Constraints: constraints}
}

func normalizeScalarValue(raw interface{}) (string, bool) {
	switch v := raw.(type) {
	case nil:
		return "", false
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case json.Number:
		return v.String(), true
	case int:
		return strconv.Itoa(v), true
	case int8:
		return strconv.FormatInt(int64(v), 10), true
	case int16:
		return strconv.FormatInt(int64(v), 10), true
	case int32:
		return strconv.FormatInt(int64(v), 10), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case uint:
		return strconv.FormatUint(uint64(v), 10), true
	case uint8:
		return strconv.FormatUint(uint64(v), 10), true
	case uint16:
		return strconv.FormatUint(uint64(v), 10), true
	case uint32:
		return strconv.FormatUint(uint64(v), 10), true
	case uint64:
		return strconv.FormatUint(v, 10), true
	default:
		return "", false
	}
}

// func Init() *ContainerManager {

// 	bus := event.NewMemoryBus()

// 	reg := runtime.NewRegistry()

// 	docker := &docker.DockerRuntime{}

// 	reg.Register("docker", docker)

// 	manager := &ContainerManager{
// 		repo: repository.NewContainerRepo(),
// 		reg:  reg,
// 		bus:  bus,
// 	}

// 	// Runtime → Manager
// 	docker.SetEventHandler(manager)

// 	return manager
// }
