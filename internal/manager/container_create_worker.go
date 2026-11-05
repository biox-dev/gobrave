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
	"time"

	"github.com/biox-dev/gobrave/internal/config"
	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

var (
	errWorkerInvalidState = errors.New("worker event invalid for current state")
	// errBraveEnvFileCreate marks the brave-env.sh preparation step so callers can
	// report the historical ContainerCreateBraveEnvFailed event.
	errBraveEnvFileCreate = errors.New("create brave-env.sh")
)

const (
	// OutboxEventTypeCreateRequest is the outbox event type for container creation requests.
	OutboxEventTypeCreateRequest = "ContainerCreateRequest"
	// OutboxEventTypeStopRequest is the outbox event type for container stop requests.
	OutboxEventTypeStopRequest = "ContainerStopRequest"
	// OutboxEventTypeDeleteRequest is the outbox event type for container delete requests.
	OutboxEventTypeDeleteRequest = "ContainerDeleteRequest"
	// OutboxEventTypeStartRequest is the outbox event type for container start requests.
	OutboxEventTypeRecreateRequest = "AppSessionContainerRecreateRequest"

	OutboxEventTypeStartRequest = "ContainerStartRequest"
	dockerSocketPath            = "/var/run/docker.sock"
)

// Ensure ContainerCreateWorker implements event.Handler.
var _ event.Handler = (*ContainerCreateWorker)(nil)

// containerCreatePayload is stored in OutboxEvent.Payload for deferred container creation.
// type containerCreatePayload struct {
// 	ContainerInstanceID int64 `json:"container_instance_id"`
// 	// RuntimeName         string `json:"runtime_name"`
// 	TemplateID int64  `json:"template_id"`
// 	OwnerType  string `json:"owner_type"`
// 	OwnerID    int64  `json:"owner_id"`
// 	Name       string `json:"name"`
// 	UserID     string `json:"user_id,omitempty"`
// }

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
	// img              *ImageManager
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
	// img *ImageManager,
	containerManager *ContainerManager,
	cfg *config.Config,
) *ContainerCreateWorker {

	return &ContainerCreateWorker{
		repo:            repo,
		projectRepo:     projectRepo,
		analysisRepo:    analysisRepo,
		workflowService: workflowService,
		reg:             reg,
		res:             res,
		// img:              img,
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
	case OutboxRecreateRequestEvent:
		w.handleRecreateRequest(context.Background(), e)
	}
}

// handleCreateRequest unmarshals the payload and executes creation.
// The outbox is marked sent later in handleContainerEvent when the
// corresponding container reaches a stable state.
func (w *ContainerCreateWorker) handleCreateRequest(ctx context.Context, req OutboxCreateRequestEvent) {
	var payload types.ContainerEvent
	if err := json.Unmarshal(req.RawPayload, &payload); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] unmarshal create payload failed, outbox_id=%d err=%v", req.OutboxID, err)
		_ = w.repo.MarkOutboxEventSent(ctx, req.OutboxID)
		return
	}

	logger.Infof(ctx, "[ContainerCreateWorker] handling create request, outbox_id=%d instance_id=%d",
		req.OutboxID, payload.ContainerInstanceID)

	execCtx := ctx
	// if payload.UserID != "" {
	// 	execCtx = context.WithValue(ctx, types.UserIDContextKey, payload.UserID)
	// }

	// ownerType := types.ContainerOwnerType(payload.OwnerType)
	inst, err := w.repo.GetContainerInstanceByID(ctx, payload.ContainerInstanceID)
	if err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] get container instance failed, instance_id=%d err=%v", payload.ContainerInstanceID, err)
		_ = w.repo.MarkOutboxEventFailed(ctx, req.OutboxID)
		return
	}
	if err := w.executeCreate(
		execCtx,
		// payload.RuntimeName,
		// payload.TemplateID,
		// ownerType,
		// payload.OwnerID,
		// payload.Name,
		inst,
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

func (w *ContainerCreateWorker) handleRecreateRequest(ctx context.Context, req OutboxRecreateRequestEvent) {
	var payload types.ContainerEvent
	if err := json.Unmarshal(req.RawPayload, &payload); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] unmarshal recreate payload failed, outbox_id=%d err=%v", req.OutboxID, err)
		_ = w.repo.MarkOutboxEventSent(ctx, req.OutboxID)
		return
	}

	logger.Infof(ctx, "[ContainerCreateWorker] handling recreate request, outbox_id=%d instance_id=%d",
		req.OutboxID, payload.ContainerInstanceID)

	execCtx := ctx

	if err := w.executeRecreate(execCtx, payload.ContainerInstanceID); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] execute recreate failed, instance_id=%d err=%v", payload.ContainerInstanceID, err)
		_ = w.repo.MarkOutboxEventPending(ctx, req.OutboxID)
		return
	}

	if err := w.repo.MarkOutboxEventSent(ctx, req.OutboxID); err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] mark outbox sent failed, outbox_id=%d instance_id=%d err=%v", req.OutboxID, payload.ContainerInstanceID, err)
		return
	}

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

	// if payload.UserID != "" {
	// 	execCtx = context.WithValue(ctx, types.UserIDContextKey, payload.UserID)
	// }
	execCtx := ctx

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
	// runtimeName string,
	// templateID int64,
	// ownerType types.ContainerOwnerType,
	// ownerID int64,
	// name string,
	inst *types.ContainerInstance,
) error {
	// 1. Acquire capacity and transition to creating.
	// 2. 变更ContainerInstance状态为 creating，创建 ContainerCreating 事件
	// 3. 调用 runtime.Create + runtime.Start

	if inst.Status != types.ContainerCreatePending {
		return fmt.Errorf("%w: expected=%s actual=%s instance_id=%d", errWorkerInvalidState, types.ContainerCreatePending, inst.Status, inst.ID)
	}
	// inst, err := w.acquireCapacityAndTransition(ctx, instanceID, types.ContainerCreatePending, types.ContainerCreating, "ContainerCreating")
	err := w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerCreating, "ContainerCreating")
	if err != nil {
		return err
	}

	tpl, err := w.repo.GetContainerTemplateByID(ctx, inst.TemplateID)
	if err != nil {
		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerTemplateNotFound")
		return err
	}
	if strings.TrimSpace(tpl.ImageName) == "" {
		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerImageNotConfigured")
		return fmt.Errorf("container template %d does not bind any image", inst.TemplateID)
	}

	rt, err := w.getRuntimeByInstance(inst)
	// rt, err := w.containerManager.getRuntime()
	if err != nil {
		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerRuntimeNotFound")
		return err
	}

	// if w.img != nil {
	// 	if err := w.img.EnsureImageReadyByEntity(ctx, runtimeName, img); err != nil {
	// 		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerImagePrepareFailed")
	// 		// _ = w.createContainerEvent(ctx, inst.ID, "ContainerImagePrepareFailedDetail", err.Error())
	// 		return err
	// 	}
	// }
	ownerCtx := w.loadOwnerRuntimeContext(ctx, inst.OwnerType, inst.OwnerID)
	// resolveVars := w.buildRuntimeResolveVariables(ctx, tpl, inst.OwnerType, ownerCtx)
	resolveVars := w.buildRuntimeVariables(ctx, tpl, inst.OwnerType, ownerCtx).ToMap()

	volumes := parseVolumes(tpl.Mounts, inst.OwnerType)
	projectVolumes := parseVolumes(ownerCtx.project.Mounts, inst.OwnerType)
	volumes = append(volumes, projectVolumes...)

	envs := parseEnv(tpl.Env)
	projectEnvs := parseEnv(ownerCtx.project.Env)
	for k, v := range projectEnvs {
		envs[k] = v
	}

	// volumes默认添加 cfg.Storage.BaseDir 目录的绑定，确保容器可以访问到这个目录下的文件（如Rprofile等）
	volumes, err = w.appendStorageVolumes(ctx, volumes)
	if err != nil {
		eventType := "ContainerCreatePackageDirFailed"
		if errors.Is(err, errBraveEnvFileCreate) {
			eventType = "ContainerCreateBraveEnvFailed"
		}
		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, eventType)
		return err
	}

	spec := &types.ContainerSpec{
		Image: tpl.ImageName,
		// 拉取策略随容器模板绑定行一起下发给 runtime（docker 在 create 前 pull，k8s 设 imagePullPolicy）。
		PullPolicy:           tpl.PullPolicy,
		Command:              parseCommand(tpl.Command),
		Env:                  envs,
		Volumes:              volumes,
		SchedulingConstraint: parseSchedulingConstraint(tpl.SchedulingConstraint),
		CPU:                  tpl.CPU,
		Memory:               tpl.Memory,
		WorkDir:              tpl.WorkDir,
		RuntimeName:          w.buildRuntimeResourceName(inst.OwnerType, inst.ID, inst.Name),
		ExposedPort:          tpl.Port,
		Labels: map[string]string{
			"gobrave-owner-type":  string(inst.OwnerType),
			"gobrave-owner-id":    strconv.FormatInt(inst.OwnerID, 10),
			"gobrave-instance-id": strconv.FormatInt(inst.ID, 10),
		},
	}
	if inst.OwnerType == types.ContainerOwnerAppSession {
		spec.WorkloadKind = "deployment"
		spec.ExposeService = tpl.Port > 0
	} else {
		spec.WorkloadKind = "job"
		spec.ExposeService = false
	}

	switch inst.OwnerType {
	case types.ContainerOwnerDagNode:
		w.containerManager.applyDagNodeRuntimeSpec(spec, resolveVars)
	}

	if w.res != nil {
		// w.ensureRuntimeFilesAndDirs(ctx, resolveVars)
		spec, err = w.res.Resolve(ctx, &ContainerRuntimeResolveInput{Spec: spec, Variables: resolveVars})
		if err != nil {
			_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerResolveSpecFailed")
			// _ = w.createContainerEvent(ctx, inst.ID, "ContainerResolveSpecFailedDetail", err.Error())
			return err
		}
	}

	// 创建 runtime 资源前，确保 volume source 都存在
	if err := w.ensureVolumeSources(ctx, spec.Volumes); err != nil {
		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerCreateVolumeSourceFailed")
		return err
	}

	if hasDockerSocketVolume(spec.Volumes) {
		if gid, ok := resolvePathGID(dockerSocketPath); ok {
			spec.SupplementalGroups = appendUniqueInt64(spec.SupplementalGroups, parseInt64WithDefault(gid, 0))
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

func (w *ContainerCreateWorker) executeRecreate(ctx context.Context, instanceID int64) error {
	inst, err := w.repo.GetContainerInstanceByID(ctx, instanceID)
	if err != nil {
		return err
	}

	// Transition to recreating.
	if err := w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerReCreating, "ContainerReCreating"); err != nil {
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

	return nil
}

// getRuntimeByName resolves a runtime by name from the registry.
// func (w *ContainerCreateWorker) getRuntimeByName(name string) (containerruntime.Runtime, error) {
// 	if name == "" {
// 		return nil, errors.New("runtime name is required")
// 	}
// 	rt := w.reg.Get(name)
// 	if rt == nil {
// 		return nil, fmt.Errorf("runtime not found: %s", name)
// 	}
// 	return rt, nil
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
func (w *ContainerCreateWorker) getRuntimeByInstance(inst *types.ContainerInstance) (containerruntime.Runtime, error) {
	return w.containerManager.getRuntimeByInstance(inst)

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

type ownerRuntimeContext struct {
	session   *types.AppSession
	node      *types.AnalysisNode
	project   *types.Project
	projectID int64
}

func (w *ContainerCreateWorker) loadOwnerRuntimeContext(ctx context.Context, ownerType types.ContainerOwnerType, ownerID int64) *ownerRuntimeContext {
	// project starts non-nil so callers can read mounts/envs without a nil check.
	// It is only ever replaced by a successful lookup: a failed or skipped lookup
	// (no project id, no repository) must not turn into a nil dereference when the
	// create path unconditionally reads ownerCtx.project.
	ownerCtx := &ownerRuntimeContext{project: &types.Project{}}

	switch ownerType {
	case types.ContainerOwnerAppSession:
		session, err := w.repo.GetAppSessionByID(ctx, ownerID)
		if err == nil && session != nil {
			ownerCtx.session = session
			ownerCtx.projectID = session.ProjectID

			if session.AnalysisNodeID != 0 && w.analysisRepo != nil {
				node, nodeErr := w.analysisRepo.GetAnalysisNodeByID(ctx, session.AnalysisNodeID)
				if nodeErr == nil && node != nil {
					ownerCtx.node = node
				}
			}
		}

	case types.ContainerOwnerDagNode:
		if w.analysisRepo != nil {
			node, err := w.analysisRepo.GetAnalysisNodeByID(ctx, ownerID)
			if err == nil && node != nil {
				ownerCtx.node = node
				ownerCtx.projectID = node.ProjectID

				if ownerCtx.projectID == 0 {
					analysis, analysisErr := w.analysisRepo.GetAnalysisByID(ctx, node.AnalysisID)
					if analysisErr == nil && analysis != nil {
						logger.Warn(context.Background(), "use analysis projectid for owner context")
						ownerCtx.projectID = analysis.ProjectID
					}
				}
			}
		}

	}

	if ownerCtx.projectID != 0 && w.projectRepo != nil {
		project, err := w.projectRepo.GetProjectByID(ctx, ownerCtx.projectID)
		if err == nil && project != nil {
			ownerCtx.project = project
		}
	}

	return ownerCtx
}

// resolveOwnerProjectVolumes appends project-level volumes based on the owner.
// func (w *ContainerCreateWorker) resolveOwnerProjectVolumes(ownerCtx *ownerRuntimeContext) []types.ContainerVolume {
// 	if ownerCtx == nil || ownerCtx.project == nil {
// 		return nil
// 	}

// 	return parseVolumes(ownerCtx.project.Volumes)
// }

// buildRuntimeResolveVariables 保留为薄封装：组装带元数据的变量集后派生扁平 map，供 resolver 使用。
// 变量的分类/描述等元数据见 runtime_variable.go 中的 runtimeVariableDefinitions。
// func (w *ContainerCreateWorker) buildRuntimeResolveVariables(
// 	ctx context.Context,
// 	tpl *types.ContainerTemplate,
// 	ownerType types.ContainerOwnerType,
// 	ownerCtx *ownerRuntimeContext,
// ) map[string]string {
// 	return w.buildRuntimeVariables(ctx, tpl, ownerType, ownerCtx).ToMap()
// }

// ensureRuntimeFilesAndDirs creates necessary directories and files for the runtime.
func ensureDirs(ctx context.Context, dirs []string) {
	if len(dirs) == 0 {
		return
	}
	//  []string{"R_PACKAGE_DIR", "USER_PROJECT_DIR", "WORKSPACE_PATH", "PYTHON_PACKAGE_DIR", "USER_CONFIG_DIR", "CONDA_PACKAGE_DIR"}
	for _, dir := range dirs {
		// dir := strings.TrimSpace(vars[key])
		if dir == "" {
			continue
		}
		ensureDir(ctx, dir)
	}

	// if profilePath := strings.TrimSpace(vars["R_PROFILE"]); profilePath != "" {
	// 	ensureEmptyFileIfNotExists(ctx, profilePath)
	// }
}

func ensureDir(ctx context.Context, dir string) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		logger.Warnf(ctx, "[ContainerCreateWorker] create runtime directory failed, path=%s err=%v", dir, err)
	}
}

// ensureVolumeSources creates the host-side source path for each volume:
// a file when Type is "file", otherwise a directory.
func (w *ContainerCreateWorker) ensureVolumeSources(ctx context.Context, volumes []types.ContainerVolume) error {
	for _, vol := range volumes {
		source := strings.TrimSpace(vol.Source)
		if source == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(vol.Type), "file") {
			ensureEmptyFileIfNotExists(ctx, source)
			continue
		}
		if strings.EqualFold(strings.TrimSpace(vol.Type), "dir") {
			if err := os.MkdirAll(source, 0755); err != nil {
				return err
			}
		}

	}
	return nil
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

// appendStorageVolumes adds the storage base dir bind and the shared
// brave-env.sh profile mount. Host paths are created when missing so runtime
// create does not fail. Shared by normal container creation and agent probes.
func (w *ContainerCreateWorker) appendStorageVolumes(ctx context.Context, volumes []types.ContainerVolume) ([]types.ContainerVolume, error) {
	if w.cfg == nil || w.cfg.Storage == nil {
		return volumes, nil
	}
	baseDir := strings.TrimSpace(w.cfg.Storage.BaseDir)
	if baseDir == "" {
		return volumes, nil
	}

	volumes = append(volumes, types.ContainerVolume{
		Source: baseDir,
		Target: baseDir,
		Mode:   "rw",
	})

	packageDir := filepath.Join(baseDir, "package")
	if err := os.MkdirAll(packageDir, 0755); err != nil {
		return volumes, err
	}
	braveEnvFile := filepath.Join(packageDir, "brave-env.sh")
	if _, err := os.Stat(braveEnvFile); os.IsNotExist(err) {
		if _, err := os.Create(braveEnvFile); err != nil {
			return volumes, fmt.Errorf("%w: %v", errBraveEnvFileCreate, err)
		}
	}
	volumes = append(volumes, types.ContainerVolume{
		Source: braveEnvFile,
		Target: "/etc/profile.d/brave-env.sh",
		Mode:   "rw",
	})
	return volumes, nil
}

func hasDockerSocketVolume(volumes []types.ContainerVolume) bool {
	for _, volume := range volumes {
		source := strings.TrimSpace(volume.Source)
		target := strings.TrimSpace(volume.Target)
		if source == dockerSocketPath || target == dockerSocketPath {
			return true
		}
	}
	return false
}

func appendUniqueInt64(values []int64, value int64) []int64 {
	if value <= 0 {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func parseInt64WithDefault(raw string, fallback int64) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

// ===== 容器模板终端容器（由 ContainerCreateWorker 统一管理）=====
//
// 终端容器的组装口径与 executeCreate 一致（模板镜像 / 资源 / 挂载 / 环境变量 +
// 项目 env + 包目录变量 + storage 挂载 + 变量解析），区别在于：
//   - 它有真实的 ContainerInstance 记录（owner_type=terminal），因此运行时事件
//     可以像普通容器一样经 ContainerManager.OnEvent 收敛状态；
//   - 它不由 outbox / Handle 链路驱动，而是由本 worker 直接创建实例并调用
//     runtime.Create + runtime.Start；
//   - 启动完成的判定改为等待 OnEvent 广播的 ContainerStarted（running），
//     不再需要 runtime 层的 StartForTerminal / StartAndWait。

const (
	terminalCleanupTimeout = 60 * time.Second
	terminalStartWait      = 2 * time.Minute
	terminalWorkloadKind   = "job"
	// terminalKeepAliveCommand 让终端容器常驻，真正的 shell 由 docker exec /
	// kubectl exec 打开。
	terminalKeepAliveCommand = "while true; do sleep 3600; done"
)

// terminalEnvKeys 是需要物化到终端容器环境里的运行时变量。模板 / 项目 env 优先，
// 这里只补齐缺失项，使命令可以直接使用 $R_PACKAGE_DIR / $PROJECT_DIR 等。
var terminalEnvKeys = []string{
	"USERID",
	"GROUPID",
	"DOCKER_GID",
	"DOCKER_GROUPID",
	"R_PROFILE",
	"PACKAGE_DIR",
	"R_PACKAGE_DIR",
	"PYTHON_PACKAGE_DIR",
	"CONDA_PACKAGE_DIR",
	"PROJECT_DIR",
	"PROJECT_CONFIG_DIR",
}

// TerminalRuntimeHandle 是一次「终端容器 + 交互式 shell」的句柄。
//
// 它把创建/认领两条路径统一起来：
//   - StartTerminalRuntime 新建容器后返回 runtimeID；
//   - AttachTerminalRuntime 在已有容器上重新 exec 一个 shell（进程重启后的认领）。
//
// Cleanup 负责删除容器（关闭 shell 之后调用）；调用方必须在会话 Close 时执行它。
type TerminalRuntimeHandle struct {
	Runtime             containerruntime.Runtime
	RuntimeName         string
	RuntimeID           string
	ContainerName       string
	ContainerInstanceID int64
	Session             containerruntime.TerminalSession
	Cleanup             func()
}

type terminalPlan struct {
	spec *types.ContainerSpec
	rt   containerruntime.Runtime
}

// ContainerTemplateName 返回模板的展示名与名称，供终端会话生成默认展示名。
//
// 模板不可读 / 未配置时返回空串，调用方会回退到模板 ID。
func (w *ContainerCreateWorker) ContainerTemplateName(ctx context.Context, templateID int64) (string, string) {
	if w == nil || w.repo == nil || templateID <= 0 {
		return "", ""
	}
	tpl, err := w.repo.GetContainerTemplateByID(ctx, templateID)
	if err != nil || tpl == nil {
		return "", ""
	}
	return tpl.DisplayName, tpl.Name
}

// buildTerminalPlan 按 executeCreate 的口径组装运行时 spec：
// 模板镜像 / 资源 / 挂载 / 环境变量，项目 env / 挂载，包目录变量，storage 挂载，
// 变量解析与 volume source 准备。命令本身留给调用方在解析之后再设置。
func (w *ContainerCreateWorker) buildTerminalPlan(ctx context.Context, userID, projectID, templateID int64) (*terminalPlan, error) {
	if w == nil || w.repo == nil || w.projectRepo == nil {
		return nil, fmt.Errorf("container worker is not configured")
	}
	if projectID <= 0 {
		return nil, fmt.Errorf("project id is required")
	}
	if templateID <= 0 {
		return nil, fmt.Errorf("container_template_id is required")
	}

	tpl, err := w.repo.GetContainerTemplateByID(ctx, templateID)
	if err != nil {
		return nil, fmt.Errorf("load container template %d: %w", templateID, err)
	}
	if tpl == nil {
		return nil, fmt.Errorf("container template %d not found", templateID)
	}
	if strings.TrimSpace(tpl.ImageName) == "" {
		return nil, fmt.Errorf("container template %d does not bind any image", templateID)
	}

	project, err := w.projectRepo.GetProjectByID(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("load project %d: %w", projectID, err)
	}
	if project == nil {
		return nil, fmt.Errorf("project %d not found", projectID)
	}

	if w.containerManager == nil {
		return nil, fmt.Errorf("container manager is not configured")
	}
	rt, err := w.containerManager.getRuntime()
	if err != nil {
		return nil, err
	}

	// 与 executeCreate 一致：先基于模板 / 项目构建运行时变量（含 buildPackageVariables），
	// 再用它解析 env / mounts / workdir 中的占位符。
	ownerType := types.ContainerOwnerDagNode
	ownerCtx := &ownerRuntimeContext{project: project, projectID: projectID}
	resolveVars := w.buildRuntimeVariables(ctx, tpl, ownerType, ownerCtx).ToMap()

	volumes := parseVolumes(tpl.Mounts, ownerType)
	volumes = append(volumes, parseVolumes(project.Mounts, ownerType)...)
	volumes, err = w.appendStorageVolumes(ctx, volumes)
	if err != nil {
		return nil, fmt.Errorf("prepare terminal volumes: %w", err)
	}

	envs := parseEnv(tpl.Env)
	for k, v := range parseEnv(project.Env) {
		envs[k] = v
	}
	fillTerminalEnv(envs, resolveVars)

	workDir := strings.TrimSpace(tpl.WorkDir)
	if workDir == "" {
		workDir = resolveVars["PROJECT_DIR"]
	}

	spec := &types.ContainerSpec{
		Image:                tpl.ImageName,
		PullPolicy:           tpl.PullPolicy,
		Entrypoint:           []string{"bash", "-lc"},
		Env:                  envs,
		Volumes:              volumes,
		SchedulingConstraint: parseSchedulingConstraint(tpl.SchedulingConstraint),
		CPU:                  tpl.CPU,
		Memory:               tpl.Memory,
		WorkDir:              workDir,
		RuntimeName: w.buildRuntimeResourceName(
			types.ContainerOwnerTerminal, 0, fmt.Sprintf("terminal-%d-%d", projectID, time.Now().UnixNano()),
		),
		Labels: map[string]string{
			"gobrave-terminal":    "true",
			"gobrave-project-id":  strconv.FormatInt(projectID, 10),
			"gobrave-template-id": strconv.FormatInt(templateID, 10),
		},
		// 终端容器是常驻进程：k8s 一律用 job，避免 deployment 副本被反复调度。
		WorkloadKind:  terminalWorkloadKind,
		ExposeService: false,
	}
	// 终端会话容器额外打上 user / session label，进程重启后可按 label 对账认领。
	if userID > 0 {
		spec.Labels["gobrave-user-id"] = strconv.FormatInt(userID, 10)
		spec.Labels["gobrave-terminal-session"] = fmt.Sprintf("term-%d-%d-%d", userID, projectID, templateID)
	}
	w.containerManager.applyRuntimeUser(spec, resolveVars)

	if w.res != nil {
		spec, err = w.res.Resolve(ctx, &ContainerRuntimeResolveInput{Spec: spec, Variables: resolveVars})
		if err != nil {
			return nil, fmt.Errorf("resolve terminal spec: %w", err)
		}
	}

	if err := w.ensureVolumeSources(ctx, spec.Volumes); err != nil {
		return nil, fmt.Errorf("prepare terminal volume sources: %w", err)
	}
	if hasDockerSocketVolume(spec.Volumes) {
		if gid, ok := resolvePathGID(dockerSocketPath); ok {
			spec.SupplementalGroups = appendUniqueInt64(spec.SupplementalGroups, parseInt64WithDefault(gid, 0))
		}
	}

	return &terminalPlan{spec: spec, rt: rt}, nil
}

// StartTerminalRuntime 创建终端容器、等待 ContainerStarted 事件确认启动完成，
// 然后打开交互式 shell。
func (w *ContainerCreateWorker) StartTerminalRuntime(ctx context.Context, userID, projectID, templateID int64, opts containerruntime.TerminalOptions) (*TerminalRuntimeHandle, error) {
	plan, err := w.buildTerminalPlan(ctx, userID, projectID, templateID)
	if err != nil {
		return nil, err
	}
	plan.spec.Command = []string{terminalKeepAliveCommand}

	inst := &types.ContainerInstance{
		RuntimeName: plan.rt.Name(),
		TemplateID:  templateID,
		OwnerType:   types.ContainerOwnerTerminal,
		OwnerID:     projectID,
		Name:        plan.spec.RuntimeName,
		Status:      types.ContainerCreatePending,
	}
	if err := w.createTerminalInstance(ctx, inst, plan.spec, plan.rt); err != nil {
		return nil, err
	}
	cleanup := func() { w.deleteTerminalInstance(context.Background(), inst, plan.rt) }

	waitCtx, cancel := context.WithTimeout(ctx, terminalStartWait)
	defer cancel()
	if _, err := w.containerManager.WaitForInstanceStatus(waitCtx, inst.ID, types.ContainerRunning); err != nil {
		cleanup()
		return nil, fmt.Errorf("wait terminal container started: %w", err)
	}

	terminal, ok := plan.rt.(containerruntime.RuntimeTerminal)
	if !ok {
		cleanup()
		return nil, fmt.Errorf("runtime %s does not support interactive terminals", plan.rt.Name())
	}
	session, err := terminal.OpenTerminal(ctx, inst.RuntimeID, normalizeTerminalOptions(opts))
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("open terminal session: %w", err)
	}

	return &TerminalRuntimeHandle{
		Runtime:             plan.rt,
		RuntimeName:         plan.rt.Name(),
		RuntimeID:           inst.RuntimeID,
		ContainerName:       plan.spec.RuntimeName,
		ContainerInstanceID: inst.ID,
		Session:             session,
		Cleanup:             cleanup,
	}, nil
}

// AttachTerminalRuntime 在已有容器上执行交互式 shell（等价于 kubectl/docker exec -it）。
//
// 这是进程重启后「认领」原容器的唯一方式：容器还在，但原来的 shell 已经随连接
// 结束，所以这里得到的是一个新的 bash（cwd / 环境变量不会保留，历史输出由滚动缓冲回放）。
func (w *ContainerCreateWorker) AttachTerminalRuntime(ctx context.Context, runtimeName, runtimeID string, opts containerruntime.TerminalOptions) (*TerminalRuntimeHandle, error) {
	rt := w.resolveRuntime(runtimeName)
	if rt == nil {
		return nil, fmt.Errorf("runtime %q is not registered", runtimeName)
	}
	terminal, ok := rt.(containerruntime.RuntimeTerminal)
	if !ok {
		return nil, fmt.Errorf("runtime %s does not support interactive terminals", rt.Name())
	}
	session, err := terminal.OpenTerminal(ctx, runtimeID, normalizeTerminalOptions(opts))
	if err != nil {
		return nil, fmt.Errorf("attach terminal session: %w", err)
	}
	return &TerminalRuntimeHandle{
		Runtime:     rt,
		RuntimeName: rt.Name(),
		RuntimeID:   runtimeID,
		Session:     session,
		Cleanup:     func() { w.DropTerminalRuntime(ctx, rt.Name(), runtimeID) },
	}, nil
}

// DropTerminalRuntime 删除一个（可能来自旧进程的）终端容器，失败只记日志。
func (w *ContainerCreateWorker) DropTerminalRuntime(ctx context.Context, runtimeName, runtimeID string) {
	if strings.TrimSpace(runtimeID) == "" {
		return
	}
	rt := w.resolveRuntime(runtimeName)
	if rt == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), terminalCleanupTimeout)
	defer cancel()
	if err := rt.Delete(cleanupCtx, runtimeID); err != nil {
		logger.Warnf(ctx, "[ContainerCreateWorker] delete terminal runtime failed, runtime_id=%s err=%v", runtimeID, err)
	}
	// 运行时事件（ContainerDeleted）由 OnEvent 收敛；这里兜底清理可能残留的实例记录。
	w.deleteTerminalInstanceByRuntimeID(cleanupCtx, runtimeID)
}

// createTerminalInstance 建立 ContainerInstance 记录并启动容器。
//
// 终端容器没有业务 owner，不经过 outbox / Handle 链路：由本 worker 直接创建实例、
// 调用 runtime.Create + runtime.Start；启动完成后由 ContainerManager.OnEvent 广播
// 的 ContainerStarted（running）确认。
func (w *ContainerCreateWorker) createTerminalInstance(ctx context.Context, inst *types.ContainerInstance, spec *types.ContainerSpec, rt containerruntime.Runtime) error {
	if inst == nil || rt == nil {
		return errors.New("terminal container instance is empty")
	}
	if err := w.repo.CreateContainerInstance(ctx, inst); err != nil {
		return fmt.Errorf("create terminal container instance: %w", err)
	}
	// 终端容器不占用创建并发额度，也不写 outbox（由本函数直接驱动）。
	if err := w.containerManager.transitionContainer(ctx, inst, types.ContainerCreating, "ContainerCreating", false, false); err != nil {
		_ = w.repo.DeleteContainerInstance(ctx, inst.ID)
		return err
	}

	runtimeID, err := rt.Create(ctx, spec)
	if err != nil {
		_ = w.containerManager.transitionContainer(ctx, inst, types.ContainerFailed, "ContainerCreateFailed", false, false)
		return fmt.Errorf("create terminal container: %w", err)
	}

	inst.RuntimeID = runtimeID
	if err := w.repo.UpdateContainerInstance(ctx, inst); err != nil {
		return fmt.Errorf("persist terminal runtime id: %w", err)
	}

	if err := rt.Start(ctx, runtimeID); err != nil {
		_ = w.containerManager.transitionContainer(ctx, inst, types.ContainerFailed, "ContainerStartFailed", false, false)
		return fmt.Errorf("start terminal container: %w", err)
	}
	return nil
}

// deleteTerminalInstance 删除终端容器：先按 FSM 进入删除中，再删除运行时资源与实例记录。
func (w *ContainerCreateWorker) deleteTerminalInstance(ctx context.Context, inst *types.ContainerInstance, rt containerruntime.Runtime) {
	if inst == nil || inst.ID == 0 {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), terminalCleanupTimeout)
	defer cancel()

	// 以数据库里的最新状态为准（OnEvent 可能已经写过 running / stopped 等状态）。
	if latest, err := w.repo.GetContainerInstanceByID(cleanupCtx, inst.ID); err == nil && latest != nil {
		inst = latest
	}

	// FSM 只允许 Deleting → Deleted，因此先 DeletePending 再 Deleting。
	switch inst.Status {
	case types.ContainerDeleting, types.ContainerDeleted:
	case types.ContainerDeletePending:
		if err := w.containerManager.transitionContainer(cleanupCtx, inst, types.ContainerDeleting, "ContainerDeleting", false, false); err != nil {
			logger.Warnf(ctx, "[ContainerCreateWorker] terminal instance to deleting failed, instance_id=%d err=%v", inst.ID, err)
		}
	default:
		if err := w.containerManager.transitionContainer(cleanupCtx, inst, types.ContainerDeletePending, "ContainerDeletePending", false, false); err != nil {
			logger.Warnf(ctx, "[ContainerCreateWorker] terminal instance to delete_pending failed, instance_id=%d status=%s err=%v", inst.ID, inst.Status, err)
		} else if err := w.containerManager.transitionContainer(cleanupCtx, inst, types.ContainerDeleting, "ContainerDeleting", false, false); err != nil {
			logger.Warnf(ctx, "[ContainerCreateWorker] terminal instance to deleting failed, instance_id=%d err=%v", inst.ID, err)
		}
	}

	if rt != nil && strings.TrimSpace(inst.RuntimeID) != "" {
		if err := rt.Delete(cleanupCtx, inst.RuntimeID); err != nil {
			logger.Warnf(ctx, "[ContainerCreateWorker] delete terminal runtime failed, runtime_id=%s err=%v", inst.RuntimeID, err)
		}
	}
	if err := w.repo.DeleteContainerInstance(cleanupCtx, inst.ID); err != nil {
		logger.Warnf(ctx, "[ContainerCreateWorker] delete terminal instance failed, instance_id=%d err=%v", inst.ID, err)
	}
}

func (w *ContainerCreateWorker) deleteTerminalInstanceByRuntimeID(ctx context.Context, runtimeID string) {
	if strings.TrimSpace(runtimeID) == "" {
		return
	}
	inst, err := w.repo.GetContainerInstanceByRuntimeID(ctx, runtimeID)
	if err != nil || inst == nil || inst.ID == 0 {
		return
	}
	if err := w.repo.DeleteContainerInstance(ctx, inst.ID); err != nil {
		logger.Warnf(ctx, "[ContainerCreateWorker] delete terminal instance failed, instance_id=%d err=%v", inst.ID, err)
	}
}

// resolveRuntime 优先按名字解析，其次回退到当前默认运行时。
func (w *ContainerCreateWorker) resolveRuntime(name string) containerruntime.Runtime {
	if w == nil {
		return nil
	}
	if w.reg != nil {
		if n := strings.TrimSpace(name); n != "" {
			if rt := w.reg.Get(n); rt != nil {
				return rt
			}
		}
	}
	if w.containerManager == nil {
		return nil
	}
	rt, err := w.containerManager.getRuntime()
	if err != nil {
		return nil
	}
	return rt
}

// normalizeTerminalOptions 统一交互式终端的默认值。
func normalizeTerminalOptions(opts containerruntime.TerminalOptions) containerruntime.TerminalOptions {
	if len(opts.Command) == 0 {
		opts.Command = []string{"/bin/bash", "-l"}
	}
	opts.TTY = true
	return opts
}

// fillTerminalEnv 补齐模板 / 项目未设置、但运行时已解析出来的变量。
func fillTerminalEnv(envs, vars map[string]string) {
	if envs == nil || vars == nil {
		return
	}
	for _, key := range terminalEnvKeys {
		if _, exists := envs[key]; exists {
			continue
		}
		if value := strings.TrimSpace(vars[key]); value != "" {
			envs[key] = value
		}
	}
}
