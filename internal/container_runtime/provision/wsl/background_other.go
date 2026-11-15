//go:build !windows

package wsl

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// hideConsoleWindow 在非 Windows 平台上无需处理（没有控制台窗口可弹）。
func hideConsoleWindow(*exec.Cmd) {}

// startBackground 以独立会话启动后台进程，只 Start 不 Wait，不阻塞调用方。
//
// WSL 只存在于 Windows，这里的 detach 没有实际差异，仅为保持跨平台签名一致。
func startBackground(exe string, args []string, devNull *os.File, detach bool) (*exec.Cmd, error) {
	_ = detach
	cmd := exec.Command(exe, args...)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// processAlive 报告 pid 对应的进程是否仍在运行（信号 0 只做存在性检查）。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// processStartedAt 在非 Windows 平台上无法获取创建时间，返回 ok=false。
// WSL 只在 Windows 上运行，该路径不会被执行。
func processStartedAt(int) (time.Time, bool) { return time.Time{}, false }

// terminateProcess 结束 pid 对应的进程。
func terminateProcess(pid int) error {
	return syscall.Kill(pid, syscall.SIGKILL)
}
