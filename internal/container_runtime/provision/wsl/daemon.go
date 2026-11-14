package wsl

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/docker/docker/client"
)

const (
	// daemonReadyInterval 是等待 daemon 就绪的轮询间隔。
	daemonReadyInterval = 1 * time.Second
	// daemonReadyMaxWait 是 ctx 没有截止时间时的兜底等待上限；
	// 与 provision.StartTimeout 保持一致，避免无限等待。
	daemonReadyMaxWait = 3 * time.Minute
)

// stopDaemonScript 通过发行版内置的 systemd 停止 dockerd。
//
// gobrave 不直接操作 dockerd 进程：daemon 的生命周期完全交给 systemd。
const stopDaemonScript = `if command -v systemctl >/dev/null 2>&1; then
  systemctl stop docker.socket docker.service 2>/dev/null || systemctl stop docker 2>/dev/null || true
fi
exit 0`

// stopDaemon 停止发行版内的 dockerd（经 systemd）。
func (p *WSLProvisioner) stopDaemon(ctx context.Context, distro string) error {
	if _, err := runInDistro(ctx, distro, stopDaemonScript); err != nil {
		return fmt.Errorf("stop dockerd in %s: %w", distro, err)
	}
	return nil
}

// probeHostPort 在宿主机上对 endpoint 的 host:port 做一次 TCP 探活。
//
// 「地址是否连通」只看这一件事：不推断 WSL 网络模式，也不读取任何运行时信息。
func probeHostPort(endpoint string, port int) bool {
	host := endpointHost(endpoint)
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
	if err != nil {
		logger.Debugf(context.Background(), "[wsl] probeHostPort: dial %s failed: %v", addr, err)
		return false
	}
	_ = conn.Close()
	return true
}

// endpointHost 取出 endpoint 的主机名；无法解析时回退 localhost。
func endpointHost(endpoint string) string {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || u.Hostname() == "" {
		return "localhost"
	}
	return u.Hostname()
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
