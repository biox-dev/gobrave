package hostshell

import "context"

// Shell 是 Agent 一次 bash 调用的「命令执行终端」统一抽象。
//
// 两个实现：
//   - PersistentShell（shell_unix.go / shell_windows.go）：宿主机常驻 shell/PowerShell；
//   - ContainerShell（container_shell.go）：用户容器模板里的持久 PTY。
//
// 二者语义完全一致：同一个 Agent 会话内的多次调用复用同一个终端
// （cwd / 环境变量 / 已安装的包在调用之间保留），会话结束时由调用方通过包级
// CloseShell(sessionID) 统一回收。
// 工具层只依赖本接口，通过 AcquireShell 按配置拿到具体实现，不必感知执行位置。
type Shell interface {
	// Exec 在终端里执行命令并等待结束。
	//
	// 返回值与 bash 工具对齐的四元组：stdout、stderr、exitCode、interrupted。
	Exec(ctx context.Context, command string, timeoutMs int) (stdout, stderr string, exitCode int, interrupted bool, err error)
}

// ShellOptions 是 AcquireShell 的入参，描述「为哪个会话、在什么位置」取终端。
type ShellOptions struct {
	// Mode 是执行位置：config.BashExecutionModeHost 或 config.BashExecutionModeContainer。
	// 传空串时按 host 处理。
	Mode string
	// SessionID 是 Agent 会话 ID：同会话复用同一个终端，必须 > 0。
	SessionID int64
	// WorkingDir 是宿主机终端的初始工作目录（仅 host 模式使用）。
	WorkingDir string

	// Container 模式下需要的定位与能力：
	//   - 需要 SessionID / UserID / ProjectID 定位用户激活项目下的容器终端；
	//   - TemplateID 指定容器模板；ContainerShell 提供容器终端服务能力。
	UserID     int64
	ProjectID  int64
	TemplateID int64
	Container  ContainerShellService
}

// AcquireShell 按 options 返回统一 Shell 接口的实现。
//
// host 模式返回宿主机 *PersistentShell；container 模式返回 *ContainerShell。
// 未启用（宿主机 shell 创建失败、容器服务缺失、模板 / 会话 / 项目信息不足）时返回 nil，
// 调用方据此给出可读的错误。
func AcquireShell(options ShellOptions) Shell {
	if options.Mode == ModeContainer {
		return GetContainerShell(options.Container, options.SessionID, options.UserID, options.ProjectID, options.TemplateID)
	}
	return GetPersistentShell(options.SessionID, options.WorkingDir)
}

// CloseShell 关闭 sessionID 对应的所有终端（宿主机 shell 与容器 shell），
// 供 Agent 请求结束时统一回收；可重复调用。
//
// 这是包内唯一的终端回收入口：Shell 接口故意不暴露 Close，
// 因为终端按会话（而非按实例）登记，回收必须同时命中两类终端。
func CloseShell(sessionID int64) {
	ClosePersistentShell(sessionID)
	CloseContainerShell(sessionID)
}

// 执行位置常量：避免 hostshell 依赖 config 包，取值与 config.BashExecutionMode* 保持一致。
// ModeHost 是默认值：Mode 为空串或 ModeHost 都走宿主机 shell。
const (
	ModeHost      = "host"
	ModeContainer = "container"
)

// 编译期断言：两个实现都必须满足统一的 Shell 接口。
var (
	_ Shell = (*PersistentShell)(nil)
	_ Shell = (*ContainerShell)(nil)
)
