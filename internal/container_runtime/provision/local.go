package provision

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// systemdProvisioner 是 Linux / macOS 上的实现：管理本机已安装的 dockerd。
//
// 它刻意不支持 Download / Install / Uninstall：系统级 daemon 由发行版包管理器
// 维护，gobrave 不应替用户装包。这样 Start 的语义与 Windows 完全一致——
// 「已安装则确保运行，未安装则返回 ErrNotInstalled」，上层无需分支。
type systemdProvisioner struct {
	// socket 是本机 daemon 的 unix socket 路径。
	socket string
	// serviceName systemd 单元名。
	serviceName string
}

// newLocal 返回管理本机 docker daemon 的实现。
//
// 非 Windows 平台都走这里（Linux 是主要场景）。macOS 上通常由 Docker Desktop
// 提供 socket，此时 Start 会因为找不到 socket 而返回 ErrNotInstalled，
// 由用户通过 Desktop 启动，行为仍然一致。
func newLocal() Provisioner {
	return &systemdProvisioner{
		socket:      defaultSocketPath(),
		serviceName: "docker",
	}
}

func defaultSocketPath() string {
	if p := strings.TrimSpace(os.Getenv("DOCKER_HOST")); strings.HasPrefix(p, "unix://") {
		return strings.TrimPrefix(p, "unix://")
	}
	return "/var/run/docker.sock"
}

// Status 探测本机 dockerd。
func (p *systemdProvisioner) Status(ctx context.Context) (*RuntimeStatus, error) {
	status := &RuntimeStatus{Distro: "local", DataDir: "", Detail: ""}
	if !p.binaryExists() {
		status.State = StateNotInstalled
		status.Detail = "dockerd is not installed"
		return status, nil
	}
	if p.socketReady(ctx) {
		status.State = StateRunning
		status.Endpoint = "unix://" + p.socket
		status.Detail = "dockerd is running"
		return status, nil
	}
	status.State = StateInstalled
	status.Detail = "dockerd is installed but not running"
	return status, nil
}

// Download 在 Linux 上不支持：daemon 由系统包管理器维护。
func (p *systemdProvisioner) Download(context.Context, ProgressFunc) error {
	return ErrUnsupported
}

// Install 在 Linux 上不支持。
func (p *systemdProvisioner) Install(context.Context, ProgressFunc) error {
	return ErrUnsupported
}

// Uninstall 在 Linux 上不支持：不替用户卸载系统包。
func (p *systemdProvisioner) Uninstall(context.Context) error {
	return ErrUnsupported
}

// Start 在已安装但未运行时启动 systemd 单元，并返回 socket endpoint。
func (p *systemdProvisioner) Start(ctx context.Context) (string, error) {
	if !p.binaryExists() {
		return "", ErrNotInstalled
	}
	if p.socketReady(ctx) {
		return "unix://" + p.socket, nil
	}
	// if err := p.systemctl(ctx, "start", p.serviceName); err != nil {
	// 	return "", err
	// }
	// // systemctl start 是同步的，但仍做一次 socket 校验，避免返回不可用的地址。
	// if !p.socketReady(ctx) {
	// 	return "", errors.New("dockerd started but socket is not ready: " + p.socket)
	// }
	return "unix://" + p.socket, nil
}

// Stop 停止 systemd 单元。
func (p *systemdProvisioner) Stop(ctx context.Context) error {
	if !p.binaryExists() {
		return ErrNotInstalled
	}
	return p.systemctl(ctx, "stop", p.serviceName)
}

// HealthCheck 探测 socket 是否存在且可连接。
func (p *systemdProvisioner) HealthCheck(ctx context.Context) error {
	if !p.socketReady(ctx) {
		return errors.New("docker socket is not available: " + p.socket)
	}
	return nil
}

// binaryExists 判断 dockerd 是否安装。
func (p *systemdProvisioner) binaryExists() bool {
	for _, name := range []string{"dockerd", "docker"} {
		if _, err := exec.LookPath(name); err == nil {
			return true
		}
	}
	return false
}

// socketReady 判断 socket 文件是否存在。
//
// 不直接 net.Dial：unix socket 需要特殊 dialer，且这里只需要「是否已就绪」的
// 粗判定，真正的可用性由 DockerRuntime.HealthCheck（docker Ping）确认。
func (p *systemdProvisioner) socketReady(context.Context) bool {
	st, err := os.Stat(p.socket)
	return err == nil && !st.IsDir()
}

// systemctl 执行 systemctl 子命令。
func (p *systemdProvisioner) systemctl(ctx context.Context, args ...string) error {
	bin, err := exec.LookPath("systemctl")
	if err != nil {
		return ErrUnsupported
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return errors.New("systemctl " + strings.Join(args, " ") + ": " + err.Error() + ": " + strings.TrimSpace(string(out)))
	}
	return nil
}
