package wsl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biox-dev/gobrave/internal/container_runtime/provision/types"
)

// Config 是 WSL 发行版预置的构造参数。
type Config struct {
	// BaseDir 是 storage.base_dir；发行版数据放在 <BaseDir>/wsl。
	BaseDir string
	// DownloadURL rootfs 包下载地址。
	DownloadURL string
	// Distro 发行版名，默认 DefaultDistro。
	Distro string
	// Port dockerd 的 TCP 端口，默认 DefaultDaemonPort。
	Port int
	// HTTPClient 用于下载；nil 时使用内置 client（大文件不设总超时）。
	HTTPClient *http.Client
}

// WSLProvisioner 是 types.Provisioner 在 Windows + WSL 上的实现。
type WSLProvisioner struct {
	cfg    Config
	layout layout

	// mu 保护 endpoint 缓存与「最近一次探测结果」，避免并发调用重复拉起 daemon。
	mu       sync.Mutex
	endpoint string
	lastErr  error
}

// New 构造 WSLProvisioner。
func New(cfg Config) *WSLProvisioner {
	if strings.TrimSpace(cfg.Distro) == "" {
		cfg.Distro = DefaultDistro
	}
	if cfg.Port <= 0 {
		cfg.Port = DefaultDaemonPort
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	return &WSLProvisioner{
		cfg:    cfg,
		layout: newLayout(cfg.BaseDir),
	}
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

		port := p.port(st)
		if p.daemonRunning(ctx, status.Distro) && p.tcpListening(ctx, status.Distro, port) {
			status.State = types.StateRunning
			if ep, err := p.resolveEndpoint(ctx, status.Distro, port); err == nil {
				status.Endpoint = ep
				status.Detail = "dockerd is running"
			} else {
				status.Detail = err.Error()
			}
			return status, nil
		}
		status.State = types.StateInstalled
		status.Detail = "dockerd is not running"
		return status, nil
	}

	if p.layout.downloaded() {
		status.State = types.StateDownloaded
		status.Detail = "rootfs package downloaded, install to finish"
		return status, nil
	}

	// 未安装但系统上已有该发行版（例如用户已装好的 Ubuntu）：
	// 直接安装即可接管，无需下载 rootfs。
	if registered, err := p.distroRegistered(ctx); err == nil && registered {
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
		return errors.New("rootfs download url is not configured")
	}
	if err := p.layout.EnsureDirs(); err != nil {
		return err
	}

	types.Report(progress, "download", 0, "resolving checksum")
	checksum := fetchChecksum(ctx, p.cfg.HTTPClient, url)

	types.Report(progress, "download", 0, "downloading %s", url)
	err := downloadFile(ctx, p.cfg.HTTPClient, url, p.layout.RootfsPath(), checksum,
		func(done, total int64) {
			percent := -1
			if total > 0 {
				percent = int(done * 100 / total)
			}
			types.Report(progress, "download", percent, "downloaded %d / %d bytes", done, total)
		})
	if err != nil {
		return err
	}
	types.Report(progress, "download", 100, "download finished")
	return nil
}

// Install 让发行版就绪并完成 dockerd 初始化。
//
// 两条路径：
//   - 系统上已有同名发行版（Registered）：直接接管，校验/启动 dockerd，不下载不导入；
//   - 否则：要求已 Download，执行 wsl --import 后再初始化。
//
// 导入路径的任何一步失败都会尽力回滚（注销发行版），避免留下半安装状态；
// 接管路径失败时绝不注销用户自己的发行版。
func (p *WSLProvisioner) Install(ctx context.Context, progress types.ProgressFunc) error {
	if p.layout.installed() {
		return nil
	}

	registered, regErr := p.distroRegistered(ctx)
	if regErr != nil {
		return fmt.Errorf("list wsl distros: %w", regErr)
	}
	if registered {
		types.Report(progress, "adopt", 0, "distro %s already installed, starting dockerd directly", p.cfg.Distro)
		return p.adopt(ctx, progress)
	}

	// 原有导入流程。
	if !p.layout.downloaded() {
		return fmt.Errorf("%w: %s", types.ErrNotDownloaded, p.layout.RootfsPath())
	}
	if err := p.layout.EnsureDirs(); err != nil {
		return err
	}

	types.Report(progress, "import", 0, "importing distro %s", p.cfg.Distro)
	// distro 目录必须是空的：wsl --import 要求目标是空目录或不存在。
	if entries, err := os.ReadDir(p.layout.DistroDir()); err == nil && len(entries) > 0 {
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(p.layout.DistroDir(), e.Name()))
		}
	}

	if _, err := runWSL(ctx,
		"--import", p.cfg.Distro, p.layout.DistroDir(), p.layout.RootfsPath(),
		"--version", "2",
	); err != nil {
		return fmt.Errorf("import distro: %w", err)
	}

	rollback := true
	defer func() {
		if rollback {
			_, _ = runWSL(context.WithoutCancel(ctx), "--unregister", p.cfg.Distro)
		}
	}()

	types.Report(progress, "import", 60, "distro imported, initializing")
	if _, err := runInDistro(ctx, p.cfg.Distro, "true"); err != nil {
		return fmt.Errorf("initialize distro %s: %w", p.cfg.Distro, err)
	}

	// 预置镜像内已包含 dockerd；若缺失则明确报错而不是尝试在线安装
	// （在线安装依赖外网与 apt 源，不适合放在安装向导的隐式步骤里）。
	types.Report(progress, "configure", 70, "checking dockerd")
	if out, err := runInDistro(ctx, p.cfg.Distro, "command -v dockerd || exit 127"); err != nil {
		return fmt.Errorf("dockerd not found in distro, the rootfs package is invalid: %w (%s)", err, strings.TrimSpace(out))
	}

	types.Report(progress, "configure", 80, "starting dockerd")
	if err := p.startDaemon(ctx, p.cfg.Distro, p.cfg.Port); err != nil {
		return err
	}

	st := &state{
		Distro:      p.cfg.Distro,
		Version:     p.detectVersion(ctx, p.cfg.Distro),
		Port:        p.cfg.Port,
		InstalledAt: time.Now(),
	}
	if err := writeState(p.layout.StatePath(), st); err != nil {
		return err
	}
	rollback = false

	types.Report(progress, "start", 95, "resolving endpoint")
	ep, err := p.resolveEndpoint(ctx, p.cfg.Distro, p.cfg.Port)
	if err != nil {
		return err
	}
	p.setEndpoint(ep)
	types.Report(progress, "start", 100, "installed, endpoint %s", ep)
	return nil
}

// Uninstall 注销发行版并删除数据目录（保留下载缓存以便重装）。
//
// 接管来的发行版不属于 gobrave：只停止 daemon、清理 state.json 与占位目录，
// 绝不 wsl --unregister 用户自己的发行版，也保留 dockerd 二进制与标记。
func (p *WSLProvisioner) Uninstall(ctx context.Context) error {
	st, _ := readState(p.layout.StatePath())
	if st != nil && st.Adopted {
		_ = p.stopDaemon(ctx, st.Distro)
		if err := os.RemoveAll(p.layout.AdoptPlaceholderDistroDir()); err != nil {
			return fmt.Errorf("remove adopt placeholder dir: %w", err)
		}
		_ = os.Remove(p.layout.StatePath())
		p.setEndpoint("")
		return nil
	}

	if !p.layout.installed() && !p.distroExistsInRegistry(ctx) {
		// 未安装：仍清理残留目录，保证幂等。
		_ = os.RemoveAll(p.layout.DistroDir())
		_ = os.Remove(p.layout.StatePath())
		return nil
	}

	// 先停 daemon，避免注销时进程占用 vhdx。
	_ = p.stopDaemon(ctx, p.cfg.Distro)

	if _, err := runWSL(ctx, "--unregister", p.cfg.Distro); err != nil {
		return fmt.Errorf("unregister distro %s: %w", p.cfg.Distro, err)
	}
	if err := os.RemoveAll(p.layout.DistroDir()); err != nil {
		return fmt.Errorf("remove distro dir: %w", err)
	}
	_ = os.Remove(p.layout.StatePath())
	p.setEndpoint("")
	return nil
}

// Start 确保运行时可用并返回 endpoint。
//
// 这是每次进程启动都会调用的入口，满足以下约束：
//  1. 未安装但系统上已有同名发行版时，直接接管（启动 dockerd）而不是返回 ErrNotInstalled；
//  2. 发行版既未安装也不存在时返回 ErrNotInstalled，不做任何修复；
//  3. 已安装时幂等：daemon 已在跑就只探活，不重复启动。
func (p *WSLProvisioner) Start(ctx context.Context) (string, error) {
	if !p.layout.installed() {
		registered, err := p.distroRegistered(ctx)
		if err != nil {
			return "", fmt.Errorf("list wsl distros: %w", err)
		}
		if !registered {
			return "", types.ErrNotInstalled
		}
		if err := p.adopt(ctx, nil); err != nil {
			p.setErr(err)
			return "", err
		}
	}

	st, err := readState(p.layout.StatePath())
	if err != nil {
		return "", fmt.Errorf("read install state: %w", err)
	}
	distro := p.cfg.Distro
	if strings.TrimSpace(st.Distro) != "" {
		distro = st.Distro
	}
	port := p.port(st)

	// 注册信息可能因用户手动 wsl --unregister 而丢失，这里做一次事实校验。
	registered, err := p.distroRegistered(ctx)
	if err != nil {
		return "", err
	}
	if !registered {
		return "", fmt.Errorf("%w: distro %s is not registered in WSL", types.ErrNotInstalled, distro)
	}

	if err := p.startDaemon(ctx, distro, port); err != nil {
		p.setErr(err)
		return "", err
	}

	ep, err := p.resolveEndpoint(ctx, distro, port)
	if err != nil {
		p.setErr(err)
		return "", err
	}
	p.setEndpoint(ep)
	return ep, nil
}

// Stop 停止 daemon（保持安装状态）。
func (p *WSLProvisioner) Stop(ctx context.Context) error {
	if !p.layout.installed() {
		return types.ErrNotInstalled
	}
	st, _ := readState(p.layout.StatePath())
	distro := p.cfg.Distro
	if st != nil && strings.TrimSpace(st.Distro) != "" {
		distro = st.Distro
	}
	if err := p.stopDaemon(ctx, distro); err != nil {
		return err
	}
	p.setEndpoint("")
	return nil
}

// HealthCheck 探测当前 endpoint。
func (p *WSLProvisioner) HealthCheck(ctx context.Context) error {
	if ep := p.Endpoint(); ep != "" {
		if err := pingDocker(ctx, ep); err == nil {
			return nil
		}
	}
	_, err := p.Start(ctx)
	return err
}

// Endpoint 返回最近一次成功解析的 endpoint（可能为空）。
func (p *WSLProvisioner) Endpoint() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.endpoint
}

func (p *WSLProvisioner) setEndpoint(ep string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.endpoint = ep
	p.lastErr = nil
}

func (p *WSLProvisioner) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastErr = err
}

// port 返回 state 中的端口，缺失时回退配置。
func (p *WSLProvisioner) port(st *state) int {
	if st != nil && st.Port > 0 {
		return st.Port
	}
	return p.cfg.Port
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

// adopt 接管系统上已存在（已注册）的发行版：确保 dockerd 可用后写接管状态。
//
// 前提：调用方已确认 distro 在 wsl -l -q 中（由 Install / Start 负责）。
//
// dockerd 缺失时尝试在发行版内安装（仅 apt 系发行版）；装不上则明确报错，
// 并保留标记文件，使下一次 Start 不必重复安装。
func (p *WSLProvisioner) adopt(ctx context.Context, progress types.ProgressFunc) error {
	if err := p.layout.EnsureAdoptDirs(); err != nil {
		return err
	}

	types.Report(progress, "adopt", 10, "checking distro %s", p.cfg.Distro)
	if _, err := runInDistro(ctx, p.cfg.Distro, "true"); err != nil {
		return fmt.Errorf("distro %s cannot be started: %w", p.cfg.Distro, err)
	}

	types.Report(progress, "adopt", 30, "checking dockerd")
	if !runInDistroQuiet(ctx, p.cfg.Distro, "command -v dockerd >/dev/null 2>&1") {
		types.Report(progress, "adopt", 40, "dockerd not found, installing docker.io")
		if _, err := runInDistro(ctx, p.cfg.Distro, installDockerScript); err != nil {
			return fmt.Errorf("install dockerd in %s: %w (install docker manually and retry)", p.cfg.Distro, err)
		}
	}

	types.Report(progress, "adopt", 75, "starting dockerd")
	if err := p.startDaemon(ctx, p.cfg.Distro, p.cfg.Port); err != nil {
		return err
	}

	if _, err := runInDistro(ctx, p.cfg.Distro, "echo \"$(date -Is) port="+strconv.Itoa(p.cfg.Port)+"\" > "+managedMarkerPath); err != nil {
		return fmt.Errorf("write managed marker in %s: %w", p.cfg.Distro, err)
	}

	st := &state{
		Distro:      p.cfg.Distro,
		Version:     p.detectVersion(ctx, p.cfg.Distro),
		Port:        p.cfg.Port,
		InstalledAt: time.Now(),
		Adopted:     true,
	}
	if err := writeState(p.layout.StatePath(), st); err != nil {
		return err
	}

	types.Report(progress, "adopt", 95, "resolving endpoint")
	ep, err := p.resolveEndpoint(ctx, p.cfg.Distro, p.cfg.Port)
	if err != nil {
		return err
	}
	p.setEndpoint(ep)
	types.Report(progress, "adopt", 100, "adopted %s, endpoint %s", p.cfg.Distro, ep)
	return nil
}

// installDockerScript 在发行版内安装 dockerd（仅 apt 系；非 apt 系直接失败）。
const installDockerScript = `set -e
export DEBIAN_FRONTEND=noninteractive
if command -v apt-get >/dev/null 2>&1; then
  apt-get update -y
  apt-get install -y docker.io
elif command -v dnf >/dev/null 2>&1; then
  dnf install -y docker
elif command -v yum >/dev/null 2>&1; then
  yum install -y docker
else
  echo "unsupported distro: no apt-get/dnf/yum" >&2
  exit 127
fi
command -v dockerd >/dev/null 2>&1`
