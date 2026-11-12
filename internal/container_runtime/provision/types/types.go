// Package types 是 provision 各实现共享的最小类型集合。
//
// 单独成包的原因：provision 包需要「按平台选择实现」，而 Windows 实现（wsl 包）
// 又需要这些类型与接口。若类型定义在 provision 包里，provision 就必须 import
// wsl 才能构造实现，而 wsl 又 import provision，形成 import cycle。
//
// 把接口与数据类型下沉到本包后，依赖方向变为单向：
//
//	provision ──► provision/types ◄── provision/wsl
//
// provision 只负责「选择实现 + 包装」，实现包只依赖 types。
package types

import (
	"context"
	"errors"
	"fmt"
)

// State 是运行时预置状态。
type State string

const (
	// StateNotInstalled 未安装：本地没有发行版（或 Linux 上没有 dockerd）。
	StateNotInstalled State = "not_installed"
	// StateDownloaded 已下载 rootfs 包但尚未导入。
	StateDownloaded State = "downloaded"
	// StateInstalled 已安装，但 daemon 未运行。
	StateInstalled State = "installed"
	// StateRunning 已安装且 daemon 可用。
	StateRunning State = "running"
)

// 预置相关的哨兵错误。调用方用 errors.Is 判断，不要匹配字符串。
var (
	// ErrNotInstalled 表示尚未安装。Start 遇到它必须不做任何修复动作。
	ErrNotInstalled = errors.New("container runtime is not installed")
	// ErrUnsupported 表示当前平台/形态不支持该操作（例如 Linux 上的 Download）。
	ErrUnsupported = errors.New("operation is not supported on this platform")
	// ErrAlreadyRunning 表示目标已经处于运行状态；调用方通常忽略它。
	ErrAlreadyRunning = errors.New("container runtime is already running")
	// ErrNotDownloaded 表示尚未下载 rootfs 包，无法安装。
	ErrNotDownloaded = errors.New("runtime package is not downloaded")
)

// RuntimeStatus 是 UI 需要的全部状态信息。
type RuntimeStatus struct {
	// State 当前状态。
	State State `json:"state"`
	// Distro 发行版名（Windows 上是 WSL 发行版；Linux 上为 "local"）。
	Distro string `json:"distro"`
	// DataDir 预置数据的根目录（<storage.base_dir>/wsl）。
	DataDir string `json:"data_dir"`
	// Endpoint 可用的 daemon 地址，例如 tcp://172.24.1.10:2375；未运行时为空。
	Endpoint string `json:"endpoint"`
	// Version 已安装 rootfs 的版本号（来自 state.json）。
	Version string `json:"version"`
	// DownloadURL 当前配置的 rootfs 下载地址，供 UI 展示。
	DownloadURL string `json:"download_url"`
	// Detail 人类可读的补充信息（daemon 版本、错误原因等）。
	Detail string `json:"detail"`
}

// Progress 表示一次 Download / Install 的进度快照。
type Progress struct {
	// Stage 阶段标识，例如 download / import / configure / start。
	Stage string `json:"stage"`
	// Percent 0-100；无法计算时为 -1。
	Percent int `json:"percent"`
	// Message 人类可读的进度描述。
	Message string `json:"message"`
}

// ProgressFunc 接收进度回调；实现必须允许被并发调用且不得阻塞过久。
type ProgressFunc func(Progress)

// Report 是给各平台实现用的进度上报工具：统一 nil 判断与格式化。
func Report(progress ProgressFunc, stage string, percent int, format string, args ...any) {
	if progress == nil {
		return
	}
	progress(Progress{Stage: stage, Percent: percent, Message: fmt.Sprintf(format, args...)})
}

// Provisioner 是容器运行时生命周期管理的统一接口。
//
// 实现必须满足：
//   - Status 纯只读，不产生副作用；
//   - Start 幂等，未安装时返回 ErrNotInstalled 且不做任何修复；
//   - 所有方法遵守 ctx 取消。
type Provisioner interface {
	// Status 探测当前状态，不产生任何副作用。
	Status(ctx context.Context) (*RuntimeStatus, error)

	// Download 下载 rootfs 包到本地缓存，支持断点续传。
	Download(ctx context.Context, progress ProgressFunc) error

	// Install 导入发行版并安装 / 配置 dockerd；要求已 Download。
	Install(ctx context.Context, progress ProgressFunc) error

	// Uninstall 注销发行版并删除数据目录。
	Uninstall(ctx context.Context) error

	// Start 确保运行时可用并返回 daemon endpoint。
	// 已安装但未运行时负责拉起；未安装时返回 ErrNotInstalled。
	Start(ctx context.Context) (endpoint string, err error)

	// Endpoint 返回当前 daemon 地址；未启动或未解析到时为空。
	Endpoint() string

	// Stop 停止 daemon（不改变安装状态）。
	Stop(ctx context.Context) error

	// HealthCheck 探测 endpoint 是否可连通。
	HealthCheck(ctx context.Context) error
}

// Config 是跨平台构造参数。各平台实现按需取用自己关心的字段。
type Config struct {
	// BaseDir 是 storage.base_dir；Windows 上发行版数据放在 <BaseDir>/wsl。
	BaseDir string
	// DownloadURL rootfs 包下载地址（仅 Windows）。
	DownloadURL string
	// Distro 发行版名（仅 Windows）。
	Distro string
	// Endpoint daemon 地址；为空时由平台实现选择默认值
	// （Windows 上是 tcp://localhost:2375，Linux / macOS 上是本机 unix socket）。
	Endpoint string
}
