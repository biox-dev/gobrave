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

	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/docker/docker/client"
)

// daemonScript 是在发行版内启动 dockerd 的脚本。
//
// 要点：
//   - exec 让 dockerd 成为该 shell 的主进程，WSL 实例因此不会因「无进程」而休眠；
//   - --host 同时保留 unix socket 与 TCP，本地 docker CLI 与 Windows 侧都能用；
//   - **TCP 必须绑 0.0.0.0 而不是 127.0.0.1**：WSL2 默认 NAT 模式下，Windows 侧
//     访问的是发行版虚拟网卡 IP（hostname -I 第一个地址），该流量从 eth0 进入，
//     绑在 127.0.0.1 上的监听根本收不到，表现就是「发行版内 dockerd 正常，
//     Windows 连接 127.0.0.1:2375 被拒绝」。绑 0.0.0.0 后 NAT 与 mirrored
//     两种模式都能连（mirrored 下 127.0.0.1 也可直达）。
//     发行版位于 WSL 私有虚拟网络内，不对局域网暴露。
//   - nohup + 重定向避免 dockerd 输出占满管道导致阻塞。
const daemonScript = `set -e
mkdir -p /var/log %s
if pgrep -x dockerd >/dev/null 2>&1; then
  exit 0
fi
nohup dockerd \
  --host=unix:///var/run/docker.sock \
  --host=tcp://0.0.0.0:%d >>%s 2>&1 &
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
	logger.Infof(ctx, "[wsl] startDaemon: distro=%s port=%d", distro, port)
	if p.daemonRunning(ctx, distro) && p.tcpListening(ctx, distro, port) {
		logger.Infof(ctx, "[wsl] startDaemon: dockerd already running in %s and listening on :%d, skip", distro, port)
		return nil
	}

	script := fmt.Sprintf("mkdir -p %s\n%v", distroRoot,
		buildStartScript(port))
	logger.Infof(ctx, "[wsl] startDaemon: launching dockerd in %s, script: %s", distro, compactScript(script))
	if _, err := runInDistro(ctx, distro, script); err != nil {
		logger.Errorf(ctx, "[wsl] startDaemon: start dockerd in %s failed: %v", distro, err)
		return fmt.Errorf("start dockerd in %s: %w", distro, err)
	}

	// 轮询等待 daemon 真正可用，而不是固定 sleep。
	// 跨进程观察：发行版内执行 ss（同一网络命名空间），Windows 侧再做一次
	// TCP 探活（验证 NAT 转发与监听地址，避免「发行版内通、Windows 不通」）。
	var lastErr error
	for i := 0; i < daemonReadyTries; i++ {
		select {
		case <-ctx.Done():
			logger.Warnf(ctx, "[wsl] startDaemon: context done while waiting dockerd in %s (try %d/%d): %v", distro, i+1, daemonReadyTries, ctx.Err())
			return ctx.Err()
		default:
		}
		listening := p.tcpListening(ctx, distro, port)
		if listening && p.probeFromWindows(ctx, distro, port) {
			logger.Infof(ctx, "[wsl] startDaemon: dockerd ready in %s on :%d after %d tries", distro, port, i+1)
			return nil
		}
		lastErr = fmt.Errorf("dockerd not reachable on :%d yet", port)
		logger.Debugf(ctx, "[wsl] startDaemon: not ready yet (try %d/%d), listening_in_distro=%t", i+1, daemonReadyTries, listening)
		time.Sleep(daemonReadyDelay)
	}
	logger.Errorf(ctx, "[wsl] startDaemon: dockerd did not become ready in %s after %d tries: %v", distro, daemonReadyTries, lastErr)
	return fmt.Errorf("dockerd did not become ready: %w", lastErr)
}

// buildStartScript 生成启动 dockerd 的 shell 脚本。
func buildStartScript(port int) string {
	return fmt.Sprintf(daemonScript, distroRoot, port, distroLogPath, distroPidPath)
}

// probeFromWindows 在本进程（Windows）侧探测 dockerd 端口是否可连。
//
// 必须遍历候选地址：NAT 模式下 Windows 的 127.0.0.1:<port> 永远不通，
// 唯一可用地址是发行版网卡 IP；只探 127.0.0.1 会让就绪判定「永远失败」
// 而白白耗光轮询次数。任一候选可达即通过。
//
// 只需建立 TCP 连接：这里验证的是「网络可达」，docker 协议探活由 resolveEndpoint 负责。
func (p *WSLProvisioner) probeFromWindows(ctx context.Context, distro string, port int) bool {
	candidates := p.endpointCandidatesFromWindows(ctx, distro, port)
	for _, host := range candidates {
		conn, err := net.DialTimeout("tcp", host, 500*time.Millisecond)
		if err != nil {
			logger.Debugf(ctx, "[wsl] probeFromWindows: dial %s failed: %v", host, err)
			continue
		}
		_ = conn.Close()
		logger.Debugf(ctx, "[wsl] probeFromWindows: dial %s ok", host)
		return true
	}
	logger.Debugf(ctx, "[wsl] probeFromWindows: none reachable, candidates=%v", candidates)
	return false
}

// endpointCandidatesFromWindows 列出 Windows 侧可能连通的 host:port，按可靠性排序。
//
// mirrored 模式下 127.0.0.1 直达发行版环回，排在最前；NAT 模式下排在后面
// （探活失败只多花 500ms，不影响正确性）。发行版 IP 为空（命令失败）时跳过。
func (p *WSLProvisioner) endpointCandidatesFromWindows(ctx context.Context, distro string, port int) []string {
	portStr := strconv.Itoa(port)
	loopback := net.JoinHostPort("127.0.0.1", portStr)
	distroHost := ""
	if ip := p.distroIP(ctx, distro); ip != "" {
		distroHost = net.JoinHostPort(ip, portStr)
	}

	if wslUsesMirroredNetworking() {
		if distroHost == "" {
			return []string{loopback}
		}
		return []string{loopback, distroHost}
	}
	if distroHost == "" {
		return []string{loopback}
	}
	return []string{distroHost, loopback}
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
	var candidates []string
	for _, host := range p.endpointCandidatesFromWindows(ctx, distro, port) {
		candidates = append(candidates, "tcp://"+host)
	}
	logger.Infof(ctx, "[wsl] resolveEndpoint: distro=%s port=%d candidates=%v", distro, port, candidates)

	seen := map[string]struct{}{}
	var lastErr error
	for _, ep := range candidates {
		if _, ok := seen[ep]; ok {
			continue
		}
		seen[ep] = struct{}{}
		if err := pingDocker(ctx, ep); err != nil {
			logger.Debugf(ctx, "[wsl] resolveEndpoint: ping %s failed: %v", ep, err)
			lastErr = err
			continue
		}
		logger.Infof(ctx, "[wsl] resolveEndpoint: using %s", ep)
		return ep, nil
	}
	logger.Errorf(ctx, "[wsl] resolveEndpoint: no reachable endpoint for %s: %v", distro, lastErr)
	return "", fmt.Errorf("dockerd is running in %s but not reachable from windows: %w", distro, lastErr)
}

// distroIP 返回发行版首个网卡 IPv4 地址。
//
// 优先 `ip -4 -o addr show`：输出带接口名，可以确定性挑出 eth0 并跳过
// docker0（172.17.0.1）/ loopback。`hostname -I` 的地址顺序在多网卡或
// 双栈环境下不稳定，只作为没有 iproute2 时的兜底。
func (p *WSLProvisioner) distroIP(ctx context.Context, distro string) string {
	// ip -4 -o addr show → "2: eth0    inet 172.27.140.255/18 brd ... scope global eth0"
	const ipScript = `ip -4 -o addr show 2>/dev/null | awk '$2!="lo:" && $2!="docker0:" {print $2, $4}'`
	if out, err := runInDistro(ctx, distro, ipScript); err == nil {
		if ip := pickDistroIP(out, "eth0"); ip != "" {
			logger.Debugf(ctx, "[wsl] distroIP: %s -> %s (from ip -4 -o addr)", distro, ip)
			return ip
		}
	}

	if out, err := runInDistro(ctx, distro, `hostname -I 2>/dev/null`); err == nil {
		if ip := pickDistroIP(out, ""); ip != "" {
			logger.Debugf(ctx, "[wsl] distroIP: %s -> %s (from hostname -I)", distro, ip)
			return ip
		}
	}
	logger.Warnf(ctx, "[wsl] distroIP: could not determine IPv4 for distro %s", distro)
	return ""
}

// pickDistroIP 从「接口名 CIDR」列表里选出可用 IPv4。
//
// preferIface 非空时优先该接口；无论是否命中，都跳过 lo / docker* / br-* 网桥，
// 因为它们对 Windows 侧不可达。
func pickDistroIP(out, preferIface string) string {
	if preferIface != "" {
		if ip := scanIPv4(out, preferIface, true); ip != "" {
			return ip
		}
	}
	return scanIPv4(out, preferIface, false)
}

// scanIPv4 解析「接口名 CIDR」逐行输出（ip -4 -o addr show），返回首个命中 IPv4。
//
// onlyPreferred 为 true 时只接受 preferIface 接口，否则只接受其它接口。
// 同时兼容 hostname -I 的「一行多个纯 IP」形态（无接口名，只在非 preferred 轮返回）。
func scanIPv4(out, preferIface string, onlyPreferred bool) string {
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 {
			continue
		}

		// 形态一：<iface>: <cidr> [more...]（ip -4 -o addr show）
		if len(fields) >= 2 && strings.Contains(fields[1], "/") {
			iface := strings.TrimSuffix(fields[0], ":")
			if isInternalIface(iface) {
				continue
			}
			if preferIface != "" && strings.EqualFold(iface, preferIface) != onlyPreferred {
				continue
			}
			if ip := firstIPv4(fields[1]); ip != "" {
				return ip
			}
			continue
		}

		// 形态二：纯 IP 列表（hostname -I），没有接口信息，只在非 preferred 轮使用。
		if onlyPreferred {
			continue
		}
		for _, f := range fields {
			if ip := firstIPv4(f); ip != "" {
				return ip
			}
		}
	}
	return ""
}

// firstIPv4 从 "172.27.140.255/18" 或 "172.27.140.255" 里取出非回环 IPv4。
func firstIPv4(token string) string {
	ip, _, _ := strings.Cut(strings.TrimSpace(token), "/")
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil || parsed.IsLoopback() {
		return ""
	}
	return ip
}

// isInternalIface 判断接口是否属于不该暴露给 Windows 的内部网桥。
func isInternalIface(name string) bool {
	n := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), ":"))
	if n == "" {
		return false
	}
	if n == "lo" || n == "docker0" {
		return true
	}
	return strings.HasPrefix(n, "docker") || strings.HasPrefix(n, "br-") || strings.HasPrefix(n, "veth")
}

// DistroIP 返回发行版的虚拟网卡地址（供装配层把容器发布端口映射成 Windows 可访问地址）。
//
// 故意不依赖 docker 容器 IP（172.17.x.x）：那是发行版内部的 docker0 网桥，
// Windows 根本无法路由。容器端口必须发布到发行版网卡（-p 0.0.0.0:port）
// 后再通过 Windows → 发行版 IP 访问。
func (p *WSLProvisioner) DistroIP(ctx context.Context) string {
	return p.distroIP(ctx, p.cfg.Distro)
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
