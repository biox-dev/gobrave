//go:build !windows

package wsl

import (
	"os"
	"os/exec"
	"syscall"
)

// startDetached 以独立会话启动后台进程，使其在 gobrave 退出后继续运行。
func startDetached(exe string, args []string, devNull *os.File) (*exec.Cmd, error) {
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
