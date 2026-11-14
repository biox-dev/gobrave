//go:build !windows

package wsl

import (
	"os"
	"os/exec"
	"syscall"
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
