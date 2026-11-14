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
	// daemonReadyMaxWait 是等待 daemon 就绪的总时长上限（与 ctx 截止时间取较早者）；
	// 与 provision.StartTimeout 保持一致，避免无限等待。
	daemonReadyMaxWait = 3 * time.Minute
	// probeHostPortTimeout 是单次宿主机 TCP 探活的超时。
	probeHostPortTimeout = 500 * time.Millisecond
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

// probeHostPort 在宿主机上对 addr（host:port）做一次 TCP 探活。
//
// 「地址是否连通」只看这一件事：不推断 WSL 网络模式，也不读取任何运行时信息。
func probeHostPort(ctx context.Context, addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, probeHostPortTimeout)
	if err != nil {
		logger.Debugf(ctx, "[wsl] probeHostPort: dial %s failed: %v", addr, err)
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

// endpointAddr 把 tcp:// endpoint 解析成 host:port；非 TCP 地址返回空串。
//
// 端口取自 endpoint 自身，与 Config.port() 同源，避免两处端口不一致。
func endpointAddr(endpoint string) string {
	u, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || !strings.EqualFold(u.Scheme, "tcp") {
		return ""
	}
	port := u.Port()
	if port == "" {
		port = strconv.Itoa(DefaultDaemonPort)
	}
	return net.JoinHostPort(endpointHost(endpoint), port)
}

// newDockerClient 构造指向 endpoint 的 docker client，调用方负责 Close。
func newDockerClient(endpoint string) (*client.Client, error) {
	cli, err := client.NewClientWithOpts(client.WithHost(endpoint), client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create client for %s: %w", endpoint, err)
	}
	return cli, nil
}

// pingDocker 用一次性 docker client 做 Ping 探活。
func pingDocker(ctx context.Context, endpoint string) error {
	cli, err := newDockerClient(endpoint)
	if err != nil {
		return err
	}
	defer cli.Close()
	return pingDockerWith(ctx, cli, endpoint)
}

// pingDockerWith 用已有 client 做 Ping 探活，轮询时复用连接池。
func pingDockerWith(ctx context.Context, cli *client.Client, endpoint string) error {
	if _, err := cli.Ping(ctx); err != nil {
		return fmt.Errorf("ping %s: %w", endpoint, err)
	}
	return nil
}
