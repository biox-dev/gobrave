//go:build windows

package wsl

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	// createBreakawayFromJob 允许子进程脱离父进程所在的作业对象：
	// 桌面宿主 / 服务包装器常用作业对象管理子进程，不脱离的话
	// gobrave 一退出，常驻的 wsl.exe 会被一起杀掉。
	createBreakawayFromJob = 0x01000000
	createNewProcessGroup  = 0x00000200
	// createNoWindow 让控制台程序在「不可见控制台」里运行。
	//
	// wsl.exe 是控制台程序：从 GUI 进程（自身没有控制台）拉起时，它会自己
	// AllocConsole 申请一个新控制台，Windows 就会弹出一个黑框窗口；
	// 若用 DETACHED_PROCESS 启动，子进程同样没有控制台，结果还是弹窗。
	// CREATE_NO_WINDOW 在创建阶段就把控制台准备好（只是不可见），wsl.exe
	// 因此不会再申请，全程无窗口。它与 DETACHED_PROCESS 互斥，只能取其一。
	createNoWindow = 0x08000000
)

// hideConsoleWindow 让 cmd 以隐藏控制台的方式启动，避免 wsl.exe 弹出控制台窗口。
//
// 适用于所有同步调用（-l -q / --import / --unregister / 发行版内命令）：
// GUI 版的 gobrave 没有控制台，不隐藏的话每次调用都可能闪一个黑框。
func hideConsoleWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}

// startBackground 启动一个后台进程并立即返回（只 Start 不 Wait，不阻塞调用方）。
//
// detach=false（默认）：进程加入 kill-on-close 作业对象，gobrave 退出（含异常退出）
// 时系统关闭作业句柄，该进程随之结束；
// detach=true：进程脱离 gobrave，gobrave 退出后继续运行。
func startBackground(exe string, args []string, devNull *os.File, detach bool) (*exec.Cmd, error) {
	if detach {
		return startDetached(exe, args, devNull)
	}
	return startAttached(exe, args, devNull)
}

// newBackgroundCmd 构造后台命令：标准流全部丢弃，隐藏控制台，使用给定创建标志。
func newBackgroundCmd(exe string, args []string, devNull *os.File, flags uint32) *exec.Cmd {
	cmd := exec.Command(exe, args...)
	cmd.Stdin = devNull
	cmd.Stdout = devNull
	cmd.Stderr = devNull
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
	return cmd
}

// startDetached 启动一个脱离 gobrave 的后台进程：隐藏控制台窗口、独立进程组，
// 因此 gobrave 退出后它继续运行。
//
// 用 CREATE_NO_WINDOW 而不是 DETACHED_PROCESS：后者让子进程完全没有控制台，
// wsl.exe 会自己 AllocConsole 弹窗；前者给的是一个不可见的控制台，静默运行。
//
// 若当前作业对象不允许脱离（ERROR_ACCESS_DENIED），降级为普通的后台进程。
func startDetached(exe string, args []string, devNull *os.File) (*exec.Cmd, error) {
	cmd := newBackgroundCmd(exe, args, devNull, createNoWindow|createNewProcessGroup|createBreakawayFromJob)
	err := cmd.Start()
	if err == nil {
		return cmd, nil
	}
	if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) {
		return nil, err
	}
	cmd = newBackgroundCmd(exe, args, devNull, createNoWindow|createNewProcessGroup)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// startAttached 启动一个跟随 gobrave 生命周期的后台进程。
//
// 用 kill-on-close 作业对象实现：gobrave 退出时系统关闭作业句柄，
// 作业内的进程（连同它撑住的发行版会话）随之结束。
// 作业句柄故意不关闭，退出路径上无需任何清理，也不阻塞。
func startAttached(exe string, args []string, devNull *os.File) (*exec.Cmd, error) {
	job, err := killOnCloseJob()
	if err != nil {
		return nil, err
	}
	cmd := newBackgroundCmd(exe, args, devNull, createNoWindow)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	if err := assignToJob(job, uint32(cmd.Process.Pid)); err != nil {
		// 没能纳入作业就不留下「本应跟随退出」的常驻会话。
		_ = cmd.Process.Kill()
		_ = cmd.Process.Release()
		return nil, err
	}
	return cmd, nil
}

// assignToJob 把 pid 对应的进程加入作业对象。
func assignToJob(job windows.Handle, pid uint32) error {
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		return fmt.Errorf("open background process %d: %w", pid, err)
	}
	defer windows.CloseHandle(proc)
	if err := windows.AssignProcessToJobObject(job, proc); err != nil {
		return fmt.Errorf("assign background process %d to job: %w", pid, err)
	}
	return nil
}

var (
	jobOnce sync.Once
	job     windows.Handle
	jobErr  error
)

// killOnCloseJob 返回一个带 JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE 的作业对象。
//
// 句柄在 gobrave 整个生命周期内保持打开：进程退出时由系统关闭，
// 作业内的所有进程随即被终止。
func killOnCloseJob() (windows.Handle, error) {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			jobErr = fmt.Errorf("create job object: %w", err)
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(h)
			jobErr = fmt.Errorf("set job object limit: %w", err)
			return
		}
		job = h
	})
	return job, jobErr
}
