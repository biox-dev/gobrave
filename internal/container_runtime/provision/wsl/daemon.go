package wsl

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/client"
)

// daemonScript 是在发行版内启动 dockerd 的脚本。
//
// 要点：
//   - exec 让 dockerd 成为该 shell 的主进程，WSL 实例因此不会因「无进程」而休眠；
//   - --host 同时保留 unix socket 与 TCP，本地 docker CLI 与 Windows 侧都能用；
//   - nohup + 重定向避免 dockerd 输出占满管道导致阻塞。
const daemonScript = `set -e
mkdir -p /var/log
if pgrep -x dockerd >/dev/null 2>&1; then
  exit 0
fi
nohup dockerd \
  --host=unix:///var/run/docker.sock \
  --host=tcp://127.0.0.1:%d >>%s 2>&1 &
echo $! > %s
`

// distroLogPath / distroPidPath 是发行版内（Linux 视角）的日志与 pid 路径。
const (
	distroRoot       = "/var/lib/gobrave"
	distroLogPath    = distroRoot + "/dockerd.log"
	distroPidPath    = distroRoot + "/dockerd.pid"
	daemonReadyTries = 40
	daemonReadyDelay = 500 * time.Millisecond
)

// daemonRunning 判断发行版内 dockerd 进程是否存活。
func (p *WSLProvisioner) daemonRunning(ctx context.Context, distro string) bool {
	return runInDistroQuiet(ctx, distro, "pgrep -x dockerd >/dev/null 2>&1")
}

// tcpListening 判断发行版内是否已在监听指定端口。
func (p *WSLProvisioner) tcpListening(ctx context.Context, distro string, port int) bool {
	script := fmt.Sprintf("ss -tln 2>/dev/null | grep -q ':%d ' || netstat -tln 2>/dev/null | grep -q ':%d '", port, port)
	return runInDistroQuiet(ctx, distro, script)
}

// startDaemon 在发行版内启动 dockerd，并等待端口就绪。
//
// 已运行则直接返回 nil（幂等）：Start 每次进程启动都会被调用，
// 不能因为重复调用而重启 daemon。
func (p *WSLProvisioner) startDaemon(ctx context.Context, distro string, port int) error {
	if p.daemonRunning(ctx, distro) && p.tcpListening(ctx, distro, port) {
		return nil
	}

	script := fmt.Sprintf("mkdir -p %s\n%v", distroRoot,
		buildStartScript(port))
	if _, err := runInDistro(ctx, distro, script); err != nil {
		return fmt.Errorf("start dockerd in %s: %w", distro, err)
	}

	// 轮询等待 daemon 真正可用，而不是固定 sleep。
	var lastErr error
	for i := 0; i < daemonReadyTries; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if p.tcpListening(ctx, distro, port) {
			return nil
		}
		lastErr = fmt.Errorf("dockerd not listening on :%d yet", port)
		time.Sleep(daemonReadyDelay)
	}
	return fmt.Errorf("dockerd did not become ready: %w", lastErr)
}

// buildStartScript 生成启动 dockerd 的 shell 脚本。
func buildStartScript(port int) string {
	return fmt.Sprintf(daemonScript, port, distroLogPath, distroPidPath)
}

// stopDaemon 停止发行版内的 dockerd。
//
// 先 TERM 让 dockerd 优雅关闭容器，超时后 KILL。忽略「本来就没运行」。
func (p *WSLProvisioner) stopDaemon(ctx context.Context, distro string) error {
	script := `if pgrep -x dockerd >/dev/null 2>&1; then
  pkill -TERM -x dockerd || true
  for i in $(seq 1 30); do
    pgrep -x dockerd >/dev/null 2>&1 || exit 0
    sleep 1
  done
  pkill -KILL -x dockerd || true
fi
exit 0`
	if _, err := runInDistro(ctx, distro, script); err != nil {
		return fmt.Errorf("stop dockerd in %s: %w", distro, err)
	}
	return nil
}

// resolveEndpoint 得出 Windows 侧可用的 daemon 地址。
//
// WSL2 有两种网络模式：
//   - mirrored（Win11 22H2+）：Windows 的 127.0.0.1 可直达发行版环回；
//   - NAT（默认）：必须使用发行版虚拟网卡 IP（hostname -I 首个地址）。
//
// 不依赖配置判断，而是把候选地址逐个探活，第一个通的即为结果：
// 配置可能滞后于实际 WSL 版本，探活才是事实依据。
func (p *WSLProvisioner) resolveEndpoint(ctx context.Context, distro string, port int) (string, error) {
	candidates := make([]string, 0, 2)

	if wslUsesMirroredNetworking() {
		candidates = append(candidates, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	}
	if ip := p.distroIP(ctx, distro); ip != "" {
		candidates = append(candidates, net.JoinHostPort(ip, strconv.Itoa(port)))
	}
	// 兜底：某些版本 mirrored 模式下 127.0.0.1 不可用而 hostname -I 返回异常值。
	candidates = append(candidates, net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))

	seen := map[string]struct{}{}
	var lastErr error
	for _, hostPort := range candidates {
		if _, ok := seen[hostPort]; ok {
			continue
		}
		seen[hostPort] = struct{}{}
		ep := "tcp://" + hostPort
		if err := pingDocker(ctx, ep); err != nil {
			lastErr = err
			continue
		}
		return ep, nil
	}
	return "", fmt.Errorf("dockerd is running in %s but not reachable from windows: %w", distro, lastErr)
}

// distroIP 返回发行版首个网卡 IPv4 地址。
func (p *WSLProvisioner) distroIP(ctx context.Context, distro string) string {
	out, err := runInDistro(ctx, distro, `hostname -I 2>/dev/null | awk '{print $1}'`)
	if err != nil {
		return ""
	}
	ip := strings.TrimSpace(out)
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil {
		return ""
	}
	return ip
}

// pingDocker 用 docker client 的 Ping 探活。
func pingDocker(ctx context.Context, endpoint string) error {
	cli, err := client.NewClientWithOpts(client.WithHost(endpoint), client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("create client for %s: %w", endpoint, err)
	}
	defer cli.Close()

	if _, err := cli.Ping(ctx); err != nil {
		return fmt.Errorf("ping %s: %w", endpoint, err)
	}
	return nil
}

// wslUsesMirroredNetworking 读取 %USERPROFILE%\.wslconfig 判断是否启用 mirrored 网络。
//
// 读不到或没有该键一律按 NAT 处理（保守：NAT 用 IP 连接总是可行的）。
func wslUsesMirroredNetworking() bool {
	home := strings.TrimSpace(os.Getenv("USERPROFILE"))
	if home == "" {
		return false
	}
	data, err := os.ReadFile(filepath.Join(home, ".wslconfig"))
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(key), "networkingMode") {
			return strings.EqualFold(strings.TrimSpace(value), "mirrored")
		}
	}
	return false
}
