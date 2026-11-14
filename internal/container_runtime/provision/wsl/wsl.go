package wsl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/container_runtime/provision/types"
	"github.com/biox-dev/gobrave/internal/logger"
)

// Config 是 WSL 发行版预置的构造参数。
type Config struct {
	// BaseDir 是 storage.base_dir；发行版数据放在 <BaseDir>/wsl。
	BaseDir string
	// DownloadURL rootfs 包下载地址。
	DownloadURL string
	// Distro 发行版名，默认 DefaultDistro。
	Distro string
	// Endpoint daemon 地址；留空表示使用平台默认（DefaultEndpoint）。
	// config 有配置时优先使用配置值。
	Endpoint string
	// HTTPClient 用于下载；nil 时使用内置 client（大文件不设总超时）。
	HTTPClient *http.Client
}

// WSLProvisioner 是 types.Provisioner 在 Windows + WSL 上的实现。
type WSLProvisioner struct {
	cfg    Config
	layout layout

	// endpoint 在构造时由配置（或平台默认）确定，之后不再变化：
	// 启动、停止、卸载都不改写它，避免装配层或其他调用方读到空地址。
	// 与 Linux 实现（systemdProvisioner.endpoint）保持一致。
	endpoint string
}

// New 构造 WSLProvisioner。
func New(cfg Config) *WSLProvisioner {
	if strings.TrimSpace(cfg.Distro) == "" {
		cfg.Distro = DefaultDistro
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	// 不修改 cfg.Endpoint：留空时由 endpoint() 回退默认值。
	endpoint := cfg.endpoint()
	p := &WSLProvisioner{
		cfg:      cfg,
		layout:   newLayout(cfg.BaseDir),
		endpoint: endpoint,
	}
	logger.Infof(context.Background(), "[wsl] provisioner created: base_dir=%s data_dir=%s distro=%s endpoint=%s download_url=%s",
		cfg.BaseDir, p.layout.Root(), cfg.Distro, endpoint, cfg.DownloadURL)
	return p
}

// endpoint 返回显式配置的 daemon 地址；为空时回退 DefaultEndpoint。
//
// 与 Linux 实现一致：显式配置优先，绝不从 WSL 运行时反推地址。
func (c Config) endpoint() string {
	if ep := strings.TrimSpace(c.Endpoint); ep != "" {
		return ep
	}
	return DefaultEndpoint
}

// port 从 Endpoint 解析出 TCP 端口；无法解析时回退默认端口。
//
// 端口只用于启动发行版后探测 daemon 是否就绪，
// 因此 unix:// 等非 TCP 地址一律回退默认值。
func (c Config) port() int {
	u, err := url.Parse(c.endpoint())
	if err != nil || u.Port() == "" {
		return DefaultDaemonPort
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 {
		return DefaultDaemonPort
	}
	return port
}

var _ types.Provisioner = (*WSLProvisioner)(nil)

// Status 探测当前状态，不产生副作用。
//
// 判定顺序：未下载 → 已下载未安装 → 已安装（再看 daemon 是否运行）。
func (p *WSLProvisioner) Status(ctx context.Context) (*types.RuntimeStatus, error) {
	status := &types.RuntimeStatus{
		Distro:      p.cfg.Distro,
		DataDir:     p.layout.Root(),
		DownloadURL: p.cfg.DownloadURL,
	}
	logger.Debugf(ctx, "[wsl] Status: installed=%t downloaded=%t rootfs=%s state=%s",
		p.layout.installed(), p.layout.downloaded(), p.layout.RootfsPath(), p.layout.StatePath())

	// 已安装：以 state.json + distro 目录 + wsl 注册表三重确认。
	if p.layout.installed() {
		st, _ := readState(p.layout.StatePath())
		if st != nil {
			status.Version = st.Version
			if st.Port > 0 {
				status.Distro = st.Distro
			}
		}
		registered, err := p.distroRegistered(ctx)
		if err != nil {
			// WSL 不可用：已安装的事实仍成立，但无法运行。
			status.State = types.StateInstalled
			status.Detail = err.Error()
			return status, nil
		}
		if !registered {
			status.State = types.StateInstalled
			status.Detail = "distro data exists but is not registered in WSL"
			return status, nil
		}

		if err := p.pingEndpoint(ctx); err != nil {
			status.State = types.StateInstalled
			status.Detail = "dockerd is not reachable at " + p.endpoint + ": " + err.Error()
			return status, nil
		}
		status.State = types.StateRunning
		status.Endpoint = p.endpoint
		status.Detail = "dockerd is running"
		return status, nil
	}

	if p.layout.downloaded() {
		status.State = types.StateDownloaded
		status.Detail = "rootfs package downloaded, install to finish"
		return status, nil
	}

	// 未安装（没有 state.json）但系统上已有该发行版（例如用户已装好的 Ubuntu）：
	// 直接安装即可接管，无需下载 rootfs。
	registered, regErr := p.distroRegistered(ctx)
	if regErr != nil {
		// WSL 不可用：此时无法判断，但也不能误报「可直接接管」。
		status.State = types.StateNotInstalled
		status.Detail = regErr.Error()
		return status, nil
	}
	if registered {
		status.Distro = p.cfg.Distro
		if err := p.pingEndpoint(ctx); err == nil {
			status.State = types.StateRunning
			status.Endpoint = p.endpoint
			status.Detail = "dockerd is running in existing distro (not yet adopted, click install to adopt)"
			return status, nil
		}
		status.State = types.StateNotInstalled
		status.Detail = fmt.Sprintf("distro %s is already installed in WSL, install will start it directly without downloading", p.cfg.Distro)
		return status, nil
	}

	status.State = types.StateNotInstalled
	return status, nil
}

// Download 下载 rootfs 包（支持断点续传 + sha256 校验）。
func (p *WSLProvisioner) Download(ctx context.Context, progress types.ProgressFunc) error {
	url := strings.TrimSpace(p.cfg.DownloadURL)
	if url == "" {
		logger.Errorf(ctx, "[wsl] Download: rootfs download url is not configured")
		return errors.New("rootfs download url is not configured")
	}
	if err := p.layout.EnsureDirs(); err != nil {
		logger.Errorf(ctx, "[wsl] Download: ensure dirs %s failed: %v", p.layout.Root(), err)
		return err
	}

	logger.Infof(ctx, "[wsl] Download: url=%s dst=%s", url, p.layout.RootfsPath())
	types.Report(progress, "download", 0, "resolving checksum")
	checksum := fetchChecksum(ctx, p.cfg.HTTPClient, url)
	if checksum == "" {
		logger.Warnf(ctx, "[wsl] Download: no checksum available for %s, skipping verification", url)
	} else {
		logger.Infof(ctx, "[wsl] Download: expecting sha256=%s", checksum)
	}

	types.Report(progress, "download", 0, "downloading %s", url)
	start := time.Now()
	err := downloadFile(ctx, p.cfg.HTTPClient, url, p.layout.RootfsPath(), checksum,
		func(done, total int64) {
			percent := -1
			if total > 0 {
				percent = int(done * 100 / total)
			}
			types.Report(progress, "download", percent, "downloaded %d / %d bytes", done, total)
		})
	if err != nil {
		logger.Errorf(ctx, "[wsl] Download: failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
		return err
	}
	logger.Infof(ctx, "[wsl] Download: finished in %s -> %s", time.Since(start).Round(time.Millisecond), p.layout.RootfsPath())
	types.Report(progress, "download", 100, "download finished")
	return nil
}

// Install 让发行版就绪。
//
// 两条路径：
//   - 系统上已有同名发行版（Registered）：直接接管，不下载不导入；
//   - 否则：要求已 Download，执行 wsl --import。
//
// 导入路径的任何一步失败都会尽力回滚（注销发行版），避免留下半安装状态；
// 接管路径失败时绝不注销用户自己的发行版。
func (p *WSLProvisioner) Install(ctx context.Context, progress types.ProgressFunc) error {
	logger.Infof(ctx, "[wsl] Install: begin, distro=%s installed=%t downloaded=%t",
		p.cfg.Distro, p.layout.installed(), p.layout.downloaded())
	if p.layout.installed() {
		logger.Infof(ctx, "[wsl] Install: already installed, nothing to do")
		return nil
	}

	registered, regErr := p.distroRegistered(ctx)
	if regErr != nil {
		logger.Errorf(ctx, "[wsl] Install: list wsl distros failed: %v", regErr)
		return fmt.Errorf("list wsl distros: %w", regErr)
	}
	if registered {
		logger.Infof(ctx, "[wsl] Install: distro %s already registered in WSL, adopting instead of importing", p.cfg.Distro)
		types.Report(progress, "adopt", 0, "distro %s already installed, adopting", p.cfg.Distro)
		return p.adopt(ctx, progress)
	}

	// 原有导入流程。
	if !p.layout.downloaded() {
		logger.Errorf(ctx, "[wsl] Install: rootfs not downloaded: %s", p.layout.RootfsPath())
		return fmt.Errorf("%w: %s", types.ErrNotDownloaded, p.layout.RootfsPath())
	}
	if err := p.layout.EnsureDirs(); err != nil {
		logger.Errorf(ctx, "[wsl] Install: ensure dirs failed: %v", err)
		return err
	}

	types.Report(progress, "import", 0, "importing distro %s", p.cfg.Distro)
	// distro 目录必须是空的：wsl --import 要求目标是空目录或不存在。
	if entries, err := os.ReadDir(p.layout.DistroDir()); err == nil && len(entries) > 0 {
		logger.Infof(ctx, "[wsl] Install: clearing %d existing entries in %s", len(entries), p.layout.DistroDir())
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(p.layout.DistroDir(), e.Name()))
		}
	}

	logger.Infof(ctx, "[wsl] Install: importing distro %s from %s into %s",
		p.cfg.Distro, p.layout.RootfsPath(), p.layout.DistroDir())
	if _, err := runWSL(ctx,
		"--import", p.cfg.Distro, p.layout.DistroDir(), p.layout.RootfsPath(),
		"--version", "2",
	); err != nil {
		logger.Errorf(ctx, "[wsl] Install: import distro failed: %v", err)
		return fmt.Errorf("import distro: %w", err)
	}

	rollback := true
	defer func() {
		if rollback {
			logger.Warnf(ctx, "[wsl] Install: rolling back by unregistering distro %s", p.cfg.Distro)
			_, _ = runWSL(context.WithoutCancel(ctx), "--unregister", p.cfg.Distro)
		}
	}()

	types.Report(progress, "configure", 60, "writing install state")
	st := &state{
		Distro:      p.cfg.Distro,
		Version:     p.detectVersion(ctx, p.cfg.Distro),
		Port:        p.cfg.port(),
		InstalledAt: time.Now(),
	}
	if err := writeState(p.layout.StatePath(), st); err != nil {
		logger.Errorf(ctx, "[wsl] Install: write state %s failed: %v", p.layout.StatePath(), err)
		return err
	}
	rollback = false
	logger.Infof(ctx, "[wsl] Install: state written to %s (version=%q)", p.layout.StatePath(), st.Version)

	types.Report(progress, "start", 80, "booting distro")
	if ep, err := p.Start(ctx); err != nil {
		return err
	} else {
		types.Report(progress, "start", 100, "installed, endpoint %s", ep)
	}
	return nil
}

// Uninstall 注销发行版并删除数据目录（保留下载缓存以便重装）。
//
// 接管来的发行版不属于 gobrave：只停止 daemon、清理 state.json 与占位目录，
// 绝不 wsl --unregister 用户自己的发行版，也保留 dockerd 二进制与标记。
func (p *WSLProvisioner) Uninstall(ctx context.Context) error {
	st, _ := readState(p.layout.StatePath())
	logger.Infof(ctx, "[wsl] Uninstall: begin, distro=%s installed=%t state=%+v",
		p.cfg.Distro, p.layout.installed(), st)
	if st != nil && st.Adopted {
		logger.Infof(ctx, "[wsl] Uninstall: distro %s was adopted, stopping daemon only (will NOT unregister)", st.Distro)
		_ = p.stopDaemon(ctx, st.Distro)
		// 发行版仍需终止，否则 vhdx 一直被占用。
		_, _ = runWSL(ctx, "--terminate", st.Distro)
		if err := os.RemoveAll(p.layout.AdoptPlaceholderDistroDir()); err != nil {
			logger.Errorf(ctx, "[wsl] Uninstall: remove adopt placeholder dir failed: %v", err)
			return fmt.Errorf("remove adopt placeholder dir: %w", err)
		}
		_ = os.Remove(p.layout.StatePath())
		logger.Infof(ctx, "[wsl] Uninstall: adopted distro released")
		return nil
	}

	if !p.layout.installed() && !p.distroExistsInRegistry(ctx) {
		// 未安装：仍清理残留目录，保证幂等。
		logger.Infof(ctx, "[wsl] Uninstall: not installed and not registered, cleaning leftovers only")
		_ = os.RemoveAll(p.layout.DistroDir())
		_ = os.Remove(p.layout.StatePath())
		return nil
	}

	// 先停 daemon，避免注销时进程占用 vhdx。
	logger.Infof(ctx, "[wsl] Uninstall: stopping daemon in %s", p.cfg.Distro)
	_ = p.stopDaemon(ctx, p.cfg.Distro)
	_, _ = runWSL(ctx, "--terminate", p.cfg.Distro)

	logger.Infof(ctx, "[wsl] Uninstall: unregistering distro %s", p.cfg.Distro)
	if _, err := runWSL(ctx, "--unregister", p.cfg.Distro); err != nil {
		logger.Errorf(ctx, "[wsl] Uninstall: unregister distro %s failed: %v", p.cfg.Distro, err)
		return fmt.Errorf("unregister distro %s: %w", p.cfg.Distro, err)
	}
	if err := os.RemoveAll(p.layout.DistroDir()); err != nil {
		logger.Errorf(ctx, "[wsl] Uninstall: remove distro dir %s failed: %v", p.layout.DistroDir(), err)
		return fmt.Errorf("remove distro dir: %w", err)
	}
	_ = os.Remove(p.layout.StatePath())
	logger.Infof(ctx, "[wsl] Uninstall: done")
	return nil
}

// Start 确保运行时可用并返回 endpoint。
//
// 这是每次进程启动都会调用的入口，满足以下约束：
//  1. 未安装但系统上已有同名发行版时，直接接管（启动发行版）而不是返回 ErrNotInstalled；
//  2. 发行版既未安装也不存在时返回 ErrNotInstalled，不做任何修复；
//  3. 已安装时幂等：启动发行版后只探测 endpoint 是否连通。
func (p *WSLProvisioner) Start(ctx context.Context) (string, error) {
	logger.Infof(ctx, "[wsl] Start: begin, installed=%t distro=%s", p.layout.installed(), p.cfg.Distro)
	if !p.layout.installed() {
		registered, err := p.distroRegistered(ctx)
		if err != nil {
			logger.Errorf(ctx, "[wsl] Start: list wsl distros failed: %v", err)
			return "", fmt.Errorf("list wsl distros: %w", err)
		}
		if !registered {
			logger.Warnf(ctx, "[wsl] Start: distro %s neither installed nor registered, returning ErrNotInstalled", p.cfg.Distro)
			return "", types.ErrNotInstalled
		}
		logger.Infof(ctx, "[wsl] Start: distro %s exists in WSL but not adopted, adopting", p.cfg.Distro)
		if err := p.adopt(ctx, nil); err != nil {
			logger.Errorf(ctx, "[wsl] Start: adopt failed: %v", err)
			return "", err
		}
	}

	st, err := readState(p.layout.StatePath())
	if err != nil {
		logger.Errorf(ctx, "[wsl] Start: read install state %s failed: %v", p.layout.StatePath(), err)
		return "", fmt.Errorf("read install state: %w", err)
	}
	distro := p.cfg.Distro
	if strings.TrimSpace(st.Distro) != "" {
		distro = st.Distro
	}
	port := p.port(st)
	logger.Infof(ctx, "[wsl] Start: state resolved, distro=%s port=%d adopted=%t", distro, port, st.Adopted)

	// 注册信息可能因用户手动 wsl --unregister 而丢失，这里做一次事实校验。
	registered, err := p.distroRegistered(ctx)
	if err != nil {
		logger.Errorf(ctx, "[wsl] Start: verify registration failed: %v", err)
		return "", err
	}
	if !registered {
		logger.Errorf(ctx, "[wsl] Start: distro %s is not registered in WSL anymore", distro)
		return "", fmt.Errorf("%w: distro %s is not registered in WSL", types.ErrNotInstalled, distro)
	}

	// 只负责「启动发行版」：dockerd 由发行版自己维护，这里不执行 docker 命令。
	if err := p.bootDistro(ctx, distro); err != nil {
		logger.Errorf(ctx, "[wsl] Start: bootDistro failed: %v", err)
		return "", err
	}

	ep := p.endpoint
	if err := p.waitEndpoint(ctx, ep, port); err != nil {
		logger.Errorf(ctx, "[wsl] Start: endpoint %s not reachable: %v", ep, err)
		return "", err
	}
	logger.Infof(ctx, "[wsl] Start: ready, endpoint=%s", ep)
	return ep, nil
}

// Stop 停止 daemon（保持安装状态）。
func (p *WSLProvisioner) Stop(ctx context.Context) error {
	if !p.layout.installed() {
		logger.Warnf(ctx, "[wsl] Stop: distro %s not installed", p.cfg.Distro)
		return types.ErrNotInstalled
	}
	st, _ := readState(p.layout.StatePath())
	distro := p.cfg.Distro
	if st != nil && strings.TrimSpace(st.Distro) != "" {
		distro = st.Distro
	}
	logger.Infof(ctx, "[wsl] Stop: stopping dockerd via systemd in %s", distro)
	if err := p.stopDaemon(ctx, distro); err != nil {
		logger.Errorf(ctx, "[wsl] Stop: stopDaemon failed: %v", err)
		return err
	}
	logger.Infof(ctx, "[wsl] Stop: done")
	return nil
}

// HealthCheck 探测当前 endpoint（与 Linux 实现一致：只探活，不注入副作用）。
func (p *WSLProvisioner) HealthCheck(ctx context.Context) error {
	if err := p.pingEndpoint(ctx); err != nil {
		return err
	}
	return nil
}

// pingEndpoint 对配置的 daemon 地址做一次 docker Ping。
func (p *WSLProvisioner) pingEndpoint(ctx context.Context) error {
	return pingDocker(ctx, p.endpoint)
}

// waitEndpoint 轮询等待配置的 daemon 地址可用。
//
// port > 0 时先做一次宿主机 TCP 探活（快速失败），再 ping docker 协议。
// 每 1s 轮询一次；等待总时长由 ctx 截止时间决定（StartAsync 默认 3 分钟），
// ctx 没有截止时间时以 daemonReadyMaxWait 兜底。
func (p *WSLProvisioner) waitEndpoint(ctx context.Context, endpoint string, port int) error {
	deadline := time.Now().Add(daemonReadyMaxWait)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	var lastErr error
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if port > 0 && !probeHostPort(endpoint, port) {
			lastErr = fmt.Errorf("%s is not reachable from host", endpoint)
		} else if err := pingDocker(ctx, endpoint); err != nil {
			lastErr = err
		} else {
			return nil
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if remaining > daemonReadyInterval {
			remaining = daemonReadyInterval
		}
		logger.Infof(ctx, "[wsl] waitEndpoint: %s not ready yet (try %d): %v", endpoint, attempt, lastErr)

		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("dockerd is not reachable at %s: %w", endpoint, lastErr)
}

// bootDistro 只启动发行版本身。
//
// 发行版内的 dockerd 由它自己维护（systemd 等），gobrave 不执行任何 docker 命令，
// 启动后仅到宿主机侧探测配置的 endpoint 是否连通。
func (p *WSLProvisioner) bootDistro(ctx context.Context, distro string) error {
	logger.Infof(ctx, "[wsl] bootDistro: starting distro %s", distro)
	if _, err := runInDistro(ctx, distro, "true"); err != nil {
		return fmt.Errorf("boot distro %s: %w", distro, err)
	}
	return nil
}

// Endpoint 返回 daemon 地址。
//
// 地址在构造时由配置（含默认值）确定，之后恒定返回同一个值：
// Stop / Uninstall 不清空，调用方任何时候读到的都是本实例配置的地址。
// 与 Linux 实现一致：不从 WSL 运行时反推地址。
func (p *WSLProvisioner) Endpoint() string {
	return p.endpoint
}

// port 返回配置中的端口（显式配置优先）。
//
// state.json 中的旧端口不再覆盖配置：修改 endpoint 后无需删除 state 即可生效。
func (p *WSLProvisioner) port(*state) int {
	return p.cfg.port()
}

// distroRegistered 判断发行版是否存在于 WSL 注册表中。
func (p *WSLProvisioner) distroRegistered(ctx context.Context) (bool, error) {
	names, err := listDistros(ctx)
	if err != nil {
		return false, fmt.Errorf("list wsl distros: %w", err)
	}
	return containsDistro(names, p.cfg.Distro), nil
}

// distroExistsInRegistry 是 distroRegistered 的宽松版（错误视为不存在）。
func (p *WSLProvisioner) distroExistsInRegistry(ctx context.Context) bool {
	ok, err := p.distroRegistered(ctx)
	return err == nil && ok
}

// detectVersion 读取发行版标识作为版本号（用于 UI 展示）。
func (p *WSLProvisioner) detectVersion(ctx context.Context, distro string) string {
	if out, err := runInDistro(ctx, distro, `(. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME") || uname -r`); err == nil {
		if v := strings.TrimSpace(out); v != "" {
			return v
		}
	}
	return time.Now().Format("2006-01-02")
}

// adopt 接管系统上已存在（已注册）的发行版：启动它并等待 endpoint 可用。
//
// 前提：调用方已确认 distro 在 wsl -l -q 中（由 Install / Start 负责）。
// gobrave 不安装 / 启动 dockerd：发行版内的 dockerd 由它自己维护。
func (p *WSLProvisioner) adopt(ctx context.Context, progress types.ProgressFunc) error {
	logger.Infof(ctx, "[wsl] adopt: begin, distro=%s", p.cfg.Distro)
	if err := p.layout.EnsureAdoptDirs(); err != nil {
		logger.Errorf(ctx, "[wsl] adopt: ensure adopt dirs failed: %v", err)
		return err
	}

	types.Report(progress, "adopt", 30, "booting distro %s", p.cfg.Distro)
	if err := p.bootDistro(ctx, p.cfg.Distro); err != nil {
		return err
	}

	st := &state{
		Distro:      p.cfg.Distro,
		Version:     p.detectVersion(ctx, p.cfg.Distro),
		Port:        p.cfg.port(),
		InstalledAt: time.Now(),
		Adopted:     true,
	}
	if err := writeState(p.layout.StatePath(), st); err != nil {
		logger.Errorf(ctx, "[wsl] adopt: write state %s failed: %v", p.layout.StatePath(), err)
		return err
	}
	logger.Infof(ctx, "[wsl] adopt: state written to %s (version=%q)", p.layout.StatePath(), st.Version)

	types.Report(progress, "adopt", 75, "waiting for endpoint")
	ep := p.endpoint
	if err := p.waitEndpoint(ctx, ep, st.Port); err != nil {
		return err
	}
	logger.Infof(ctx, "[wsl] adopt: done, endpoint=%s", ep)
	types.Report(progress, "adopt", 100, "adopted %s, endpoint %s", p.cfg.Distro, ep)
	return nil
}
