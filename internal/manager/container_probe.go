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
	probeTerminalStartWait = 2 * time.Minute
	probeContainerWorkload = "job"
	// probeKeepAliveCommand keeps the probe container alive until the interactive
	// terminal disconnects; the real shell is opened with docker exec / kubectl exec.
	probeKeepAliveCommand = "while true; do sleep 3600; done"
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

// ContainerProbeRunner implements runtimecontext.ContainerEnvProbe by reusing the
// same container assembly path as ContainerCreateWorker.executeCreate.
type ContainerProbeRunner struct {
	worker *ContainerCreateWorker
}

// NewContainerProbeRunner builds the probe_container_env executor.
func NewContainerProbeRunner(worker *ContainerCreateWorker) *ContainerProbeRunner {
	return &ContainerProbeRunner{worker: worker}
}

// ContainerTemplateName 返回模板的展示名与名称，供终端会话生成默认展示名。
//
// 模板不可读 / 未配置时返回空串，调用方会回退到模板 ID。
func (r *ContainerProbeRunner) ContainerTemplateName(ctx context.Context, templateID int64) (string, string) {
	if r == nil || r.worker == nil || r.worker.repo == nil || templateID <= 0 {
		return "", ""
	}
	tpl, err := r.worker.repo.GetContainerTemplateByID(ctx, templateID)
	if err != nil || tpl == nil {
		return "", ""
	}
	return tpl.DisplayName, tpl.Name
}

type probePlan struct {
	spec *types.ContainerSpec
	rt   containerruntime.Runtime
}

// buildProbePlan assembles the runtime spec exactly like executeCreate:
// template image / resources / mounts / env, project env / mounts, package
// variables, storage mounts, variable resolution, volume source preparation.
// The command itself is left empty for the caller to set after resolution.
func (r *ContainerProbeRunner) buildProbePlan(ctx context.Context, userID, projectID, templateID int64) (*probePlan, error) {
	w := r.worker
	if w == nil || w.repo == nil || w.projectRepo == nil {
		return nil, fmt.Errorf("container probe is not configured")
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
		return nil, fmt.Errorf("prepare probe volumes: %w", err)
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
			ownerType, 0, fmt.Sprintf("agent-probe-%d-%d", projectID, time.Now().UnixNano()),
		),
		Labels: map[string]string{
			"gobrave-probe":       "true",
			"gobrave-project-id":  strconv.FormatInt(projectID, 10),
			"gobrave-template-id": strconv.FormatInt(templateID, 10),
		},
		// 探针是一次性命令：k8s 一律用 job，避免 deployment 退出后被重启。
		WorkloadKind:  probeContainerWorkload,
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
			return nil, fmt.Errorf("resolve probe spec: %w", err)
		}
	}

	if err := w.ensureVolumeSources(ctx, spec.Volumes); err != nil {
		return nil, fmt.Errorf("prepare probe volume sources: %w", err)
	}
	if hasDockerSocketVolume(spec.Volumes) {
		if gid, ok := resolvePathGID(dockerSocketPath); ok {
			spec.SupplementalGroups = appendUniqueInt64(spec.SupplementalGroups, parseInt64WithDefault(gid, 0))
		}
	}

	return &probePlan{spec: spec, rt: rt}, nil
}

// deleteProbeRuntime removes a probe container and suppresses lifecycle events:
// probe containers have no DB instance, so ContainerManager must not try to
// apply ContainerDeleted to an instance that does not exist.
func deleteProbeRuntime(ctx context.Context, rt containerruntime.Runtime, runtimeID string) {
	if containerruntime.MarkIfNotMonitoring(runtimeID) {
		defer containerruntime.UnmarkRuntimeMonitoring(runtimeID)
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), probeCleanupTimeout)
	defer cancel()
	if err := rt.Delete(cleanupCtx, runtimeID); err != nil {
		logger.Warnf(ctx, "[ContainerProbe] delete runtime failed, runtime_id=%s err=%v", runtimeID, err)
	}
}

// ProbeTerminalHandle 是一次「探针容器 + 交互式 shell」的句柄。
//
// 它把创建/认领两条路径统一起来：
//   - StartTerminalRuntime 新建容器后返回 runtimeID；
//   - AttachTerminalRuntime 在已有容器上重新 exec 一个 shell（进程重启后的认领）。
//
// Cleanup 负责删除容器（关闭 shell 之后调用）；调用方必须在会话 Close 时执行它。
type ProbeTerminalHandle struct {
	Runtime       containerruntime.Runtime
	RuntimeName   string
	RuntimeID     string
	ContainerName string
	Session       containerruntime.TerminalSession
	Cleanup       func()
}

// StartTerminalRuntime 创建长期存活的探针容器、等待其可被 exec，并打开交互式 shell。
func (r *ContainerProbeRunner) StartTerminalRuntime(ctx context.Context, userID, projectID, templateID int64, opts containerruntime.TerminalOptions) (*ProbeTerminalHandle, error) {
	plan, err := r.buildProbePlan(ctx, userID, projectID, templateID)
	if err != nil {
		return nil, err
	}
	plan.spec.Command = []string{probeKeepAliveCommand}

	runtimeID, err := plan.rt.Create(ctx, plan.spec)
	if err != nil {
		return nil, fmt.Errorf("create probe container: %w", err)
	}
	cleanup := func() { deleteProbeRuntime(ctx, plan.rt, runtimeID) }

	starter, ok := plan.rt.(containerruntime.RuntimeTerminalStarter)
	if !ok {
		cleanup()
		return nil, fmt.Errorf("runtime %s does not support interactive terminals", plan.rt.Name())
	}
	startCtx, cancel := context.WithTimeout(ctx, probeTerminalStartWait)
	defer cancel()
	if err := starter.StartForTerminal(startCtx, runtimeID); err != nil {
		cleanup()
		return nil, fmt.Errorf("start probe container: %w", err)
	}

	handle := &ProbeTerminalHandle{
		Runtime:       plan.rt,
		RuntimeName:   plan.rt.Name(),
		RuntimeID:     runtimeID,
		ContainerName: plan.spec.RuntimeName,
		Cleanup:       cleanup,
	}
	session, err := plan.rt.(containerruntime.RuntimeTerminal).OpenTerminal(ctx, runtimeID, normalizeTerminalOptions(opts))
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("open probe terminal: %w", err)
	}
	handle.Session = session
	return handle, nil
}

// AttachTerminalRuntime 在已有容器上执行交互式 shell（等价于 kubectl/docker exec -it）。
//
// 这是进程重启后「认领」原容器的唯一方式：容器还在，但原来的 shell 已经随连接
// 结束，所以这里得到的是一个新的 bash（cwd / 环境变量不会保留，历史输出由滚动缓冲回放）。
func (r *ContainerProbeRunner) AttachTerminalRuntime(ctx context.Context, runtimeName, runtimeID string, opts containerruntime.TerminalOptions) (*ProbeTerminalHandle, error) {
	rt := r.resolveRuntime(runtimeName)
	if rt == nil {
		return nil, fmt.Errorf("runtime %q is not registered", runtimeName)
	}
	terminal, ok := rt.(containerruntime.RuntimeTerminal)
	if !ok {
		return nil, fmt.Errorf("runtime %s does not support interactive terminals", rt.Name())
	}
	session, err := terminal.OpenTerminal(ctx, runtimeID, normalizeTerminalOptions(opts))
	if err != nil {
		return nil, fmt.Errorf("attach probe terminal: %w", err)
	}
	return &ProbeTerminalHandle{
		Runtime:     rt,
		RuntimeName: rt.Name(),
		RuntimeID:   runtimeID,
		Session:     session,
		Cleanup:     func() { deleteProbeRuntime(ctx, rt, runtimeID) },
	}, nil
}

// DropTerminalRuntime 删除一个（可能来自旧进程的）终端容器，失败只记日志。
func (r *ContainerProbeRunner) DropTerminalRuntime(ctx context.Context, runtimeName, runtimeID string) {
	if strings.TrimSpace(runtimeID) == "" {
		return
	}
	rt := r.resolveRuntime(runtimeName)
	if rt == nil {
		return
	}
	deleteProbeRuntime(ctx, rt, runtimeID)
}

// resolveRuntime 优先按名字解析，其次回退到当前默认运行时。
func (r *ContainerProbeRunner) resolveRuntime(name string) containerruntime.Runtime {
	if r.worker == nil {
		return nil
	}
	if r.worker.reg != nil {
		if n := strings.TrimSpace(name); n != "" {
			if rt := r.worker.reg.Get(n); rt != nil {
				return rt
			}
		}
	}
	if r.worker.containerManager == nil {
		return nil
	}
	rt, err := r.worker.containerManager.getRuntime()
	if err != nil {
		return nil
	}
	return rt
}

// OpenProbeTerminal 保留旧接口：创建容器并打开 shell，cleanup 删除容器。
func (r *ContainerProbeRunner) OpenProbeTerminal(ctx context.Context, projectID, templateID int64, opts containerruntime.TerminalOptions) (containerruntime.TerminalSession, func(), error) {
	handle, err := r.StartTerminalRuntime(ctx, 0, projectID, templateID, opts)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { handle.Cleanup() }
	return handle.Session, cleanup, nil
}

// normalizeTerminalOptions 统一交互式终端的默认值。
func normalizeTerminalOptions(opts containerruntime.TerminalOptions) containerruntime.TerminalOptions {
	if len(opts.Command) == 0 {
		opts.Command = []string{"/bin/bash", "-l"}
	}
	opts.TTY = true
	return opts
}

// ProbeContainerEnv creates a throwaway container from a template, runs the
// command synchronously, reads its log, and always deletes the runtime resource.
func (r *ContainerProbeRunner) ProbeContainerEnv(ctx context.Context, in runtimecontext.ContainerEnvProbeInput) (runtimecontext.ContainerEnvProbeResult, error) {
	var result runtimecontext.ContainerEnvProbeResult

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

	plan, err := r.buildProbePlan(ctx, 0, in.ProjectID, in.ContainerTemplateID)
	if err != nil {
		return result, err
	}
	result.RuntimeName = plan.rt.Name()
	// 命令是 agent 传入的原始 shell 文本，必须在变量解析之后再挂上，避免把
	// $HOME / $PATH / $R_PACKAGE_DIR 等 shell 变量提前替换成宿主机值。
	plan.spec.Command = []string{command}

	runtimeID, err := plan.rt.Create(ctx, plan.spec)
	if err != nil {
		return result, fmt.Errorf("create probe container: %w", err)
	}
	defer deleteProbeRuntime(ctx, plan.rt, runtimeID)

	starter, ok := plan.rt.(containerruntime.RuntimeSyncStarter)
	if !ok {
		return result, fmt.Errorf("runtime %s does not support synchronous start", plan.rt.Name())
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
	logText, logErr := plan.rt.Logs(logCtx, runtimeID, probeLogTailLines)
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
