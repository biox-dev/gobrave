package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
)

type DockerRuntime struct {
	handler containerruntime.RuntimeEventHandler

	// paths 统一负责宿主 / 容器 / daemon 三种路径语义的转换。
	// 连接 WSL2 里的 dockerd 时（DOCKER_HOST=tcp://...），bind mount 的 Source
	// 会被映射成 daemon 视角的 /mnt/<drive>/...，否则 daemon 侧会报
	// bind source path does not exist / mount path must be absolute。
	paths *containerruntime.PathMapper

	once    sync.Once
	client  *client.Client
	initErr error
}

// DockerRuntimeOption 定制 DockerRuntime 的构造。
type DockerRuntimeOption func(*DockerRuntime)

// WithPathMapper 注入统一路径映射器（由装配层按 storage.base_dir + WSL 探测构建）。
func WithPathMapper(paths *containerruntime.PathMapper) DockerRuntimeOption {
	return func(d *DockerRuntime) {
		if paths != nil {
			d.paths = paths
		}
	}
}

func NewDockerRuntime(opts ...DockerRuntimeOption) *DockerRuntime {
	d := &DockerRuntime{paths: containerruntime.NewPathMapper("", false)}
	for _, opt := range opts {
		if opt != nil {
			opt(d)
		}
	}
	return d
}

// AutoDetectWSLDaemon 依据 DOCKER_HOST 推断是否连接 WSL2 内的 dockerd。
//
// 判据：Windows 宿主 + DOCKER_HOST 为 tcp:// 或 http://。
// npipe://（Docker Desktop 的 Windows daemon）不参与推断：它的文件系统就是 Windows，
// bind mount 的 Source 仍应是 Windows 盘符路径。
func AutoDetectWSLDaemon() bool {
	if runtime.GOOS != "windows" {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(os.Getenv("DOCKER_HOST")))
	return strings.HasPrefix(host, "tcp://") || strings.HasPrefix(host, "http://")
}

// WSLDaemon 报告当前是否按 WSL2 daemon 语义翻译挂载路径。
func (d *DockerRuntime) WSLDaemon() bool {
	return d.paths.WSLDaemon()
}

func (d *DockerRuntime) Name() string {
	return "docker"
}

func (d *DockerRuntime) Create(ctx context.Context, spec *types.ContainerSpec) (string, error) {
	if spec == nil {
		return "", errors.New("container spec is nil")
	}
	if strings.TrimSpace(spec.Image) == "" {
		return "", errors.New("container image is required")
	}

	cli, err := d.getClient()
	if err != nil {
		return "", err
	}

	// docker create 本身不含拉取动作（没有 imagePullPolicy 概念），
	// 因此把镜像的 PullPolicy 翻译成 create 前是否/何时 pull，语义与 k8s 对齐：
	// Always -> 强制 pull，IfNotPresent -> 本地缺失才 pull，Never -> 本地缺失直接失败。
	pullPolicy := normalizePullPolicy(spec.PullPolicy)
	switch pullPolicy {
	case types.PullPolicyAlways, types.PullPolicyIfNotPresent, types.PullPolicyNever:
	default:
		// 与 k8s 侧（toKubeImagePullPolicy）保持一致：无法识别的策略按 IfNotPresent 处理，
		// 避免历史脏数据直接阻断容器创建。
		pullPolicy = types.PullPolicyIfNotPresent
	}
	if err := d.EnsureImage(ctx, spec.Image, pullPolicy); err != nil {
		return "", err
	}

	resp, err := cli.ContainerCreate(ctx, d.toContainerConfig(spec), d.toHostConfig(spec), nil, nil, "")
	if err != nil {
		// Never 表示只允许使用本地镜像，兜底 pull 会违背语义。
		if pullPolicy == types.PullPolicyNever {
			return "", fmt.Errorf("create container: %w", err)
		}

		pullErr := d.pullImage(ctx, cli, spec.Image)
		if pullErr != nil {
			return "", fmt.Errorf("create container: %w", err)
		}

		resp, err = cli.ContainerCreate(ctx, d.toContainerConfig(spec), d.toHostConfig(spec), nil, nil, "")
		if err != nil {
			logger.Errorf(context.Background(), "create container after pull image %s: %v", spec.Image, err)
			return "", fmt.Errorf("create container after pull: %w", err)
		}
	}

	return d.toRuntimeID(resp.ID), nil
}

func (d *DockerRuntime) EnsureImage(ctx context.Context, imageName string, pullPolicy string) error {
	imageName = strings.TrimSpace(imageName)
	if imageName == "" {
		return errors.New("image is required")
	}

	cli, err := d.getClient()
	if err != nil {
		return err
	}

	exists, err := d.imageExists(ctx, cli, imageName)
	if err != nil {
		return err
	}

	switch normalizePullPolicy(pullPolicy) {
	case types.PullPolicyAlways:
		return d.pullImage(ctx, cli, imageName)
	case types.PullPolicyNever:
		if !exists {
			return fmt.Errorf("image not found in local runtime: %s", imageName)
		}
		return nil
	case types.PullPolicyIfNotPresent:
		if exists {
			return nil
		}
		return d.pullImage(ctx, cli, imageName)
	default:
		return fmt.Errorf("unsupported pull policy: %s", pullPolicy)
	}
}

func (d *DockerRuntime) Start(ctx context.Context, id string) error {
	containerID, err := d.toContainerID(id)
	if err != nil {
		return err
	}

	cli, err := d.getClient()
	if err != nil {
		return err
	}

	if err := cli.ContainerStart(ctx, containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("start container %s: %w", containerID, err)
	}

	d.emitEvent("ContainerStarted", id, "")
	return d.Monitor(ctx, id)
}

func (d *DockerRuntime) Monitor(ctx context.Context, runtimeID string) error {
	if _, err := d.toContainerID(runtimeID); err != nil {
		return err
	}

	cli, err := d.getClient()
	if err != nil {
		return err
	}

	if ctx == nil {
		ctx = context.Background()
	}

	containerID, _ := d.toContainerID(runtimeID)
	if _, err := cli.ContainerInspect(ctx, containerID); err != nil {
		if errdefs.IsNotFound(err) {
			d.emitEvent("ContainerDeleted", runtimeID, "container not found")
			return nil
		}
		return fmt.Errorf("inspect container %s for monitor: %w", containerID, err)
	}

	if !containerruntime.MarkIfNotMonitoring(runtimeID) {
		return nil
	}

	go d.waitContainerExit(runtimeID)
	return nil
}

func (d *DockerRuntime) waitContainerExit(runtimeID string) {
	defer containerruntime.UnmarkRuntimeMonitoring(runtimeID)

	containerID, err := d.toContainerID(runtimeID)
	if err != nil {
		d.emitEvent("ContainerFailed", runtimeID, err.Error())
		return
	}

	cli, err := d.getClient()
	if err != nil {
		d.emitEvent("ContainerFailed", runtimeID, err.Error())
		return
	}

	statusCh, errCh := cli.ContainerWait(context.Background(), containerID, container.WaitConditionNotRunning)
	for statusCh != nil || errCh != nil {
		select {
		case waitErr, ok := <-errCh:
			if !ok {
				errCh = nil
				continue
			}
			if waitErr == nil {
				continue
			}
			if errdefs.IsNotFound(waitErr) {
				d.emitEvent("ContainerDeleted", runtimeID, waitErr.Error())
				return
			}
			d.emitEvent("ContainerFailed", runtimeID, waitErr.Error())
			return
		case result, ok := <-statusCh:
			if !ok {
				statusCh = nil
				continue
			}

			exitCode := int(result.StatusCode)
			if result.Error != nil {
				if strings.Contains(strings.ToLower(result.Error.Message), "no such container") {
					d.emitEvent("ContainerDeleted", runtimeID, result.Error.Message)
					return
				}
				d.emitEvent("ContainerFailed", runtimeID, result.Error.Message)
				return
			}
			if exitCode == 0 {
				d.emitEvent("ContainerExited", runtimeID, strconv.Itoa(exitCode))
				return
			}
			d.emitEvent("ContainerFailed", runtimeID, strconv.Itoa(exitCode))
			return
		}
	}
}

func (d *DockerRuntime) Stop(ctx context.Context, id string) error {
	containerID, err := d.toContainerID(id)
	if err != nil {
		return err
	}

	cli, err := d.getClient()
	if err != nil {
		return err
	}

	if err := cli.ContainerStop(ctx, containerID, container.StopOptions{}); err != nil {
		return fmt.Errorf("stop container %s: %w", containerID, err)
	}

	return nil
}

func (d *DockerRuntime) Pause(ctx context.Context, id string) error {
	containerID, err := d.toContainerID(id)
	if err != nil {
		return err
	}

	cli, err := d.getClient()
	if err != nil {
		return err
	}

	if err := cli.ContainerPause(ctx, containerID); err != nil {
		return fmt.Errorf("pause container %s: %w", containerID, err)
	}

	d.emitEvent("ContainerPaused", id, "")
	return nil
}

func (d *DockerRuntime) Resume(ctx context.Context, id string) error {
	containerID, err := d.toContainerID(id)
	if err != nil {
		return err
	}

	cli, err := d.getClient()
	if err != nil {
		return err
	}

	if err := cli.ContainerUnpause(ctx, containerID); err != nil {
		return fmt.Errorf("resume container %s: %w", containerID, err)
	}

	d.emitEvent("ContainerResumed", id, "")
	return nil
}

func (d *DockerRuntime) Delete(ctx context.Context, id string) error {
	containerID, err := d.toContainerID(id)
	if err != nil {
		return err
	}

	cli, err := d.getClient()
	if err != nil {
		return err
	}

	isMonitor := containerruntime.IsRuntimeMonitoring(id)
	if err := cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		// return fmt.Errorf("delete container %s: %w", containerID, err)
		logger.Errorf(context.Background(), "delete container %s: %v", containerID, err)
		d.emitEvent("ContainerDeleted", id, err.Error())
		return nil
	}
	if !isMonitor {
		d.emitEvent("ContainerDeleted", id, "container deleted")
	}
	// d.emitEvent("ContainerDeleted", id, "")
	return nil
}

func (d *DockerRuntime) Logs(ctx context.Context, id string, tail int) (string, error) {
	if tail <= 0 {
		tail = 200
	}
	containerID, err := d.toContainerID(id)
	if err != nil {
		return "", err
	}

	cli, err := d.getClient()
	if err != nil {
		return "", err
	}

	r, err := cli.ContainerLogs(ctx, containerID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       strconv.Itoa(tail),
	})
	if err != nil {
		return "", fmt.Errorf("get logs for %s: %w", containerID, err)
	}
	defer r.Close()

	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, r); err != nil {
		return "", fmt.Errorf("decode logs for %s: %w", containerID, err)
	}

	return out.String(), nil
}

func (d *DockerRuntime) Exec(ctx context.Context, id string, cmd []string) (string, error) {
	if len(cmd) == 0 {
		return "", errors.New("exec command is required")
	}

	containerID, err := d.toContainerID(id)
	if err != nil {
		return "", err
	}

	cli, err := d.getClient()
	if err != nil {
		return "", err
	}

	execResp, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("create exec for %s: %w", containerID, err)
	}

	attach, err := cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("attach exec for %s: %w", containerID, err)
	}
	defer attach.Close()

	var out bytes.Buffer
	if _, err := stdcopy.StdCopy(&out, &out, attach.Reader); err != nil {
		return "", fmt.Errorf("read exec output for %s: %w", containerID, err)
	}

	inspect, err := cli.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return "", fmt.Errorf("inspect exec for %s: %w", containerID, err)
	}
	if inspect.ExitCode != 0 {
		return out.String(), fmt.Errorf("exec on %s exited with code %d", containerID, inspect.ExitCode)
	}

	return out.String(), nil
}

func (d *DockerRuntime) Inspect(ctx context.Context, id string) (*containerruntime.RuntimeInspection, error) {
	containerID, err := d.toContainerID(id)
	if err != nil {
		return nil, err
	}

	cli, err := d.getClient()
	if err != nil {
		return nil, err
	}

	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("inspect container %s: %w", containerID, err)
	}

	ip := ""
	if info.NetworkSettings != nil {
		ip = strings.TrimSpace(info.NetworkSettings.IPAddress)
		if ip == "" {
			for _, nw := range info.NetworkSettings.Networks {
				if nw == nil {
					continue
				}
				ip = strings.TrimSpace(nw.IPAddress)
				if ip != "" {
					break
				}
			}
		}
	}

	return &containerruntime.RuntimeInspection{IPAddress: ip}, nil
}

func (d *DockerRuntime) Describe(ctx context.Context, id string) (*containerruntime.RuntimeDescription, error) {
	containerID, err := d.toContainerID(id)
	if err != nil {
		return nil, err
	}

	cli, err := d.getClient()
	if err != nil {
		return nil, err
	}

	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("inspect container %s: %w", containerID, err)
	}

	raw, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal container %s: %w", containerID, err)
	}

	return &containerruntime.RuntimeDescription{
		Kind:   "container",
		Name:   strings.TrimPrefix(info.Name, "/"),
		Format: "json",
		Raw:    string(raw),
	}, nil
}

func (d *DockerRuntime) SetEventHandler(h containerruntime.RuntimeEventHandler) {
	d.handler = h
}

// HealthCheck satisfies containerruntime.RuntimeHealthChecker.
// 通过 docker client 的 Ping 探测 daemon 是否可达（socket 权限 / daemon 未启动都会在这里暴露）。
func (d *DockerRuntime) HealthCheck(ctx context.Context) error {
	cli, err := d.getClient()
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := cli.Ping(ctx); err != nil {
		return fmt.Errorf("ping docker daemon: %w", err)
	}
	return nil
}

func (d *DockerRuntime) getClient() (*client.Client, error) {
	d.once.Do(func() {
		cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
		if err != nil {
			d.initErr = fmt.Errorf("init docker client: %w", err)
			return
		}
		d.client = cli
	})

	if d.initErr != nil {
		return nil, d.initErr
	}
	if d.client == nil {
		return nil, errors.New("docker client is not initialized")
	}

	return d.client, nil
}

func (d *DockerRuntime) pullImage(ctx context.Context, cli *client.Client, imageName string) error {
	r, err := cli.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return err
	}
	defer r.Close()

	_, _ = io.Copy(io.Discard, r)
	return nil
}

func (d *DockerRuntime) imageExists(ctx context.Context, cli *client.Client, imageName string) (bool, error) {
	_, _, err := cli.ImageInspectWithRaw(ctx, imageName)
	if err == nil {
		return true, nil
	}
	if errdefs.IsNotFound(err) {
		return false, nil
	}
	return false, fmt.Errorf("inspect image %s: %w", imageName, err)
}

func normalizePullPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case strings.ToLower(types.PullPolicyAlways):
		return types.PullPolicyAlways
	case strings.ToLower(types.PullPolicyNever):
		return types.PullPolicyNever
	case "", strings.ToLower(types.PullPolicyIfNotPresent):
		return types.PullPolicyIfNotPresent
	default:
		return strings.TrimSpace(policy)
	}
}

func (d *DockerRuntime) toContainerConfig(spec *types.ContainerSpec) *container.Config {
	envKeys := make([]string, 0, len(spec.Env))
	for key := range spec.Env {
		envKeys = append(envKeys, key)
	}
	sort.Strings(envKeys)

	env := make([]string, 0, len(spec.Env)+1)
	for _, key := range envKeys {
		env = append(env, fmt.Sprintf("%s=%s", key, spec.Env[key]))
	}
	// HOME 由 spec.HomeDir 决定（非 root 用户运行时的可写家目录），
	// 显式设置后覆盖模板 env 里的同名值。
	if homeDir := strings.TrimSpace(spec.HomeDir); homeDir != "" {
		env = append(env, "HOME="+homeDir)
	}

	return &container.Config{
		Image:      spec.Image,
		Entrypoint: spec.Entrypoint,
		Cmd:        spec.Command,
		WorkingDir: spec.WorkDir,
		Env:        env,
		User:       strings.TrimSpace(spec.User),
	}
}

func (d *DockerRuntime) toHostConfig(spec *types.ContainerSpec) *container.HostConfig {
	resources := container.Resources{}
	if spec.Memory > 0 {
		resources.Memory = spec.Memory
	}
	if spec.CPU > 0 {
		nanoCPUs := int64(math.Round(spec.CPU * 1e9))
		if nanoCPUs > 0 {
			resources.NanoCPUs = nanoCPUs
		}
	}

	mounts := d.toMounts(spec.Volumes)

	groupAdd := make([]string, 0, len(spec.SupplementalGroups))
	for _, gid := range spec.SupplementalGroups {
		if gid <= 0 {
			continue
		}
		groupAdd = append(groupAdd, strconv.FormatInt(gid, 10))
	}

	return &container.HostConfig{
		Resources: resources,
		// 用 Mounts 而不是 Binds：Mounts 是结构化字段，Source/Target 分开传递，
		// 不会被 docker 再按 ":" 切分，因此 Windows 宿主路径（D:\a\b）不会被误判。
		// Binds 的 "src:dst:mode" 语法对含盘符的路径无解（too many colons）。
		Mounts:   mounts,
		GroupAdd: groupAdd,
		Tmpfs:    toTmpfsOptions(spec.Tmpfs),
	}
}

// toMounts 把 spec.Volumes 转成 docker 的结构化 mount 列表。
//
// 这里刻意不使用 container.HostConfig.Binds：bind 字符串是分号分隔的
// "source:target:mode"，Windows 宿主路径自带冒号（D:\workspace\...），
// 无论怎么拼接都会被 docker 解析成多余的分段，报
// "mount denied: the source path ... too many colons"。
//
// 结构化 mount 的 Source 直接交给 daemon，daemon 侧再做路径解析，因此
// Linux 与 Windows 宿主都能正确处理；Target 统一转成容器内的 POSIX 路径。
//
// 路径语义全部交给 containerruntime.PathMapper：bind 的 Source 由它翻译成
// daemon 视角（WSL2 下 D:\x → /mnt/d/x），Target 由它归一成容器路径。
func (d *DockerRuntime) toMounts(volumes []types.ContainerVolume) []mount.Mount {
	if len(volumes) == 0 {
		return nil
	}
	mounts := make([]mount.Mount, 0, len(volumes))
	for _, volume := range volumes {
		source := strings.TrimSpace(volume.Source)
		target := strings.TrimSpace(volume.Target)
		logger.Debugf(context.Background(), "toMounts: raw volume type=%q source=%q target=%q mode=%q", volume.Type, source, target, volume.Mode)
		if source == "" || target == "" {
			logger.Debugf(context.Background(), "toMounts: skip volume with empty source=%q or target=%q", source, target)
			continue
		}
		mountType := mountTypeForVolume(volume)
		if mountType == mount.TypeBind {
			// 只有 bind 需要翻译：named volume / tmpfs 的 Source 不是宿主路径。
			source = d.paths.Daemon(source)
		}
		containerTarget := d.paths.Container(target)
		logger.Debugf(context.Background(), "toMounts: resolved mount type=%q source=%q target=%q", mountType, source, containerTarget)
		mountItem := mount.Mount{
			Type:   mountType,
			Source: source,
			Target: containerTarget,
		}
		// ro/rw 落到 ReadOnly。moby 的 Mount.Consistency 只接受
		// consistent/cached/delegated（macOS 专用），z/Z 等 SELinux 选项
		// 在这里没有对应字段，只能忽略（与 Binds 时代的行为差别仅限 SELinux 标记）。
		mountItem.ReadOnly = isReadOnlyMode(volume.Mode)
		mounts = append(mounts, mountItem)
	}
	if len(mounts) == 0 {
		return nil
	}
	return mounts
}

func mountTypeForVolume(volume types.ContainerVolume) mount.Type {
	if containerruntime.IsBindVolumeType(volume.Type) {
		return mount.TypeBind
	}
	if strings.EqualFold(strings.TrimSpace(volume.Type), "tmpfs") {
		return mount.TypeTmpfs
	}
	return mount.TypeVolume
}

// isReadOnlyMode 解析 mode 里的 ro 语义（"ro"、"ro,z"、"z,ro" 都算只读）。
func isReadOnlyMode(mode string) bool {
	for _, part := range strings.Split(mode, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "ro", "readonly":
			return true
		}
	}
	return false
}

// toTmpfsOptions 把 spec.Tmpfs 转成 docker 的 --tmpfs 选项字符串，
// 例如 "/home/app:rw,uid=1000,gid=1000,mode=700"。
func toTmpfsOptions(mounts []types.ContainerTmpfs) map[string]string {
	if len(mounts) == 0 {
		return nil
	}
	out := make(map[string]string, len(mounts))
	for _, mount := range mounts {
		target := strings.TrimSpace(mount.Target)
		if target == "" {
			continue
		}
		opts := []string{"rw"}
		if uid := strings.TrimSpace(mount.UID); uid != "" {
			opts = append(opts, "uid="+uid)
		}
		if gid := strings.TrimSpace(mount.GID); gid != "" {
			opts = append(opts, "gid="+gid)
		}
		mode := strings.TrimSpace(mount.Mode)
		if mode == "" {
			mode = "1777"
		}
		opts = append(opts, "mode="+mode)
		out[target] = strings.Join(opts, ",")
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (d *DockerRuntime) toContainerID(runtimeID string) (string, error) {
	id := strings.TrimSpace(runtimeID)
	id = strings.TrimPrefix(id, d.Name()+"-")
	if id == "" {
		return "", errors.New("runtime id is required")
	}
	return id, nil
}

func (d *DockerRuntime) toRuntimeID(containerID string) string {
	return d.Name() + "-" + strings.TrimSpace(containerID)
}

func (d *DockerRuntime) emitEvent(eventType string, runtimeID string, message string) {
	if d.handler == nil {
		return
	}

	d.handler.OnEvent(containerruntime.RuntimeEvent{
		Type:      eventType,
		RuntimeID: runtimeID,
		Message:   message,
	})
}
