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

	"github.com/gobravedev/gobrave/internal/config"
	containerruntime "github.com/gobravedev/gobrave/internal/container_runtime"
	"github.com/gobravedev/gobrave/internal/event"
	"github.com/gobravedev/gobrave/internal/fsm"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

type ContainerManager struct {
	repo            interfaces.ContainerRepository
	projectRepo     interfaces.ProjectRepository
	analysisRepo    interfaces.AnalysisRepository
	workflowService interfaces.WorkflowService
	reg             *containerruntime.Registry
	bus             event.Bus
	res             ContainerRuntimeResolver
	img             *ImageManager
	cfg             *config.Config
	createWorker    *ContainerCreateWorker
	monitorOnce     sync.Once
}

func NewContainerManager(
	repo interfaces.ContainerRepository,
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
		img = NewImageManager(repo, reg)
	}
	return &ContainerManager{repo: repo, projectRepo: projectRepo, analysisRepo: analysisRepo, workflowService: workflowService, reg: reg, bus: bus, res: res, img: img, cfg: cfg}
}

// SetCreateWorker attaches a ContainerCreateWorker to enable queue-based creation.
// When set and the queue is enabled via config, CreateByTemplate will enqueue requests
// instead of executing them synchronously.
func (m *ContainerManager) SetCreateWorker(w *ContainerCreateWorker) {
	m.createWorker = w
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
	runtimeName string,
	templateID int64,
	ownerType types.ContainerOwnerType,
	ownerID int64,
	name string,
) (*types.ContainerInstance, error) {
	runtimeName = m.resolveRuntimeName(runtimeName)

	tpl, err := m.repo.GetContainerTemplateByID(ctx, templateID)
	if err != nil {
		return nil, err
	}

	_, err = m.repo.GetContainerImageByID(ctx, tpl.ImageID)
	if err != nil {
		return nil, err
	}

	_, err = m.getRuntimeByName(runtimeName)
	if err != nil {
		return nil, err
	}

	inst := &types.ContainerInstance{
		TemplateID: templateID,
		OwnerType:  ownerType,
		OwnerID:    ownerID,
		Name:       name,
		Status:     types.ContainerPending,
	}
	if err := m.repo.CreateContainerInstance(ctx, inst); err != nil {
		return nil, err
	}
	_ = m.createContainerEvent(ctx, inst.ID, "ContainerPending", "container instance created")

	// Always enqueue the create request through the worker.
	userID := ""
	if ctx != nil {
		if uid, ok := ctx.Value(types.UserIDContextKey).(string); ok {
			userID = uid
		} else if uid, ok := ctx.Value(types.UserIDContextKey.String()).(string); ok {
			userID = uid
		}
	}

	req := containerCreatePayload{
		ContainerInstanceID: inst.ID,
		RuntimeName:         runtimeName,
		TemplateID:          templateID,
		OwnerType:           string(ownerType),
		OwnerID:             ownerID,
		Name:                name,
		UserID:              userID,
	}

	if err := m.createWorker.Enqueue(ctx, req); err != nil {
		// Queue full or other error — clean up the instance we just created.
		_ = m.repo.DeleteContainerInstance(ctx, inst.ID)
		return nil, err
	}

	logger.Infof(ctx, "[ContainerManager] enqueued create request, instance_id=%d name=%s", inst.ID, name)
	return inst, nil
}

func (m *ContainerManager) Start(ctx context.Context, id int64) error {
	inst, rt, err := m.getInstanceAndRuntime(ctx, id)
	if err != nil {
		return err
	}

	if err := rt.Start(ctx, inst.RuntimeID); err != nil {
		_ = m.transition(ctx, inst, fsm.Failed, "ContainerStartFailed")
		_ = m.createContainerEvent(ctx, inst.ID, "ContainerStartFailedDetail", err.Error())
		return err
	}
	return nil
}

// func (m *ContainerManager) Stop(ctx context.Context, id uint64) error {

// 	inst := m.repo.Get(id)

// 	// FSM check
// 	_ = m.fsm.Transition(inst.Status, Stopped)

// 	err := m.runtime.Stop(ctx, inst.RuntimeID)
// 	if err != nil {
// 		_ = m.transition(ctx, inst, Failed, "ContainerFailed")
// 		return err
// 	}

// 	return m.transition(ctx, inst, Stopped, "ContainerStopped")
// }

func (m *ContainerManager) Stop(ctx context.Context, id int64) error {
	inst, _, err := m.getInstanceAndRuntime(ctx, id)
	if err != nil {
		return err
	}

	// If already in a terminal state, nothing to do.
	switch strings.TrimSpace(strings.ToLower(string(inst.Status))) {
	case string(types.ContainerStopped), string(types.ContainerFailed), string(types.ContainerExited):
		return nil
	}

	// Always enqueue the stop request through the worker.
	// Transition to stop_pending.
	if err := m.transition(ctx, inst, fsm.StopPending, "ContainerStopPending"); err != nil {
		return err
	}

	userID := ""
	if ctx != nil {
		if uid, ok := ctx.Value(types.UserIDContextKey).(string); ok {
			userID = uid
		} else if uid, ok := ctx.Value(types.UserIDContextKey.String()).(string); ok {
			userID = uid
		}
	}

	req := containerStopPayload{
		ContainerInstanceID: inst.ID,
		UserID:              userID,
	}

	if err := m.createWorker.EnqueueStop(ctx, req); err != nil {
		logger.Errorf(ctx, "[ContainerManager] enqueue stop failed, instance_id=%d err=%v", inst.ID, err)
		return err
	}

	logger.Infof(ctx, "[ContainerManager] enqueued stop request, instance_id=%d", inst.ID)
	return nil
}

func (m *ContainerManager) StopByOwner(ctx context.Context, ownerType types.ContainerOwnerType, ownerID int64) error {
	inst, err := m.repo.GetContainerInstanceByOwner(ctx, ownerType, ownerID)
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
		string(types.ContainerStopPending), string(types.ContainerStopping):
		return nil
	}
	return m.Stop(ctx, inst.ID)
}

func (m *ContainerManager) Delete(ctx context.Context, id int64) error {
	inst, rt, err := m.getInstanceAndRuntime(ctx, id)
	if err != nil {
		return err
	}
	if inst.RuntimeID != "" {
		if err := rt.Delete(ctx, inst.RuntimeID); err != nil {
			logger.Error(context.Background(), err.Error())
			// return err
		}
	}

	_ = m.createContainerEvent(ctx, inst.ID, "ContainerDeleted", "container deleted")
	return m.repo.DeleteContainerInstance(ctx, inst.ID)
}

func (m *ContainerManager) Restart(ctx context.Context, id int64) error {
	if err := m.Stop(ctx, id); err != nil {
		return err
	}
	if err := m.Start(ctx, id); err != nil {
		return err
	}
	_ = m.createContainerEvent(ctx, id, "ContainerRestarted", "container restarted")
	return nil
}

func (m *ContainerManager) Pause(ctx context.Context, id int64) error {
	inst, rt, err := m.getInstanceAndRuntime(ctx, id)
	if err != nil {
		return err
	}

	if err := rt.Pause(ctx, inst.RuntimeID); err != nil {
		_ = m.transition(ctx, inst, fsm.Failed, "ContainerPauseFailed")
		_ = m.createContainerEvent(ctx, id, "ContainerPauseFailedDetail", err.Error())
		return err
	}

	return m.transition(ctx, inst, fsm.Paused, "ContainerPaused")
}

func (m *ContainerManager) Resume(ctx context.Context, id int64) error {
	inst, rt, err := m.getInstanceAndRuntime(ctx, id)
	if err != nil {
		return err
	}

	if err := rt.Resume(ctx, inst.RuntimeID); err != nil {
		_ = m.transition(ctx, inst, fsm.Failed, "ContainerResumeFailed")
		_ = m.createContainerEvent(ctx, id, "ContainerResumeFailedDetail", err.Error())
		return err
	}

	return m.transition(ctx, inst, fsm.Running, "ContainerResumed")
}

func (m *ContainerManager) GetLogs(ctx context.Context, id int64, tail int) (string, error) {
	if tail <= 0 {
		tail = 200
	}

	inst, rt, err := m.getInstanceAndRuntime(ctx, id)
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
	inst, err := m.repo.GetContainerInstanceByRuntimeID(context.Background(), e.RuntimeID)
	if err != nil {
		logger.Warnf(context.Background(), "[ContainerManager] OnEvent: GetContainerInstanceByRuntimeID failed, runtime_id=%s event=%s err=%v", e.RuntimeID, e.Type, err)
		return
	}

	// 防御性检查：如果收到终端事件但容器状态仍为 Creating，
	// 说明 ContainerStarted 事件可能丢失，先补偿 started 时间戳再处理。
	currentStatus := strings.ToLower(strings.TrimSpace(string(inst.Status)))
	isCreating := currentStatus == string(types.ContainerCreating)
	isTerminalEvent := e.Type == "ContainerExited" || e.Type == "ContainerFailed"
	if isCreating && isTerminalEvent {
		logger.Warnf(context.Background(), "[ContainerManager] received terminal event while container still creating, runtime_id=%s event=%s status=%s, compensating started_at", e.RuntimeID, e.Type, inst.Status)
		now := time.Now()
		inst.StartedAt = &now
		inst.FinishedAt = nil
		if rt, rtErr := m.getRuntimeByInstance(inst); rtErr == nil {
			m.syncInstanceIPAddress(context.Background(), rt, inst)
		}
		// 先尝试转 Running，失败也无妨（FSM 现在允许 Creating→Stopped）
		_ = m.transition(context.Background(), inst, fsm.Running, "ContainerStarted")
	}

	switch e.Type {

	case "ContainerStarted":
		now := time.Now()
		inst.StartedAt = &now
		inst.FinishedAt = nil
		if rt, rtErr := m.getRuntimeByInstance(inst); rtErr == nil {
			m.syncInstanceIPAddress(context.Background(), rt, inst)
		}
		_ = m.transition(context.Background(), inst, fsm.Running, "ContainerStarted")

	case "ContainerPaused":
		_ = m.transition(context.Background(), inst, fsm.Paused, "ContainerPaused")

	case "ContainerResumed":
		now := time.Now()
		inst.StartedAt = &now
		inst.FinishedAt = nil
		if rt, rtErr := m.getRuntimeByInstance(inst); rtErr == nil {
			m.syncInstanceIPAddress(context.Background(), rt, inst)
		}
		_ = m.transition(context.Background(), inst, fsm.Running, "ContainerResumed")

	case "ContainerExited":
		now := time.Now()
		inst.FinishedAt = &now
		if code, ok := parseRuntimeExitCode(e.Message); ok {
			inst.ExitCode = &code
		}
		_ = m.transition(context.Background(), inst, fsm.Stopped, "ContainerStopped")

	case "ContainerFailed":
		now := time.Now()
		inst.FinishedAt = &now
		if code, ok := parseRuntimeExitCode(e.Message); ok {
			inst.ExitCode = &code
		}
		_ = m.transition(context.Background(), inst, fsm.Failed, "ContainerFailed")

	case "ContainerDeleted":
		now := time.Now()
		inst.FinishedAt = &now
		_ = m.transition(context.Background(), inst, fsm.Stopped, "ContainerStopped")
		_ = m.createContainerEvent(context.Background(), inst.ID, "ContainerDeleted", e.Message)

	default:
		_ = m.createContainerEvent(context.Background(), inst.ID, e.Type, e.Message)
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

func (m *ContainerManager) transition(
	ctx context.Context,
	inst *types.ContainerInstance,
	to fsm.State,
	eventType string,
) error {
	if inst == nil {
		return errors.New("container instance is nil")
	}

	return m.repo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
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

func (m *ContainerManager) getRuntimeByName(name string) (containerruntime.Runtime, error) {
	if name == "" {
		return nil, errors.New("runtime name is required")
	}
	rt := m.reg.Get(name)
	if rt == nil {
		return nil, fmt.Errorf("runtime not found: %s", name)
	}
	return rt, nil
}

func (m *ContainerManager) resolveRuntimeName(runtimeName string) string {
	runtimeName = strings.TrimSpace(runtimeName)
	if runtimeName != "" {
		normalized := strings.ToLower(runtimeName)
		switch normalized {
		case "kubernetes":
			normalized = "k8s"
		}

		if m.reg != nil {
			if m.reg.Get(normalized) != nil {
				return normalized
			}
			if normalized == "k8s" && m.reg.Get("k3s") != nil {
				return "k3s"
			}
			if normalized == "k3s" && m.reg.Get("k8s") != nil {
				return "k8s"
			}
		}

		return normalized
	}

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

func (m *ContainerManager) getInstanceAndRuntime(ctx context.Context, id int64) (*types.ContainerInstance, containerruntime.Runtime, error) {
	inst, err := m.repo.GetContainerInstanceByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	rt, err := m.getRuntimeByInstance(inst)
	if err != nil {
		return nil, nil, err
	}
	return inst, rt, nil
}

func (m *ContainerManager) createContainerEvent(ctx context.Context, instanceID int64, evt string, msg string) error {
	return m.repo.CreateContainerEvent(ctx, &types.ContainerEvent{
		ContainerInstanceID: instanceID,
		Event:               evt,
		Message:             msg,
	})
}

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

func applyDagNodeRuntimeSpec(spec *types.ContainerSpec, resolveVars map[string]string, runtimeName string) {
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

	if err := m.repo.UpdateContainerInstance(ctx, inst); err != nil {
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

func parseVolumes(raw []byte) []types.ContainerVolume {
	if len(raw) == 0 {
		return nil
	}

	obj := map[string]map[string]interface{}{}
	if err := json.Unmarshal(raw, &obj); err == nil {
		out := make([]types.ContainerVolume, 0, len(obj))
		for rawTarget, item := range obj {
			target := strings.TrimSpace(rawTarget)
			if target == "" {
				continue
			}

			source := target
			if bind, ok := item["bind"]; ok {
				if text, ok := normalizeScalarValue(bind); ok {
					source = strings.TrimSpace(text)
				}
			}
			mode := ""
			if rawMode, ok := item["mode"]; ok {
				if text, ok := normalizeScalarValue(rawMode); ok {
					mode = strings.TrimSpace(text)
				}
			}

			if source == "" || target == "" {
				continue
			}
			out = append(out, types.ContainerVolume{Source: source, Target: target, Mode: mode})
		}
		return out
	}

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
		out = append(out, types.ContainerVolume{
			Source: source,
			Target: target,
			Mode:   strings.TrimSpace(vol.Mode),
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
