package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/errdefs"
	"github.com/docker/docker/pkg/stdcopy"
	containerruntime "github.com/gobravedev/gobrave/internal/container_runtime"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/types"
)

const dockerSocketPath = "/var/run/docker.sock"

type DockerRuntime struct {
	handler containerruntime.RuntimeEventHandler

	once    sync.Once
	client  *client.Client
	initErr error
}

func NewDockerRuntime() *DockerRuntime {
	return &DockerRuntime{}
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

	resp, err := cli.ContainerCreate(ctx, d.toContainerConfig(spec), d.toHostConfig(spec), nil, nil, "")
	if err != nil {
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

	if err := cli.ContainerRemove(ctx, containerID, container.RemoveOptions{Force: true}); err != nil {
		// return fmt.Errorf("delete container %s: %w", containerID, err)
		logger.Errorf(context.Background(), "delete container %s: %v", containerID, err)
	}

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

func (d *DockerRuntime) SetEventHandler(h containerruntime.RuntimeEventHandler) {
	d.handler = h
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
	env := make([]string, 0, len(spec.Env))
	for key, value := range spec.Env {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
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

	binds := make([]string, 0, len(spec.Volumes))
	for _, volume := range spec.Volumes {
		source := strings.TrimSpace(volume.Source)
		target := strings.TrimSpace(volume.Target)
		if source == "" || target == "" {
			continue
		}
		bind := source + ":" + target
		if mode := strings.TrimSpace(volume.Mode); mode != "" {
			bind += ":" + mode
		}
		binds = append(binds, bind)
	}

	groupAdd := []string{}
	if hasDockerSocketVolume(spec.Volumes) {
		if gid, ok := resolveSocketGID(dockerSocketPath); ok {
			groupAdd = append(groupAdd, gid)
		}
	}

	return &container.HostConfig{Resources: resources, Binds: binds, GroupAdd: groupAdd}
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

func resolveSocketGID(path string) (string, bool) {
	info, err := os.Stat(strings.TrimSpace(path))
	if err != nil {
		return "", false
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}

	return strconv.FormatUint(uint64(stat.Gid), 10), true
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
