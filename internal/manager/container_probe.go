package manager

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
)

const (
	defaultProbeTimeout    = 5 * time.Minute
	probeLogTailLines      = 2000
	maxProbeLogBytes       = 12000
	probeCleanupTimeout    = 60 * time.Second
	probeLogReadTimeout    = 10 * time.Second
	probeContainerWorkload = "job"
)

// probeEnvKeys are the runtime variables that are materialized into the probe
// container environment. Template / project env values keep precedence; these
// only fill the gaps so commands can use $R_PACKAGE_DIR / $PROJECT_DIR directly,
// instead of relying on template placeholders alone.
var probeEnvKeys = []string{
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

// containerProbeRunner implements runtimecontext.ContainerEnvProbe by reusing the
// same container assembly path as ContainerCreateWorker.executeCreate.
type containerProbeRunner struct {
	worker *ContainerCreateWorker
}

// NewContainerProbeRunner builds the probe_container_env executor.
func NewContainerProbeRunner(worker *ContainerCreateWorker) runtimecontext.ContainerEnvProbe {
	return &containerProbeRunner{worker: worker}
}

// ProbeContainerEnv creates a throwaway container from a template, runs the
// command synchronously, reads its log, and always deletes the runtime resource.
func (r *containerProbeRunner) ProbeContainerEnv(ctx context.Context, in runtimecontext.ContainerEnvProbeInput) (runtimecontext.ContainerEnvProbeResult, error) {
	var result runtimecontext.ContainerEnvProbeResult

	w := r.worker
	if w == nil || w.repo == nil || w.projectRepo == nil {
		return result, fmt.Errorf("container probe is not configured")
	}
	if in.ProjectID <= 0 {
		return result, fmt.Errorf("project id is required")
	}
	if in.ContainerTemplateID <= 0 {
		return result, fmt.Errorf("container_template_id is required")
	}
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return result, fmt.Errorf("command is required")
	}
	result.ContainerTemplateID = in.ContainerTemplateID

	tpl, err := w.repo.GetContainerTemplateByID(ctx, in.ContainerTemplateID)
	if err != nil {
		return result, fmt.Errorf("load container template %d: %w", in.ContainerTemplateID, err)
	}
	if tpl == nil {
		return result, fmt.Errorf("container template %d not found", in.ContainerTemplateID)
	}
	if strings.TrimSpace(tpl.ImageName) == "" {
		return result, fmt.Errorf("container template %d does not bind any image", in.ContainerTemplateID)
	}

	project, err := w.projectRepo.GetProjectByID(ctx, in.ProjectID)
	if err != nil {
		return result, fmt.Errorf("load project %d: %w", in.ProjectID, err)
	}
	if project == nil {
		return result, fmt.Errorf("project %d not found", in.ProjectID)
	}

	if w.containerManager == nil {
		return result, fmt.Errorf("container manager is not configured")
	}
	rt, err := w.containerManager.getRuntime()
	if err != nil {
		return result, err
	}
	result.RuntimeName = rt.Name()

	// 与 executeCreate 一致：先基于模板 / 项目构建运行时变量（含 buildPackageVariables），
	// 再用它解析 env / mounts / workdir 中的占位符。
	ownerType := types.ContainerOwnerDagNode
	ownerCtx := &ownerRuntimeContext{project: project, projectID: in.ProjectID}
	resolveVars := w.buildRuntimeVariables(ctx, tpl, ownerType, ownerCtx).ToMap()

	volumes := parseVolumes(tpl.Mounts, ownerType)
	volumes = append(volumes, parseVolumes(project.Mounts, ownerType)...)
	volumes, err = w.appendStorageVolumes(ctx, volumes)
	if err != nil {
		return result, fmt.Errorf("prepare probe volumes: %w", err)
	}

	envs := parseEnv(tpl.Env)
	for k, v := range parseEnv(project.Env) {
		envs[k] = v
	}
	fillProbeEnv(envs, resolveVars)

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
			ownerType, 0, fmt.Sprintf("agent-probe-%d-%d", in.ProjectID, time.Now().UnixNano()),
		),
		Labels: map[string]string{
			"gobrave-probe":      "true",
			"gobrave-project-id": strconv.FormatInt(in.ProjectID, 10),
		},
		// 探针是一次性命令：k8s 一律用 job，避免 deployment 退出后被重启。
		WorkloadKind:  probeContainerWorkload,
		ExposeService: false,
	}
	w.containerManager.applyRuntimeUser(spec, resolveVars)

	if w.res != nil {
		spec, err = w.res.Resolve(ctx, &ContainerRuntimeResolveInput{Spec: spec, Variables: resolveVars})
		if err != nil {
			return result, fmt.Errorf("resolve probe spec: %w", err)
		}
	}
	// 命令是 agent 传入的原始 shell 文本，必须在变量解析之后再挂上，避免把
	// $HOME / $PATH / $R_PACKAGE_DIR 等 shell 变量提前替换成宿主机值。
	spec.Command = []string{command}

	if err := w.ensureVolumeSources(ctx, spec.Volumes); err != nil {
		return result, fmt.Errorf("prepare probe volume sources: %w", err)
	}
	if hasDockerSocketVolume(spec.Volumes) {
		if gid, ok := resolvePathGID(dockerSocketPath); ok {
			spec.SupplementalGroups = appendUniqueInt64(spec.SupplementalGroups, parseInt64WithDefault(gid, 0))
		}
	}

	runtimeID, err := rt.Create(ctx, spec)
	if err != nil {
		return result, fmt.Errorf("create probe container: %w", err)
	}
	defer func() {
		// Probe containers have no DB instance; mark them as monitored so Delete
		// does not emit lifecycle events that ContainerManager would try to apply.
		if containerruntime.MarkIfNotMonitoring(runtimeID) {
			defer containerruntime.UnmarkRuntimeMonitoring(runtimeID)
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), probeCleanupTimeout)
		defer cancel()
		if deleteErr := rt.Delete(cleanupCtx, runtimeID); deleteErr != nil {
			logger.Warnf(ctx, "[ContainerProbe] delete runtime failed, runtime_id=%s err=%v", runtimeID, deleteErr)
		}
	}()

	starter, ok := rt.(containerruntime.RuntimeSyncStarter)
	if !ok {
		return result, fmt.Errorf("runtime %s does not support synchronous start", rt.Name())
	}

	timeout := defaultProbeTimeout
	if in.TimeoutSeconds > 0 {
		timeout = time.Duration(in.TimeoutSeconds) * time.Second
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	startedAt := time.Now()
	exitCode, waitErr := starter.StartAndWait(waitCtx, runtimeID)
	result.DurationSeconds = time.Since(startedAt).Seconds()
	result.ExitCode = exitCode
	if waitErr != nil {
		result.Error = waitErr.Error()
		result.TimedOut = errors.Is(waitErr, context.DeadlineExceeded)
	} else if exitCode != 0 {
		result.Error = fmt.Sprintf("command exited with code %d", exitCode)
	}
	result.Success = waitErr == nil && exitCode == 0

	logCtx, logCancel := context.WithTimeout(context.Background(), probeLogReadTimeout)
	defer logCancel()
	logText, logErr := rt.Logs(logCtx, runtimeID, probeLogTailLines)
	if logErr != nil {
		if result.Error == "" {
			result.Error = logErr.Error()
		}
	} else {
		result.Log, result.LogTruncated = truncateProbeLog(logText, maxProbeLogBytes)
	}

	return result, nil
}

// fillProbeEnv adds package / project runtime variables that are not already set
// by the template or project. Existing env values always win.
func fillProbeEnv(envs, vars map[string]string) {
	if envs == nil || vars == nil {
		return
	}
	for _, key := range probeEnvKeys {
		if _, exists := envs[key]; exists {
			continue
		}
		if value := strings.TrimSpace(vars[key]); value != "" {
			envs[key] = value
		}
	}
}

// truncateProbeLog keeps the tail of the log and reports whether it was cut.
func truncateProbeLog(text string, maxBytes int) (string, bool) {
	if maxBytes <= 0 || len(text) <= maxBytes {
		return text, false
	}
	return "...(truncated)\n" + text[len(text)-maxBytes:], true
}
