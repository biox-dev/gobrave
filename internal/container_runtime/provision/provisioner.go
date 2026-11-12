// Package provision 统一管理容器运行时的「安装 / 启动」生命周期。
//
// 设计动机见 docs/container-runtime-provisioner.md。要点：
//
//   - 每次进程启动只调用一次 Start：已安装则确保 daemon 可用并返回 endpoint，
//     未安装则返回 ErrNotInstalled 且不产生任何副作用；
//   - Download / Install / Uninstall 只由安装向导（UI）触发；
//   - 接口在 Windows（WSL 发行版）与 Linux（本机 dockerd）上保持一致，
//     上层装配与 HTTP 接口无需区分平台。
//
// 具体类型定义在 provision/types 子包，这里用类型别名再导出，
// 调用方只需 import provision 一个包。
package provision

import (
	"github.com/biox-dev/gobrave/internal/container_runtime/provision/types"
)

// 类型别名：让调用方只依赖 provision 包。
type (
	// State 运行时预置状态。
	State = types.State
	// RuntimeStatus UI 需要的状态信息。
	RuntimeStatus = types.RuntimeStatus
	// Progress 一次 Download / Install 的进度快照。
	Progress = types.Progress
	// ProgressFunc 进度回调。
	ProgressFunc = types.ProgressFunc
	// Provisioner 容器运行时生命周期接口。
	Provisioner = types.Provisioner
	// Config 跨平台构造参数。
	Config = types.Config
)

// 状态常量别名。
const (
	StateNotInstalled = types.StateNotInstalled
	StateDownloaded   = types.StateDownloaded
	StateInstalled    = types.StateInstalled
	StateRunning      = types.StateRunning
)

// 哨兵错误别名。
var (
	ErrNotInstalled   = types.ErrNotInstalled
	ErrUnsupported    = types.ErrUnsupported
	ErrAlreadyRunning = types.ErrAlreadyRunning
	ErrNotDownloaded  = types.ErrNotDownloaded
)

// Report 进度上报工具（实现包使用）。
var Report = types.Report
