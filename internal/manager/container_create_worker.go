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
	"sync/atomic"
	"time"

	"github.com/gobravedev/gobrave/internal/config"
	containerruntime "github.com/gobravedev/gobrave/internal/container_runtime"
	"github.com/gobravedev/gobrave/internal/event"
	"github.com/gobravedev/gobrave/internal/fsm"
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
	repo            interfaces.ContainerRepository
	projectRepo     interfaces.ProjectRepository
	analysisRepo    interfaces.AnalysisRepository
	workflowService interfaces.WorkflowService
	reg             *containerruntime.Registry
	res             ContainerRuntimeResolver
	img             *ImageManager
	cfg             *config.Config
	maxConcurrency  int
	maxPending      int
	startTimeout    time.Duration
	sem             chan struct{}
	tracking        sync.Map // instanceID → chan struct{}
	activeCount     atomic.Int64
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
	cfg *config.Config,
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
		repo:            repo,
		projectRepo:     projectRepo,
		analysisRepo:    analysisRepo,
		workflowService: workflowService,
		reg:             reg,
		res:             res,
		img:             img,
		cfg:             cfg,
		maxConcurrency:  maxConcurrency,
		maxPending:      maxPending,
		startTimeout:    5 * time.Minute,
		sem:             make(chan struct{}, maxConcurrency),
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

	if err := w.executeCreate(
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
	inst, err := w.repo.GetContainerInstanceByID(ctx, instanceID)
	if err != nil {
		return err
	}

	tpl, err := w.repo.GetContainerTemplateByID(ctx, templateID)
	if err != nil {
		_ = w.transition(ctx, inst, fsm.Failed, "ContainerTemplateNotFound")
		return err
	}

	img, err := w.repo.GetContainerImageByID(ctx, tpl.ImageID)
	if err != nil {
		_ = w.transition(ctx, inst, fsm.Failed, "ContainerImageNotFound")
		return err
	}

	rt, err := w.getRuntimeByName(runtimeName)
	if err != nil {
		_ = w.transition(ctx, inst, fsm.Failed, "ContainerRuntimeNotFound")
		return err
	}

	if w.img != nil {
		if err := w.img.EnsureImageReadyByEntity(ctx, runtimeName, img); err != nil {
			_ = w.transition(ctx, inst, fsm.Failed, "ContainerImagePrepareFailed")
			_ = w.createContainerEvent(ctx, inst.ID, "ContainerImagePrepareFailedDetail", err.Error())
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
			_ = w.transition(ctx, inst, fsm.Failed, "ContainerCreatePackageDirFailed")
			return err
		}
		braveEnvFile := filepath.Join(packageDir, "brave-env.sh")
		if _, err := os.Stat(braveEnvFile); os.IsNotExist(err) {
			if _, err := os.Create(braveEnvFile); err != nil {
				_ = w.transition(ctx, inst, fsm.Failed, "ContainerCreateBraveEnvFailed")
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
			_ = w.transition(ctx, inst, fsm.Failed, "ContainerResolveSpecFailed")
			_ = w.createContainerEvent(ctx, inst.ID, "ContainerResolveSpecFailedDetail", err.Error())
			return err
		}
	}

	runtimeID, err := rt.Create(ctx, spec)
	if err != nil {
		_ = w.transition(ctx, inst, fsm.Failed, "ContainerCreateFailed")
		_ = w.createContainerEvent(ctx, inst.ID, "ContainerCreateFailedDetail", err.Error())
		return err
	}

	inst.RuntimeID = runtimeID
	if err := w.repo.UpdateContainerInstance(ctx, inst); err != nil {
		return err
	}
	if err := w.transition(ctx, inst, fsm.Creating, "ContainerCreating"); err != nil {
		return err
	}

	if err := rt.Start(ctx, runtimeID); err != nil {
		_ = w.transition(ctx, inst, fsm.Failed, "ContainerStartFailed")
		_ = w.createContainerEvent(ctx, inst.ID, "ContainerStartFailedDetail", err.Error())
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
func (w *ContainerCreateWorker) transition(
	ctx context.Context,
	inst *types.ContainerInstance,
	to fsm.State,
	eventType string,
) error {
	if inst == nil {
		return errors.New("container instance is nil")
	}

	return w.repo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
		latest, err := tx.GetContainerInstanceByID(ctx, inst.ID)
		if err != nil {
			return err
		}

		if latest.Status == types.ContainerStatus(to) {
			inst.Status = latest.Status
			return nil
		}

		f := &fsm.FSM{}
		if err := f.Transition(fsm.State(latest.Status), to); err != nil {
			return err
		}

		inst.Status = types.ContainerStatus(to)
		if err := tx.UpdateContainerInstance(ctx, inst); err != nil {
			return err
		}

		domainEvent := &types.ContainerEvent{
			ContainerInstanceID: inst.ID,
			Event:               eventType,
			Message:             string(to),
		}
		if err := tx.CreateContainerEvent(ctx, domainEvent); err != nil {
			return err
		}

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

// createContainerEvent creates a simple container event record.
func (w *ContainerCreateWorker) createContainerEvent(ctx context.Context, instanceID int64, evt string, msg string) error {
	return w.repo.CreateContainerEvent(ctx, &types.ContainerEvent{
		ContainerInstanceID: instanceID,
		Event:               evt,
		Message:             msg,
	})
}

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
