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

	logger.Infof(ctx, "[ContainerCreateWorker] handling create request, outbox_id=%d instance_id=%d name=%s",
		req.OutboxID, payload.ContainerInstanceID)

	execCtx := ctx
	// if payload.UserID != "" {
	// 	execCtx = context.WithValue(ctx, types.UserIDContextKey, payload.UserID)
	// }

	// ownerType := types.ContainerOwnerType(payload.OwnerType)
	inst, err := w.repo.GetContainerInstanceByID(ctx, payload.ContainerInstanceID)
	if err != nil {
		logger.Errorf(ctx, "[ContainerCreateWorker] get container instance failed, instance_id=%d err=%v", payload.ContainerInstanceID, err)
		_ = w.repo.MarkOutboxEventPending(ctx, req.OutboxID)
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

	img, err := w.repo.GetContainerImageByID(ctx, tpl.ImageID)
	if err != nil {
		_ = w.containerManager.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerImageNotFound")
		return err
	}

	// rt, err := w.getRuntimeByName(runtimeName)
	rt, err := w.containerManager.getRuntime()
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
	resolveVars := w.buildRuntimeResolveVariables(ctx, tpl, w.cfg, inst.TemplateID, inst.OwnerType, inst.OwnerID, inst.Name, ownerCtx)

	volumes := parseVolumes(tpl.Volumes, inst.OwnerType)
	projectVolumes := parseVolumes(ownerCtx.project.Volumes, inst.OwnerType)
	volumes = append(volumes, projectVolumes...)

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

	if inst.OwnerType == types.ContainerOwnerDagNode {
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

type ownerRuntimeContext struct {
	session   *types.AppSession
	node      *types.AnalysisNode
	project   *types.Project
	projectID int64
}

func (w *ContainerCreateWorker) loadOwnerRuntimeContext(ctx context.Context, ownerType types.ContainerOwnerType, ownerID int64) *ownerRuntimeContext {
	ownerCtx := &ownerRuntimeContext{}

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

// buildRuntimeResolveVariables builds the variable map used by the runtime resolver.
func (w *ContainerCreateWorker) buildRuntimeResolveVariables(
	ctx context.Context,
	tpl *types.ContainerTemplate,
	cfg *config.Config,
	// img *types.ContainerImage,
	templateID int64,
	ownerType types.ContainerOwnerType,
	ownerID int64,
	name string,
	ownerCtx *ownerRuntimeContext,
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

	packageDir := fmt.Sprintf("%s/package", baseDir)
	profilePath := fmt.Sprintf("%s/Rprofile", packageDir)
	ensureEmptyFileIfNotExists(ctx, profilePath)
	setRuntimeVar(vars, "R_PROFILE", profilePath)
	setRuntimeVar(vars, "PACKAGE_DIR", packageDir)

	rPackageDir := fmt.Sprintf("%s/package/R/%s", baseDir, tpl.GetRLibraryPath())
	setRuntimeVar(vars, "R_PACKAGE_DIR", rPackageDir)
	pythonPackageDir := fmt.Sprintf("%s/package/python/%s", baseDir, tpl.GetPythonLibraryPath())
	setRuntimeVar(vars, "PYTHON_PACKAGE_DIR", pythonPackageDir)
	condaPackageDir := fmt.Sprintf("%s/package/conda/%s", baseDir, tpl.GetCondaLibraryPath())
	setRuntimeVar(vars, "CONDA_PACKAGE_DIR", condaPackageDir)

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
		setRuntimeVar(vars, "DOCKER_GROUPID", dockerGID)
	} else if gid, ok := resolvePathGID("/var/run/docker.sock"); ok {
		setRuntimeVar(vars, "DOCKER_GID", gid)
		setRuntimeVar(vars, "DOCKER_GROUPID", gid)
	} else {
		setRuntimeVar(vars, "DOCKER_GID", vars["GROUPID"])
		setRuntimeVar(vars, "DOCKER_GROUPID", vars["GROUPID"])
	}

	// if ctx != nil {
	// 	// if userID, ok := ctx.Value(types.UserIDContextKey).(string); ok {
	// 	// 	setRuntimeVar(vars, "SYS_USER_ID", userID)
	// 	// }
	// 	// if userID, ok := ctx.Value(types.UserIDContextKey.String()).(string); ok {
	// 	// 	setRuntimeVar(vars, "SYS_USER_ID", userID)
	// 	// }

	// }
	projectDir := fmt.Sprintf("%s/data/%s", baseDir, ownerCtx.project.ProjectID)
	setRuntimeVar(vars, "PROJECT_DIR", projectDir)
	projectConfigDir := fmt.Sprintf("%s/data/%s/.config", baseDir, ownerCtx.project.ProjectID)
	setRuntimeVar(vars, "PROJECT_CONFIG_DIR", projectConfigDir)

	// if ownerCtx != nil {
	// 	if ownerCtx.projectID != 0 {
	// 		setRuntimeVar(vars, "PROJECT_ID", strconv.FormatInt(ownerCtx.projectID, 10))
	// 		// setRuntimeVar(vars, "PROJECTID", strconv.FormatInt(ownerCtx.projectID, 10))

	// 		if baseDir != "" {
	// 			setRuntimeVar(vars, "USER_PROJECT_DIR", fmt.Sprintf("%s/data/%d", baseDir, ownerCtx.projectID))
	// 		}
	// 	}

	// 	if ownerCtx.project != nil {
	// 		setRuntimeVar(vars, "PROJECT_NAME", ownerCtx.project.ProjectName)
	// 	}
	// }
	ensureEmptyFileIfNotExists(ctx, profilePath)
	ensureDirs(ctx, []string{rPackageDir, pythonPackageDir, condaPackageDir, projectDir, projectConfigDir})

	if ownerType == types.ContainerOwnerAppSession && ownerCtx != nil && ownerCtx.session != nil {
		session := ownerCtx.session
		setRuntimeVar(vars, "APP_SESSION_ID", strconv.FormatInt(session.ID, 10))
		setRuntimeVar(vars, "APPSESSION_ID", strconv.FormatInt(session.ID, 10))
		setRuntimeVar(vars, "SYS_USER_ID", session.UserID)
		setRuntimeVar(vars, "WORKSPACE_PATH", session.WorkspacePath)
		userDir := fmt.Sprintf("%s/user/%s", baseDir, session.UserID)
		setRuntimeVar(vars, "USER_DIR", userDir)
		userConfigDir := fmt.Sprintf("%s/user/%s/.config", baseDir, session.UserID)
		setRuntimeVar(vars, "USER_CONFIG_DIR", userConfigDir)

		if session.WorkspacePath == "" && ownerCtx.projectID != 0 && baseDir != "" {
			session.WorkspacePath = fmt.Sprintf("%s/data/%s", baseDir, ownerCtx.project.ProjectID)

			setRuntimeVar(vars, "WORKSPACE_PATH", session.WorkspacePath)
		}

		if ownerCtx.node != nil && w.workflowService != nil {
			scriptDir, mainFile, err := w.workflowService.GetScriptFileByScriptID(ctx, ownerCtx.node.ScriptID)
			if err == nil && strings.TrimSpace(mainFile) != "" && strings.TrimSpace(scriptDir) != "" {
				scriptFile := filepath.Join(scriptDir, mainFile)
				setRuntimeVar(vars, "SCRIPT_FILE", scriptFile)
			}
		}

		setRuntimeVar(vars, "APP_TYPE", session.AppType)
		ensureDirs(ctx, []string{session.WorkspacePath, userDir, userConfigDir})

	}

	if ownerType == types.ContainerOwnerDagNode && ownerCtx != nil && ownerCtx.node != nil {
		node := ownerCtx.node
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
		ensureDirs(ctx, []string{node.WorkspaceDir, node.OutputDir})

	}

	return vars
}

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
