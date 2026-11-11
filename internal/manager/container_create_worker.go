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
	// lifecycle 收敛状态迁移 / 运行时解析 / spec 组装等共享能力，
	// 与 ContainerManager 共用同一实例（装配阶段由 Manager.Lifecycle() 注入）。
	lifecycle *ContainerLifecycle
	// owner 是 StartTerminalRuntime 需要的创建入口（CreateByTemplateWithOptions）。
	// 构造时为空，由 ContainerManager.SetCreateWorker 回填，避免构造期循环依赖。
	owner *ContainerManager
	cfg   *config.Config
	// paths 是跨平台路径语义的唯一入口（宿主 / 容器 / daemon）。
	// 由 storage.base_dir 构建，变量拼接、落盘、volume 归一都经它换算。
	paths *containerruntime.PathMapper
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
	lifecycle *ContainerLifecycle,
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
		lifecycle: lifecycle,
		cfg:       cfg,
		paths:     newPathMapper(cfg),
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
	if err := w.executeCreateInstance(execCtx, inst); err != nil {
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

// executeCreateInstance 是「按实例创建并启动容器」的统一实现。
//
// 调用方（outbox 异步链路 / ContainerManager 的同步创建 / 终端会话容器）都已把
// ContainerInstance 落库为 create_pending，并把它传进来；这里只负责：
//   - 状态迁移 create_pending → creating；
//   - 按容器运行形态（inst.Kind）调整启动命令 / workload / 运行用户；
//   - 组装运行时 spec（模板 / 项目 env、挂载、变量解析、storage 挂载）；
//   - runtime.Create + runtime.Start。
//
// 启动命令、workload 类型、运行用户等差异全部来自 inst.Kind。
func (w *ContainerCreateWorker) executeCreateInstance(ctx context.Context, inst *types.ContainerInstance) error {
	if inst == nil || inst.ID == 0 {
		return errors.New("container instance is required")
	}
	// 运行形态决定后续所有差异，未显式设置时按 OwnerType 兜底并回写实例。
	inst.Kind = inst.NormalizedKind()
	if err := w.transitionToCreating(ctx, inst); err != nil {
		return err
	}

	tpl, err := w.repo.GetContainerTemplateByID(ctx, inst.TemplateID)
	if err != nil {
		_ = w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerFailed, "ContainerTemplateNotFound")
		return err
	}
	if strings.TrimSpace(tpl.ImageName) == "" {
		_ = w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerFailed, "ContainerImageNotConfigured")
		return fmt.Errorf("container template %d does not bind any image", inst.TemplateID)
	}

	rt, err := w.getRuntimeByInstance(inst)
	// rt, err := w.lifecycle.GetRuntime()
	if err != nil {
		_ = w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerFailed, "ContainerRuntimeNotFound")
		return err
	}

	// if w.img != nil {
	// 	if err := w.img.EnsureImageReadyByEntity(ctx, runtimeName, img); err != nil {
	// 		_ = w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerFailed, "ContainerImagePrepareFailed")
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
		_ = w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerFailed, eventType)
		return err
	}

	labels := map[string]string{
		"gobrave-owner-type":    string(inst.OwnerType),
		"gobrave-owner-id":      strconv.FormatInt(inst.OwnerID, 10),
		"gobrave-instance-id":   strconv.FormatInt(inst.ID, 10),
		"gobrave-instance-kind": string(inst.Kind),
	}

	spec := w.buildInstanceSpec(inst, tpl, resolveVars, envs, volumes, labels)

	if w.res != nil {
		// w.ensureRuntimeFilesAndDirs(ctx, resolveVars)
		spec, err = w.res.Resolve(ctx, &ContainerRuntimeResolveInput{Spec: spec, Variables: resolveVars})
		if err != nil {
			_ = w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerFailed, "ContainerResolveSpecFailed")
			// _ = w.createContainerEvent(ctx, inst.ID, "ContainerResolveSpecFailedDetail", err.Error())
			return err
		}
	}

	// 容器工作目录统一取项目的 PROJECT_DIR：容器模板不再提供 work_dir。
	// 任务型容器（DAG 节点）已在 applyTaskRuntimeSpec 里按节点工作区 WORKSPACE_PATH 覆盖，
	// 这里只补齐仍未设置的情况（应用会话 / 终端容器等），不会把 run.sh 所在的工作区替掉。
	// 因此它必须在 resolver 之后执行。
	if strings.TrimSpace(spec.WorkDir) == "" {
		spec.WorkDir = strings.TrimSpace(resolveVars["PROJECT_DIR"])
	}

	// 变量展开后的 bind source 可能仍是容器 POSIX 路径（Windows 上丢了盘符），
	// 必须还原成宿主路径，落盘与 runtime.Create 才能找到源。
	w.paths.NormalizeVolumes(spec.Volumes)

	// 创建 runtime 资源前，确保 volume source 都存在
	if err := w.ensureVolumeSources(ctx, spec.Volumes); err != nil {
		_ = w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerFailed, "ContainerCreateVolumeSourceFailed")
		return err
	}

	if hasDockerSocketVolume(spec.Volumes) {
		if gid, ok := resolvePathGID(dockerSocketPath); ok {
			spec.SupplementalGroups = appendUniqueInt64(spec.SupplementalGroups, parseInt64WithDefault(gid, 0))
		}
	}

	runtimeID, err := rt.Create(ctx, spec)
	if err != nil {
		_ = w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerFailed, "ContainerCreateFailed")
		// _ = w.createContainerEvent(ctx, inst.ID, "ContainerCreateFailedDetail", err.Error())
		return err
	}

	inst.RuntimeID = runtimeID
	if err := w.repo.UpdateContainerInstance(ctx, inst); err != nil {
		return err
	}

	if err := rt.Start(ctx, runtimeID); err != nil {
		_ = w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerFailed, "ContainerStartFailed")
		// _ = w.createContainerEvent(ctx, inst.ID, "ContainerStartFailedDetail", err.Error())
		return err
	}

	return nil
}

// transitionToCreating 把实例迁移到 creating。
//
// 容器运行形态只在 OneShot 时计入创建并发额度：terminal / web 容器长期占用 running，
// 若计入额度，几次终端会话就会把创建队列占满。
func (w *ContainerCreateWorker) transitionToCreating(ctx context.Context, inst *types.ContainerInstance) error {
	enforceConcurrency := containerKindIsOneShot(inst.Kind)
	return w.lifecycle.Transition(ctx, inst, types.ContainerCreating, "ContainerCreating", enforceConcurrency, true)
}

// containerKindIsOneShot 表示该形态的容器跑完即退出（DAG 节点任务容器）。
func containerKindIsOneShot(kind types.ContainerInstanceKind) bool {
	return kind == types.ContainerInstanceKindDagNode
}

// buildInstanceSpec 按容器运行形态组装运行时 spec。
//
// 三种形态的差异全部收敛在这里，调用的 create 链路（outbox 异步 / 同步）只负责
// 落库、状态迁移与 runtime.Create + Start：
//   - web      ：跑模板命令，deployment + 暴露模板端口，供网关转发；
//   - terminal ：/bin/sh -c keep-alive 常驻，job 不暴露端口，真正的 shell 由 exec 打开；
//   - dag_node ：bash ./run.sh 并 tee 到 LOG_PATH，job 不暴露端口，以宿主用户运行。
func (w *ContainerCreateWorker) buildInstanceSpec(
	inst *types.ContainerInstance,
	tpl *types.ContainerTemplate,
	resolveVars map[string]string,
	envs map[string]string,
	volumes []types.ContainerVolume,
	labels map[string]string,
) *types.ContainerSpec {
	spec := &types.ContainerSpec{
		Image: tpl.ImageName,
		// 拉取策略随容器模板绑定行一起下发给 runtime（docker 在 create 前 pull，k8s 设 imagePullPolicy）。
		PullPolicy:           tpl.PullPolicy,
		Entrypoint:           nil,
		Command:              parseCommand(tpl.Command),
		Env:                  envs,
		Volumes:              volumes,
		SchedulingConstraint: parseSchedulingConstraint(tpl.SchedulingConstraint),
		CPU:                  tpl.CPU,
		Memory:               tpl.Memory,
		RuntimeName:          w.buildRuntimeResourceName(inst.OwnerType, inst.ID, inst.Name),
		ExposedPort:          tpl.Port,
		Labels:               labels,
	}

	switch inst.Kind {
	case types.ContainerInstanceKindTerminal:
		// shell 语句必须交给 shell 执行：k8s 把 Entrypoint 下发为 command、
		// Command 下发为 args；docker 下发为 Entrypoint + Cmd。
		// 直接当成可执行文件会报
		// `exec: "while true; do sleep 3600; done": executable file not found in $PATH`。
		spec.Entrypoint = terminalShellEntrypoint()
		spec.Command = terminalShellArgs(terminalKeepAliveCommand)
		spec.WorkloadKind = "job"
		spec.ExposeService = false
		// 终端容器以宿主用户启动：容器内写出的文件在宿主上可直接读写，
		// 无需在 shell 里再 chown（同时挂上属主为该用户的 HOME）。
		w.lifecycle.ApplyRuntimeUser(spec, resolveVars)

	case types.ContainerInstanceKindDagNode:
		spec.WorkloadKind = "job"
		spec.ExposeService = false
		// 任务容器统一入口 bash ./run.sh，并按节点工作区 / LOG_PATH 覆盖工作目录与用户。
		w.lifecycle.ApplyDagNodeRuntimeSpec(spec, resolveVars)

	default:
		// web：跑模板命令并通过模板端口暴露给网关。
		spec.WorkloadKind = "deployment"
		spec.ExposeService = tpl.Port > 0
	}

	return spec
}

func (w *ContainerCreateWorker) executeRecreate(ctx context.Context, instanceID int64) error {
	inst, err := w.repo.GetContainerInstanceByID(ctx, instanceID)
	if err != nil {
		return err
	}

	// Transition to recreating.
	if err := w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerReCreating, "ContainerReCreating"); err != nil {
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
		_ = w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerFailed, "ContainerStopFailed")
		// _ = w.createContainerEvent(ctx, inst.ID, "ContainerStopFailedDetail", err.Error())
		return err
	}

	// Transition to stopping.
	if err := w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerStopping, "ContainerStopping"); err != nil {
		return err
	}

	// Execute runtime stop.
	if err := rt.Stop(ctx, inst.RuntimeID); err != nil {
		_ = w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerFailed, "ContainerStopFailed")
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
	err = w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerStarting, "ContainerStarting")
	if err != nil {
		return err
	}

	// inst, err := w.acquireCapacityAndTransition(ctx, instanceID, types.ContainerStartPending, types.ContainerStarting, "ContainerStarting")
	// if err != nil {
	// 	return err
	// }

	rt, err := w.getRuntimeByInstance(inst)
	if err != nil {
		_ = w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerFailed, "ContainerStartFailed")
		// _ = w.createContainerEvent(ctx, inst.ID, "ContainerStartFailedDetail", err.Error())
		return err
	}

	if err := rt.Start(ctx, inst.RuntimeID); err != nil {
		_ = w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerFailed, "ContainerStartFailed")
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
	if err := w.lifecycle.TransitionAndEnqueue(ctx, inst, types.ContainerDeleting, "ContainerDeleting"); err != nil {
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
	return w.lifecycle.GetRuntimeByInstance(inst)

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

// newPathMapper 是 manager 包构建路径映射器的唯一入口。
//
// storage.base_dir 决定宿主的盘符锚点；manager 不涉及 daemon 路径语义，wslDaemon 恒为 false。
func newPathMapper(cfg *config.Config) *containerruntime.PathMapper {
	return containerruntime.NewPathMapper(config.StorageBaseDir(cfg), false)
}

// ensureDirs 把运行时变量里的目录（容器路径）落到宿主。
func (w *ContainerCreateWorker) ensureDirs(ctx context.Context, dirs []string) {
	for _, dir := range dirs {
		if strings.TrimSpace(dir) == "" {
			continue
		}
		if err := os.MkdirAll(w.paths.Host(dir), 0755); err != nil {
			logger.Warnf(ctx, "[ContainerCreateWorker] create runtime directory failed, path=%s err=%v", dir, err)
		}
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
		switch {
		case strings.EqualFold(strings.TrimSpace(vol.Type), "file"):
			ensureEmptyFileIfNotExists(ctx, w.paths, source)
		case strings.EqualFold(strings.TrimSpace(vol.Type), "dir"):
			if err := os.MkdirAll(w.paths.Host(source), 0755); err != nil {
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
		Source: w.paths.Host(baseDir),
		Target: w.paths.Container(baseDir),
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
		Type:   "file",
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

// ===== 容器模板终端容器（复用统一的创建链路）=====
//
// 终端会话容器不再单独维护一套 spec 组装 / 创建启动逻辑：
//   - 会话实体就是 types.AppSession（go_container_app_session），
//     容器是 owner_type=app_session、owner_id=AppSession.ID、kind=terminal 的普通 ContainerInstance；
//   - 创建走 ContainerManager.CreateByTemplateWithOptions(Sync=true, Kind=terminal) →
//     ContainerCreateWorker.executeCreateInstance，与 DAG / 应用会话完全同一条链路；
//   - 唯一的差异是 buildInstanceSpec 里按 Kind=terminal 组装的启动命令
//     （terminalKeepAliveCommand 常驻），真正的交互式 shell 由 docker exec /
//     kubectl exec 打开。

const (
	terminalCleanupTimeout = 60 * time.Second
	terminalStartWait      = 2 * time.Minute
	// terminalKeepAliveCommand 让终端容器常驻，真正的 shell 由 docker exec /
	// kubectl exec 打开。
	//
	// 注意：它是 shell 语句，必须交给 shell 执行（见 terminalShellEntrypoint /
	// terminalShellArgs），不能作为单个参数直接当可执行文件 exec。
	terminalKeepAliveCommand = "while true; do sleep 3600; done"
)

// terminalShellEntrypoint / terminalShellArgs 把 shell 语句包装成
// 「entrypoint + args」：k8s 的 command=entrypoint、docker 的 Entrypoint。
//
// 否则 runtime 会把整条语句当成可执行文件名：
// runc create failed: unable to start container process: exec:
// "while true; do sleep 3600; done": executable file not found in $PATH
func terminalShellEntrypoint() []string { return []string{"/bin/sh"} }

func terminalShellArgs(script string) []string { return []string{"-c", script} }

// TerminalRuntimeHandle 是一次「终端容器 + 交互式 shell」的句柄。
//
// 它把创建/认领两条路径统一起来：
//   - StartTerminalRuntime 同步创建容器（keep-alive 常驻）后打开 shell；
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

// StartTerminalRuntime 创建终端容器并打开交互式 shell。
//
// 容器由 ContainerManager.CreateByTemplateWithOptions(Sync=true, Kind=terminal) 同步
// 创建并启动（复用 ContainerCreateWorker.executeCreateInstance，模板 / 项目 env、挂载、
// 变量解析、storage 挂载与普通容器完全一致）。启动命令 / workload / 运行用户等
// 终端容器特有的差异全部由 buildInstanceSpec 按 Kind=terminal 处理。
//
// sessionID 是终端会话（AppSession）主键：容器以 owner_type=app_session +
// owner_id=sessionID 归属该会话，进程重启后可按 owner 认领原容器。
func (w *ContainerCreateWorker) StartTerminalRuntime(ctx context.Context, userID, projectID, templateID, sessionID int64, opts containerruntime.TerminalOptions) (*TerminalRuntimeHandle, error) {
	if w == nil || w.owner == nil {
		return nil, fmt.Errorf("container worker is not configured")
	}
	if sessionID == 0 {
		// 0 表示「尚未分配」；落库前的新会话使用进程内负数占位 ID，同样可用。
		return nil, fmt.Errorf("terminal session id is required")
	}

	inst, err := w.owner.CreateByTemplateWithOptions(ctx, ContainerCreateOptions{
		TemplateID:  templateID,
		OwnerType:   types.ContainerOwnerAppSession,
		OwnerID:     sessionID,
		Name:        fmt.Sprintf("terminal-%d-%d", projectID, sessionID),
		Kind:        types.ContainerInstanceKindTerminal,
		Sync:        true,
		WaitTimeout: terminalStartWait,
	})
	if err != nil {
		return nil, err
	}

	rt := w.resolveRuntime(inst.RuntimeName)
	if rt == nil {
		w.dropTerminalInstance(ctx, inst)
		return nil, fmt.Errorf("runtime %q is not registered", inst.RuntimeName)
	}
	terminal, ok := rt.(containerruntime.RuntimeTerminal)
	if !ok {
		w.dropTerminalInstance(ctx, inst)
		return nil, fmt.Errorf("runtime %s does not support interactive terminals", rt.Name())
	}
	session, err := terminal.OpenTerminal(ctx, inst.RuntimeID, normalizeTerminalOptions(opts))
	if err != nil {
		w.dropTerminalInstance(ctx, inst)
		return nil, fmt.Errorf("open terminal session: %w", err)
	}

	handle := &TerminalRuntimeHandle{
		Runtime:             rt,
		RuntimeName:         rt.Name(),
		RuntimeID:           inst.RuntimeID,
		ContainerName:       inst.Name,
		ContainerInstanceID: inst.ID,
		Session:             session,
	}
	handle.Cleanup = func() { w.dropTerminalInstance(context.Background(), inst) }
	return handle, nil
}

// AttachTerminalRuntime 在已有容器上执行交互式 shell（等价于 kubectl/docker exec -it）。
//
// 这是进程重启后「认领」原容器的唯一方式：容器还在，但原来的 shell 已经随连接
// 结束，所以这里得到的是一个新的 bash（cwd / 环境变量不会保留，历史输出由滚动缓冲回放）。
//
// instance 是 ContainerInstance 记录：RuntimeName / RuntimeID 由它提供，
// Cleanup 也会按 owner 关系把实例与容器一并回收。
func (w *ContainerCreateWorker) AttachTerminalRuntime(ctx context.Context, instance *types.ContainerInstance, opts containerruntime.TerminalOptions) (*TerminalRuntimeHandle, error) {
	if instance == nil || strings.TrimSpace(instance.RuntimeID) == "" {
		return nil, fmt.Errorf("container instance with runtime id is required")
	}
	rt := w.resolveRuntime(instance.RuntimeName)
	if rt == nil {
		return nil, fmt.Errorf("runtime %q is not registered", instance.RuntimeName)
	}
	terminal, ok := rt.(containerruntime.RuntimeTerminal)
	if !ok {
		return nil, fmt.Errorf("runtime %s does not support interactive terminals", rt.Name())
	}
	session, err := terminal.OpenTerminal(ctx, instance.RuntimeID, normalizeTerminalOptions(opts))
	if err != nil {
		return nil, fmt.Errorf("attach terminal session: %w", err)
	}
	handle := &TerminalRuntimeHandle{
		Runtime:             rt,
		RuntimeName:         rt.Name(),
		RuntimeID:           instance.RuntimeID,
		ContainerName:       instance.Name,
		ContainerInstanceID: instance.ID,
		Session:             session,
	}
	handle.Cleanup = func() { w.dropTerminalInstance(context.Background(), instance) }
	return handle, nil
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

// dropTerminalInstance 删除终端容器：先按 FSM 进入删除中，再删除运行时资源与实例记录。
func (w *ContainerCreateWorker) dropTerminalInstance(ctx context.Context, inst *types.ContainerInstance) {
	if inst == nil || inst.ID == 0 {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), terminalCleanupTimeout)
	defer cancel()

	// 以数据库里的最新状态为准（OnEvent 可能已经写过 running / stopped 等状态）。
	if latest, err := w.repo.GetContainerInstanceByID(cleanupCtx, inst.ID); err == nil && latest != nil {
		inst = latest
	}

	rt, rtErr := w.getRuntimeByInstance(inst)
	if rtErr != nil {
		rt = nil
	}

	// FSM 只允许 Deleting → Deleted，因此先 DeletePending 再 Deleting。
	switch inst.Status {
	case types.ContainerDeleting, types.ContainerDeleted:
	case types.ContainerDeletePending:
		if err := w.lifecycle.Transition(cleanupCtx, inst, types.ContainerDeleting, "ContainerDeleting", false, false); err != nil {
			logger.Warnf(ctx, "[ContainerCreateWorker] terminal instance to deleting failed, instance_id=%d err=%v", inst.ID, err)
		}
	default:
		if err := w.lifecycle.Transition(cleanupCtx, inst, types.ContainerDeletePending, "ContainerDeletePending", false, false); err != nil {
			logger.Warnf(ctx, "[ContainerCreateWorker] terminal instance to delete_pending failed, instance_id=%d status=%s err=%v", inst.ID, inst.Status, err)
		} else if err := w.lifecycle.Transition(cleanupCtx, inst, types.ContainerDeleting, "ContainerDeleting", false, false); err != nil {
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
	if w.lifecycle == nil {
		return nil
	}
	rt, err := w.lifecycle.GetRuntime()
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
