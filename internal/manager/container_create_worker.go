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

	"github.com/gobravedev/gobrave/internal/config"
	containerruntime "github.com/gobravedev/gobrave/internal/container_runtime"
	"github.com/gobravedev/gobrave/internal/event"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
)

var (
	errWorkerInvalidState = errors.New("worker event invalid for current state")
)

const (
	// OutboxEventTypeCreateRequest is the outbox event type for container creation requests.
	OutboxEventTypeCreateRequest = "ContainerCreateRequest"
	// OutboxEventTypeStopRequest is the outbox event type for container stop requests.
	OutboxEventTypeStopRequest = "ContainerStopRequest"
	// OutboxEventTypeDeleteRequest is the outbox event type for container delete requests.
	OutboxEventTypeDeleteRequest = "ContainerDeleteRequest"
	// OutboxEventTypeStartRequest is the outbox event type for container start requests.
	OutboxEventTypeStartRequest = "ContainerStartRequest"
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

// // containerStopPayload is stored in OutboxEvent.Payload for deferred container stop.
// type containerStopPayload struct {
// 	ContainerInstanceID int64 `json:"container_instance_id"`
// 	// UserID              string `json:"user_id,omitempty"`
// }

// // containerDeletePayload is stored in OutboxEvent.Payload for deferred container delete.
// type containerDeletePayload struct {
// 	ContainerInstanceID int64 `json:"container_instance_id"`
// 	// UserID              string `json:"user_id,omitempty"`
// }

// // containerStartPayload is stored in OutboxEvent.Payload for deferred container start.
// type containerStartPayload struct {
// 	ContainerInstanceID int64 `json:"container_instance_id"`
// 	// UserID              string `json:"user_id,omitempty"`
// }

// ContainerCreateWorker subscribes to OutboxCreateRequestEvent and ContainerEvent
// from the event bus. For creation requests it executes rt.Create + rt.Start,
// and updates the active create request count while the request is being handled.
type ContainerCreateWorker struct {
	containerService interfaces.ContainerService
	repo             interfaces.ContainerRepository
	projectRepo      interfaces.ProjectRepository
	analysisRepo     interfaces.AnalysisRepository
	workflowService  interfaces.WorkflowService
	reg              *containerruntime.Registry
	res              ContainerRuntimeResolver
	img              *ImageManager
	containerManager *ContainerManager
	cfg              *config.Config
	// maxConcurrency   int
}

// NewContainerCreateWorker creates a new worker.
func NewContainerCreateWorker(
	repo interfaces.ContainerRepository,
	projectRepo interfaces.ProjectRepository,
	analysisRepo interfaces.AnalysisRepository,
	workflowService interfaces.WorkflowService,
	reg *containerruntime.Registry,
	res ContainerRuntimeResolver,
	img *ImageManager,
	containerManager *ContainerManager,
	cfg *config.Config,
) *ContainerCreateWorker {

	return &ContainerCreateWorker{
		repo:             repo,
		projectRepo:      projectRepo,
		analysisRepo:     analysisRepo,
		workflowService:  workflowService,
		reg:              reg,
		res:              res,
		img:              img,
		containerManager: containerManager,
		cfg:              cfg,
		// maxConcurrency:  maxConcurrency,
	}
}

// Handle dispatches events from the event bus. It handles four event types:
//   - OutboxCreateRequestEvent: executes deferred container creation
//   - OutboxStopRequestEvent: executes deferred container stop
//   - OutboxDeleteRequestEvent: executes deferred container delete
//   - OutboxStartRequestEvent: executes deferred container start
func (w *ContainerCreateWorker) Handle(evt event.Event) {
	switch e := evt.(type) {
	case OutboxCreateRequestEvent:
		w.handleCreateRequest(context.Background(), e)

	case OutboxStopRequestEvent:
		w.handleStopRequest(context.Background(), e)

	case OutboxDeleteRequestEvent:
		w.handleDeleteRequest(context.Background(), e)

	case OutboxStartRequestEvent:
		w.handleStartRequest(context.Background(), e)
	}
}

// handleCreateRequest unmarshals the payload and executes creation.
// The outbox is marked sent later in handleContainerEvent when the
// corresponding container reaches a stable state.
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

	if err := w.executeCreate(
		execCtx,
		payload.RuntimeName,
		payload.TemplateID,
		ownerType,
		payload.OwnerID,
		payload.Name,
		payload.ContainerInstanceID,
	); err != nil {
		if errors.Is(err, errWorkerInvalidState) {
			logger.Warnf(ctx, "[ContainerCreateWorker] skip stale create request, outbox_id=%d instance_id=%d err=%v",
				req.OutboxID, payload.ContainerInstanceID, err)
			_ = w.repo.MarkOutboxEventSent(ctx, req.OutboxID)
			return
		}
		logger.Errorf(ctx, "[ContainerCreateWorker] execute create failed, instance_id=%d err=%v", payload.ContainerInstanceID, err)
		_ = w.repo.MarkOutboxEventPending(ctx, req.OutboxID)
		return
	}

	if err := w.repo.MarkOutboxEventSent(ctx, req.OutboxID); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] mark outbox sent failed, outbox_id=%d instance_id=%d err=%v",
			req.OutboxID, payload.ContainerInstanceID, err)
		return
	}
	logger.Infof(ctx, "[ContainerCreateWorker] create request completed and marked sent, outbox_id=%d instance_id=%d",
		req.OutboxID, payload.ContainerInstanceID)
}

// handleStopRequest unmarshals the payload, executes the stop, and marks the outbox as sent.
func (w *ContainerCreateWorker) handleStopRequest(ctx context.Context, req OutboxStopRequestEvent) {
	var payload types.ContainerEvent
	if err := json.Unmarshal(req.RawPayload, &payload); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] unmarshal stop payload failed, outbox_id=%d err=%v", req.OutboxID, err)
		_ = w.repo.MarkOutboxEventSent(ctx, req.OutboxID)
		return
	}

	logger.Infof(ctx, "[ContainerCreateWorker] handling stop request, outbox_id=%d instance_id=%d",
		req.OutboxID, payload.ContainerInstanceID)

	execCtx := ctx
	// if payload.UserID != "" {
	// 	execCtx = context.WithValue(ctx, types.UserIDContextKey, payload.UserID)
	// }

	if err := w.executeStop(execCtx, payload.ContainerInstanceID); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] execute stop failed, instance_id=%d err=%v", payload.ContainerInstanceID, err)
		_ = w.repo.MarkOutboxEventPending(ctx, req.OutboxID)
		return
	}

	if err := w.repo.MarkOutboxEventSent(ctx, req.OutboxID); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] mark outbox sent failed, outbox_id=%d err=%v", req.OutboxID, err)
	}
}

// handleDeleteRequest unmarshals the payload, executes the delete, and marks the outbox as sent.
func (w *ContainerCreateWorker) handleDeleteRequest(ctx context.Context, req OutboxDeleteRequestEvent) {
	var payload types.ContainerEvent
	if err := json.Unmarshal(req.RawPayload, &payload); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] unmarshal delete payload failed, outbox_id=%d err=%v", req.OutboxID, err)
		_ = w.repo.MarkOutboxEventSent(ctx, req.OutboxID)
		return
	}

	logger.Infof(ctx, "[ContainerCreateWorker] handling delete request, outbox_id=%d instance_id=%d",
		req.OutboxID, payload.ContainerInstanceID)

	execCtx := ctx
	// if payload.UserID != "" {
	// 	execCtx = context.WithValue(ctx, types.UserIDContextKey, payload.UserID)
	// }

	if err := w.executeDelete(execCtx, payload.ContainerInstanceID); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] execute delete failed, instance_id=%d err=%v", payload.ContainerInstanceID, err)
		_ = w.repo.MarkOutboxEventPending(ctx, req.OutboxID)
		return
	}

	if err := w.repo.MarkOutboxEventSent(ctx, req.OutboxID); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] mark outbox sent failed, outbox_id=%d err=%v", req.OutboxID, err)
	}
}

// 补充一个现状风险：
// 如果进程在 MarkOutboxEventProcessing 之后、MarkOutboxEventPending/Sent 之前崩溃，事件可能卡在 processing。当前 recoverStaleProcessing 还是占位实现，没有真正回收 processing：
// internal/manager/outbox_dispatcher.go#L169
// internal/manager/outbox_dispatcher.go#L177

// handleStartRequest unmarshals the payload, executes the start, and marks the outbox as sent.
func (w *ContainerCreateWorker) handleStartRequest(ctx context.Context, req OutboxStartRequestEvent) {
	var payload types.ContainerEvent
	if err := json.Unmarshal(req.RawPayload, &payload); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] unmarshal start payload failed, outbox_id=%d err=%v", req.OutboxID, err)
		_ = w.repo.MarkOutboxEventSent(ctx, req.OutboxID)
		return
	}

	logger.Infof(ctx, "[ContainerCreateWorker] handling start request, outbox_id=%d instance_id=%d",
		req.OutboxID, payload.ContainerInstanceID)

	execCtx := ctx
	// if payload.UserID != "" {
	// 	execCtx = context.WithValue(ctx, types.UserIDContextKey, payload.UserID)
	// }

	if err := w.executeStart(execCtx, payload.ContainerInstanceID); err != nil {
		if errors.Is(err, errWorkerInvalidState) {
			logger.Warnf(ctx, "[ContainerCreateWorker] skip stale start request, outbox_id=%d instance_id=%d err=%v",
				req.OutboxID, payload.ContainerInstanceID, err)
			_ = w.repo.MarkOutboxEventSent(ctx, req.OutboxID)
			return
		}
		// If the start fails, we mark the outbox event as pending so it can be retried later.
		logger.Errorf(ctx, "[ContainerCreateWorker] execute start failed, instance_id=%d err=%v", payload.ContainerInstanceID, err)
		_ = w.repo.MarkOutboxEventPending(ctx, req.OutboxID)
		return
	}

	if err := w.repo.MarkOutboxEventSent(ctx, req.OutboxID); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] mark outbox sent failed, outbox_id=%d err=%v", req.OutboxID, err)
	}
}

// executeCreate performs the actual container creation and start. It is called either
// directly by ContainerManager.CreateByTemplate (sync path) or by
// ContainerCreateWorker.handleCreateRequest (async path).
// The ContainerInstance must already exist in DB with status=pending.
func (w *ContainerCreateWorker) executeCreate(
	ctx context.Context,
	runtimeName string,
	templateID int64,
	ownerType types.ContainerOwnerType,
	ownerID int64,
	name string,
	instanceID int64,
) error {
	// 1. Acquire capacity and transition to creating.
	// 2. 变更ContainerInstance状态为 creating，创建 ContainerCreating 事件
	// 3. 调用 runtime.Create + runtime.Start
	inst, err := w.repo.GetContainerInstanceByID(ctx, instanceID)
	if err != nil {
		return err
	}
	if inst.Status != types.ContainerCreatePending {
		return fmt.Errorf("%w: expected=%s actual=%s instance_id=%d", errWorkerInvalidState, types.ContainerCreatePending, inst.Status, instanceID)
	}
	// inst, err := w.acquireCapacityAndTransition(ctx, instanceID, types.ContainerCreatePending, types.ContainerCreating, "ContainerCreating")
	err = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerCreating, "ContainerCreating")
	if err != nil {
		return err
	}

	tpl, err := w.repo.GetContainerTemplateByID(ctx, templateID)
	if err != nil {
		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerTemplateNotFound")
		return err
	}

	img, err := w.repo.GetContainerImageByID(ctx, tpl.ImageID)
	if err != nil {
		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerImageNotFound")
		return err
	}

	rt, err := w.getRuntimeByName(runtimeName)
	if err != nil {
		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerRuntimeNotFound")
		return err
	}

	if w.img != nil {
		if err := w.img.EnsureImageReadyByEntity(ctx, runtimeName, img); err != nil {
			_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerImagePrepareFailed")
			// _ = w.createContainerEvent(ctx, inst.ID, "ContainerImagePrepareFailedDetail", err.Error())
			return err
		}
	}

	volumes := parseVolumes(tpl.Volumes)
	volumes = append(volumes, w.resolveOwnerProjectVolumes(ctx, ownerType, ownerID)...)

	// volumes默认添加 cfg.Storage.BaseDir 目录的绑定，确保容器可以访问到这个目录下的文件（如Rprofile等）
	if w.cfg != nil && w.cfg.Storage != nil && w.cfg.Storage.BaseDir != "" {
		volumes = append(volumes, types.ContainerVolume{
			Source: w.cfg.Storage.BaseDir,
			Target: w.cfg.Storage.BaseDir,
			Mode:   "rw",
		})
		// 添加 挂载点 $PACKAGE_DIR/brave-env.sh ，如果文件不存在则先创建
		packageDir := filepath.Join(w.cfg.Storage.BaseDir, "package")
		if err := os.MkdirAll(packageDir, 0755); err != nil {
			_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerCreatePackageDirFailed")
			return err
		}
		braveEnvFile := filepath.Join(packageDir, "brave-env.sh")
		if _, err := os.Stat(braveEnvFile); os.IsNotExist(err) {
			if _, err := os.Create(braveEnvFile); err != nil {
				_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerCreateBraveEnvFailed")
				return err
			}
		}
		volumes = append(volumes, types.ContainerVolume{
			Source: braveEnvFile,
			Target: "/etc/profile.d/brave-env.sh",
			Mode:   "rw",
		})
	}

	spec := &types.ContainerSpec{
		Image:                img.FullName,
		Command:              parseCommand(tpl.Command),
		Env:                  parseEnv(tpl.Env),
		Volumes:              volumes,
		SchedulingConstraint: parseSchedulingConstraint(tpl.SchedulingConstraint),
		CPU:                  tpl.CPU,
		Memory:               tpl.Memory,
		WorkDir:              tpl.WorkDir,
		RuntimeName:          w.buildRuntimeResourceName(ownerType, inst.ID, name),
		ExposedPort:          tpl.Port,
		Labels: map[string]string{
			"gobrave-owner-type":  string(ownerType),
			"gobrave-owner-id":    strconv.FormatInt(ownerID, 10),
			"gobrave-instance-id": strconv.FormatInt(inst.ID, 10),
		},
	}
	if ownerType == types.ContainerOwnerAppSession {
		spec.WorkloadKind = "deployment"
		spec.ExposeService = tpl.Port > 0
	} else {
		spec.WorkloadKind = "job"
		spec.ExposeService = false
	}

	resolveVars := w.buildRuntimeResolveVariables(ctx, w.cfg, img, templateID, ownerType, ownerID, name)
	if ownerType == types.ContainerOwnerDagNode {
		applyDagNodeRuntimeSpec(spec, resolveVars, runtimeName)
	}

	if w.res != nil {
		w.ensureRuntimeFilesAndDirs(ctx, resolveVars)
		spec, err = w.res.Resolve(ctx, &ContainerRuntimeResolveInput{Spec: spec, Variables: resolveVars})
		if err != nil {
			_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerResolveSpecFailed")
			// _ = w.createContainerEvent(ctx, inst.ID, "ContainerResolveSpecFailedDetail", err.Error())
			return err
		}
	}

	runtimeID, err := rt.Create(ctx, spec)
	if err != nil {
		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerCreateFailed")
		// _ = w.createContainerEvent(ctx, inst.ID, "ContainerCreateFailedDetail", err.Error())
		return err
	}

	inst.RuntimeID = runtimeID
	if err := w.repo.UpdateContainerInstance(ctx, inst); err != nil {
		return err
	}

	if err := rt.Start(ctx, runtimeID); err != nil {
		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerStartFailed")
		// _ = w.createContainerEvent(ctx, inst.ID, "ContainerStartFailedDetail", err.Error())
		return err
	}

	return nil
}

// getRuntimeByName resolves a runtime by name from the registry.
func (w *ContainerCreateWorker) getRuntimeByName(name string) (containerruntime.Runtime, error) {
	if name == "" {
		return nil, errors.New("runtime name is required")
	}
	rt := w.reg.Get(name)
	if rt == nil {
		return nil, fmt.Errorf("runtime not found: %s", name)
	}
	return rt, nil
}

// transition performs an FSM state transition for a container instance within a transaction.
// func (w *ContainerCreateWorker) transition(
// 	ctx context.Context,
// 	inst *types.ContainerInstance,
// 	to types.ContainerStatus,
// 	eventType string,
// ) error {
// 	if inst == nil {
// 		return errors.New("container instance is nil")
// 	}

// 	return w.repo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
// 		latest, err := tx.GetContainerInstanceByID(ctx, inst.ID)
// 		if err != nil {
// 			return err
// 		}

// 		if latest.Status == to {
// 			inst.Status = latest.Status
// 			return nil
// 		}

// 		f := &fsm.FSM{}
// 		if err := f.Transition(latest.Status, to); err != nil {
// 			return err
// 		}

// 		inst.Status = to
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

// createContainerEvent creates a simple container event record.
// func (w *ContainerCreateWorker) createContainerEvent(ctx context.Context, instanceID int64, evt string, msg string) error {
// 	return w.repo.CreateContainerEvent(ctx, &types.ContainerEvent{
// 		ContainerInstanceID: instanceID,
// 		Event:               evt,
// 		Message:             msg,
// 	})
// }

// executeStop performs the actual container stop. It is called by
// ContainerCreateWorker.handleStopRequest (async path).
func (w *ContainerCreateWorker) executeStop(ctx context.Context, instanceID int64) error {
	inst, err := w.repo.GetContainerInstanceByID(ctx, instanceID)
	if err != nil {
		return err
	}

	// If already in a terminal state, nothing to do.
	switch inst.Status {
	case types.ContainerStopped, types.ContainerFailed, types.ContainerExited:
		return nil
	}

	rt, err := w.getRuntimeByInstance(inst)
	if err != nil {
		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerStopFailed")
		// _ = w.createContainerEvent(ctx, inst.ID, "ContainerStopFailedDetail", err.Error())
		return err
	}

	// Transition to stopping.
	if err := w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerStopping, "ContainerStopping"); err != nil {
		return err
	}

	// Execute runtime stop.
	if err := rt.Stop(ctx, inst.RuntimeID); err != nil {
		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerStopFailed")
		// _ = w.createContainerEvent(ctx, inst.ID, "ContainerStopFailedDetail", err.Error())
		return err
	}

	// 状态变更为 stopped 由 runtime event 触发，见 kubernetes_executor.go#L103
	// func (m *ContainerManager) OnEvent(e containerruntime.RuntimeEvent)

	// Transition to stopped.
	// now := time.Now()
	// inst.FinishedAt = &now
	// if err := w.transition(ctx, inst, fsm.Stopped, "ContainerStopped"); err != nil {
	// 	return err
	// }
	return nil
}

// executeStart performs the actual container start. It is called by
// ContainerCreateWorker.handleStartRequest (async path).
func (w *ContainerCreateWorker) executeStart(ctx context.Context, instanceID int64) error {
	// 1. Acquire capacity and transition to starting.
	// 2. 变更ContainerInstance状态为 starting，创建 ContainerStarting 事件
	// 3. 调用 runtime.Start
	inst, err := w.repo.GetContainerInstanceByID(ctx, instanceID)
	if err != nil {
		return err
	}
	if inst.Status != types.ContainerStartPending {
		return fmt.Errorf("%w: expected=%s actual=%s instance_id=%d", errWorkerInvalidState, types.ContainerStartPending, inst.Status, instanceID)
	}
	err = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerStarting, "ContainerStarting")
	if err != nil {
		return err
	}

	// inst, err := w.acquireCapacityAndTransition(ctx, instanceID, types.ContainerStartPending, types.ContainerStarting, "ContainerStarting")
	// if err != nil {
	// 	return err
	// }

	rt, err := w.getRuntimeByInstance(inst)
	if err != nil {
		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerStartFailed")
		// _ = w.createContainerEvent(ctx, inst.ID, "ContainerStartFailedDetail", err.Error())
		return err
	}

	if err := rt.Start(ctx, inst.RuntimeID); err != nil {
		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerStartFailed")
		// _ = w.createContainerEvent(ctx, inst.ID, "ContainerStartFailedDetail", err.Error())
		return err
	}

	return nil
}

// executeDelete performs the actual container delete. It is called by
// ContainerCreateWorker.handleDeleteRequest (async path).
func (w *ContainerCreateWorker) executeDelete(ctx context.Context, instanceID int64) error {
	inst, err := w.repo.GetContainerInstanceByID(ctx, instanceID)
	if err != nil {
		return err
	}

	// Transition to deleting.
	if err := w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerDeleting, "ContainerDeleting"); err != nil {
		return err
	}

	// Resolve runtime and delete the runtime resource.
	rt, rtErr := w.getRuntimeByInstance(inst)
	if rtErr == nil && inst.RuntimeID != "" {
		if err := rt.Delete(ctx, inst.RuntimeID); err != nil {
			logger.Errorf(ctx, "[ContainerCreateWorker] runtime delete failed, instance_id=%d err=%v", instanceID, err)
			// Continue with DB cleanup even if runtime delete fails.
		}
	}

	// Transition to stopped, then delete the DB record.
	// _ = w.transition(ctx, inst, fsm.Stopped, "ContainerDeleted")
	// _ = w.createContainerEvent(ctx, inst.ID, "ContainerDeleted", "container deleted")

	// 删除ContainerInstance由 runtime event 触发，见 kubernetes_executor.go#L103
	// func (m *ContainerManager) OnEvent(e containerruntime.RuntimeEvent)

	// if err := w.repo.DeleteContainerInstance(ctx, inst.ID); err != nil {
	// 	return err
	// }

	return nil
}

// getRuntimeByInstance resolves a runtime from a container instance's RuntimeID.
func (w *ContainerCreateWorker) getRuntimeByInstance(inst *types.ContainerInstance) (containerruntime.Runtime, error) {
	if inst == nil {
		return nil, errors.New("container instance is nil")
	}

	for _, item := range w.reg.List() {
		if strings.HasPrefix(inst.RuntimeID, item.Name()+"-") {
			return item, nil
		}
	}

	items := w.reg.List()
	if len(items) == 1 {
		return items[0], nil
	}

	return nil, fmt.Errorf("failed to resolve runtime for instance %d", inst.ID)
}

// func (w *ContainerCreateWorker) acquireCapacityAndTransition(
// 	ctx context.Context,
// 	instanceID int64,
// 	from types.ContainerStatus,
// 	to types.ContainerStatus,
// 	eventType string,
// ) (*types.ContainerInstance, error) {
// 	var updated *types.ContainerInstance
// 	err := w.repo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
// 		latest, err := tx.GetContainerInstanceByID(ctx, instanceID)
// 		if err != nil {
// 			return err
// 		}

// 		if latest.Status != from {
// 			return fmt.Errorf("%w: expected=%s actual=%s instance_id=%d", errWorkerInvalidState, from, latest.Status, instanceID)
// 		}

// 		if w.maxConcurrency > 0 {
// 			active, err := tx.CountContainerInstanceByStatuses(ctx, concurrencyOccupiedStatuses())
// 			if err != nil {
// 				return err
// 			}
// 			if active >= int64(w.maxConcurrency) {
// 				return fmt.Errorf("%w: active=%d max=%d", errWorkerConcurrencyLimit, active, w.maxConcurrency)
// 			}
// 		}

// 		f := &fsm.FSM{}
// 		if err := f.Transition(from, to); err != nil {
// 			return err
// 		}

// 		latest.Status = to
// 		if err := tx.UpdateContainerInstance(ctx, latest); err != nil {
// 			return err
// 		}

// 		updated = latest

// 		domainEvent := &types.ContainerEvent{
// 			ContainerInstanceID: latest.ID,
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
// 	if err != nil {
// 		return nil, err
// 	}
// 	return updated, nil
// }

// func isConcurrencyOccupiedStatus(status types.ContainerStatus) bool {
// 	switch status {
// 	case types.ContainerCreating, types.ContainerRunning, types.ContainerStarting, types.ContainerStopping:
// 		return true
// 	default:
// 		return false
// 	}
// }

// func concurrencyOccupiedStatuses() []types.ContainerStatus {
// 	return []types.ContainerStatus{
// 		types.ContainerCreating,
// 		types.ContainerRunning,
// 		types.ContainerStarting,
// 		types.ContainerStopping,
// 		types.ContainerStopPending,
// 	}
// }

// resolveOwnerProjectVolumes appends project-level volumes based on the owner.
func (w *ContainerCreateWorker) resolveOwnerProjectVolumes(ctx context.Context, ownerType types.ContainerOwnerType, ownerID int64) []types.ContainerVolume {
	if w.projectRepo == nil {
		return nil
	}

	projectID := w.resolveProjectIDByOwner(ctx, ownerType, ownerID)
	if projectID == 0 {
		return nil
	}

	project, err := w.projectRepo.GetProjectByID(ctx, projectID)
	if err != nil || project == nil {
		return nil
	}

	return parseVolumes(project.Volumes)
}

// resolveProjectIDByOwner resolves a project ID from an owner type and ID.
func (w *ContainerCreateWorker) resolveProjectIDByOwner(ctx context.Context, ownerType types.ContainerOwnerType, ownerID int64) int64 {
	switch ownerType {
	case types.ContainerOwnerAppSession:
		session, err := w.repo.GetAppSessionByID(ctx, ownerID)
		if err != nil || session == nil {
			return 0
		}
		return session.ProjectID
	case types.ContainerOwnerDagNode:
		if w.analysisRepo == nil {
			return 0
		}
		node, err := w.analysisRepo.GetAnalysisNodeByID(ctx, ownerID)
		if err != nil || node == nil {
			return 0
		}

		if node.ProjectID != 0 {
			return node.ProjectID
		}
		analysis, err := w.analysisRepo.GetAnalysisByID(ctx, node.AnalysisID)
		if err != nil || analysis == nil {
			return 0
		}
		logger.Warn(context.Background(), "use analysis projectid for resolveProjectIDByOwner")
		return analysis.ProjectID
	default:
		return 0
	}
}

// buildRuntimeResolveVariables builds the variable map used by the runtime resolver.
func (w *ContainerCreateWorker) buildRuntimeResolveVariables(
	ctx context.Context,
	cfg *config.Config,
	img *types.ContainerImage,
	templateID int64,
	ownerType types.ContainerOwnerType,
	ownerID int64,
	name string,
) map[string]string {
	vars := map[string]string{}
	baseDir := ""
	if cfg != nil && cfg.Storage != nil {
		baseDir = strings.TrimSpace(cfg.Storage.BaseDir)
	}

	setRuntimeVar(vars, "CONTAINER_TEMPLATE_ID", strconv.FormatInt(templateID, 10))
	setRuntimeVar(vars, "TEMPLATE_ID", strconv.FormatInt(templateID, 10))
	setRuntimeVar(vars, "OWNER_TYPE", string(ownerType))
	setRuntimeVar(vars, "OWNER_ID", strconv.FormatInt(ownerID, 10))
	setRuntimeVar(vars, "CONTAINER_NAME", name)

	if baseDir != "" {
		packageDir := fmt.Sprintf("%s/package", baseDir)
		profilePath := fmt.Sprintf("%s/Rprofile", packageDir)
		ensureEmptyFileIfNotExists(ctx, profilePath)
		setRuntimeVar(vars, "R_PROFILE", profilePath)
		setRuntimeVar(vars, "PACKAGE_DIR", packageDir)

		rPackageDir := fmt.Sprintf("%s/package/R/%s", baseDir, img.LibraryVersion)
		setRuntimeVar(vars, "R_PACKAGE_DIR", rPackageDir)
	}

	if userID, ok := os.LookupEnv("USERID"); ok {
		setRuntimeVar(vars, "USERID", userID)
	} else {
		setRuntimeVar(vars, "USERID", strconv.Itoa(os.Getuid()))
	}
	if groupID, ok := os.LookupEnv("GROUPID"); ok {
		setRuntimeVar(vars, "GROUPID", groupID)
	} else {
		setRuntimeVar(vars, "GROUPID", strconv.Itoa(os.Getgid()))
	}

	if dockerGID, ok := os.LookupEnv("DOCKER_GID"); ok {
		setRuntimeVar(vars, "DOCKER_GID", dockerGID)
	} else if gid, ok := resolvePathGID("/var/run/docker.sock"); ok {
		setRuntimeVar(vars, "DOCKER_GID", gid)
	} else {
		setRuntimeVar(vars, "DOCKER_GID", vars["GROUPID"])
	}

	if ctx != nil {
		if userID, ok := ctx.Value(types.UserIDContextKey).(string); ok {
			setRuntimeVar(vars, "SYS_USER_ID", userID)
		}
		if userID, ok := ctx.Value(types.UserIDContextKey.String()).(string); ok {
			setRuntimeVar(vars, "SYS_USER_ID", userID)
		}
	}

	if ownerType == types.ContainerOwnerAppSession && ownerID > 0 {
		if session, err := w.repo.GetAppSessionByID(ctx, ownerID); err == nil && session != nil {
			setRuntimeVar(vars, "APP_SESSION_ID", strconv.FormatInt(session.ID, 10))
			setRuntimeVar(vars, "APPSESSION_ID", strconv.FormatInt(session.ID, 10))
			setRuntimeVar(vars, "SYS_USER_ID", session.UserID)
			setRuntimeVar(vars, "PROJECT_ID", strconv.FormatInt(session.ProjectID, 10))
			user_project_dir := fmt.Sprintf("%s/data/%d", baseDir, session.ProjectID)
			if baseDir != "" {
				setRuntimeVar(vars, "USER_PROJECT_DIR", user_project_dir)
			}

			setRuntimeVar(vars, "PROJECTID", strconv.FormatInt(session.ProjectID, 10))
			setRuntimeVar(vars, "WORKSPACE_PATH", session.WorkspacePath)
			if session.WorkspacePath == "" {
				setRuntimeVar(vars, "WORKSPACE_PATH", user_project_dir)
			}

			analysisNodeID := session.AnalysisNodeID
			if analysisNodeID != 0 {
				analysisNode, err := w.analysisRepo.GetAnalysisNodeByID(ctx, analysisNodeID)
				if err == nil && analysisNode != nil {
					if w.workflowService != nil {
						scriptDir, mainFile, err := w.workflowService.GetScriptFileByScriptID(ctx, analysisNode.ScriptID)
						if err == nil && strings.TrimSpace(mainFile) != "" && strings.TrimSpace(scriptDir) != "" {
							scriptFile := filepath.Join(scriptDir, mainFile)
							setRuntimeVar(vars, "SCRIPT_FILE", scriptFile)
						}
					}
				}
			}

			setRuntimeVar(vars, "APP_TYPE", session.AppType)
		}
	}

	if ownerType == types.ContainerOwnerDagNode && ownerID > 0 && w.analysisRepo != nil {
		if node, err := w.analysisRepo.GetAnalysisNodeByID(ctx, ownerID); err == nil && node != nil {
			setRuntimeVar(vars, "ANALYSIS_NODE_ID", strconv.FormatUint(uint64(node.ID), 10))
			setRuntimeVar(vars, "ANALYSIS_ID", strconv.FormatInt(node.AnalysisID, 10))
			setRuntimeVar(vars, "NODE_ID", node.NodeID)
			setRuntimeVar(vars, "WORKSPACE_PATH", node.WorkspaceDir)
			setRuntimeVar(vars, "WORKSPACE_DIR", node.WorkspaceDir)
			setRuntimeVar(vars, "OUTPUT_DIR", node.OutputDir)
			setRuntimeVar(vars, "COMMAND_PATH", node.CommandPath)
			setRuntimeVar(vars, "LOG_PATH", node.LogPath)

			if strings.TrimSpace(node.LogPath) == "" {
				if outputDir := strings.TrimSpace(node.OutputDir); outputDir != "" {
					setRuntimeVar(vars, "LOG_PATH", filepath.Join(outputDir, "run.log"))
				}
			}
		}
	}

	return vars
}

// ensureRuntimeFilesAndDirs creates necessary directories and files for the runtime.
func (w *ContainerCreateWorker) ensureRuntimeFilesAndDirs(ctx context.Context, vars map[string]string) {
	if len(vars) == 0 {
		return
	}

	for _, key := range []string{"R_PACKAGE_DIR", "USER_PROJECT_DIR", "WORKSPACE_PATH"} {
		dir := strings.TrimSpace(vars[key])
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			logger.Warnf(ctx, "[ContainerCreateWorker] create runtime directory failed, key=%s path=%s err=%v", key, dir, err)
		}
	}

	if profilePath := strings.TrimSpace(vars["R_PROFILE"]); profilePath != "" {
		ensureEmptyFileIfNotExists(ctx, profilePath)
	}
}

// buildRuntimeResourceName builds a sanitized resource name for the container runtime.
func (w *ContainerCreateWorker) buildRuntimeResourceName(ownerType types.ContainerOwnerType, instanceID int64, fallbackName string) string {
	prefix := "workload"
	switch ownerType {
	case types.ContainerOwnerAppSession:
		prefix = "app-session"
	case types.ContainerOwnerDagNode:
		prefix = "dag-node"
	case types.ContainerOwnerService:
		prefix = "service"
	}

	name := strings.TrimSpace(fallbackName)
	if name != "" {
		name = sanitizeKubernetesResourceName(name)
	}
	if name == "" {
		name = prefix
	}

	if instanceID > 0 {
		return fmt.Sprintf("%s-%d", name, instanceID)
	}
	return name
}
