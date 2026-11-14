//go:build windows

package wsl

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

const (
	// createBreakawayFromJob 允许子进程脱离父进程所在的作业对象：
	// 桌面宿主 / 服务包装器常用作业对象管理子进程，不脱离的话
	// gobrave 一退出，常驻的 wsl.exe 会被一起杀掉。
	createBreakawayFromJob = 0x01000000
	createNewProcessGroup  = 0x00000200
	detachedProcess        = 0x00000008
)

// startDetached 启动一个脱离 gobrave 的后台进程：独立进程组、不继承控制台，
// 因此 gobrave 退出后它继续运行。
//
// 若当前作业对象不允许脱离（ERROR_ACCESS_DENIED），降级为普通的分离后台进程。
func startDetached(exe string, args []string, devNull *os.File) (*exec.Cmd, error) {
	newCmd := func(flags uint32) *exec.Cmd {
		cmd := exec.Command(exe, args...)
		cmd.Stdin = devNull
		cmd.Stdout = devNull
		cmd.Stderr = devNull
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
		return cmd
	}

	cmd := newCmd(detachedProcess | createNewProcessGroup | createBreakawayFromJob)
	err := cmd.Start()
	if err == nil {
		return cmd, nil
	}
	if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		return nil, err
	}
	cmd = newCmd(detachedProcess | createNewProcessGroup)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}
