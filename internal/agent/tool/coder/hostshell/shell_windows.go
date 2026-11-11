//go:build windows

package hostshell

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// PersistentShell 在 Windows 上是无状态实现：每条命令交给 cmd /C 用 %COMSPEC% 执行。
// Windows 没有 bash，也没有 /dev/null、pgrep、SIGTERM，因此不维护持久会话。
type PersistentShell struct {
	cwd string
}

// GetPersistentShell 返回 Windows 下的命令执行器（cwd 由调用方按命令传入）。
// Windows 无持久会话，sessionID 不参与选择。
func GetPersistentShell(_ int64, workingDir string) *PersistentShell {
	return &PersistentShell{cwd: workingDir}
}

// ClosePersistentShell 在 Windows 下无资源可释放。
func ClosePersistentShell(_ int64) {}

// CloseAllPersistentShells 在 Windows 下无资源可释放。
func CloseAllPersistentShells() {}

// Exec 用 cmd /C 执行 command 并汇总输出。
// 超时由 ctx 控制（调用方按 timeout 派生带取消的 ctx）。
func (s *PersistentShell) Exec(ctx context.Context, command string, _ int) (string, string, int, bool, error) {
	cmd := exec.CommandContext(ctx, comspec(), "/C", command)
	if s.cwd != "" {
		cmd.Dir = s.cwd
	}

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
			stderr.WriteString(err.Error())
		}
	}

	interrupted := ctx.Err() == context.DeadlineExceeded
	return stdout.String(), stderr.String(), exitCode, interrupted, nil
}

// Close 无需释放资源。
func (s *PersistentShell) Close() {}

func comspec() string {
	if c := os.Getenv("COMSPEC"); c != "" {
		return c
	}
	return "cmd.exe"
}
