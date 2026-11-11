//go:build !windows

package hostshell

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type PersistentShell struct {
	// sessionID 是 shell 的归属会话：同一个 Agent 请求内的多次调用复用，请求结束即关闭。
	sessionID    int64
	cmd          *exec.Cmd
	stdin        *os.File
	isAlive      bool
	cwd          string
	mu           sync.Mutex
	commandQueue chan *commandExecution
}

type commandExecution struct {
	command    string
	timeout    time.Duration
	resultChan chan commandResult
	ctx        context.Context
}

type commandResult struct {
	stdout      string
	stderr      string
	exitCode    int
	interrupted bool
	err         error
}

var (
	shellMu sync.Mutex
	// shells 按会话维护持久 shell：不同会话（并发对话）各自独立，互不干扰。
	shells = make(map[int64]*PersistentShell)
)

// GetPersistentShell 返回 sessionID 对应的持久 shell：同一会话内复用（cwd / 环境变量保留），
// 会话不同则各自新建，可并发执行。会话结束后调用方必须调用 ClosePersistentShell 释放进程。
func GetPersistentShell(sessionID int64, workingDir string) *PersistentShell {
	shellMu.Lock()
	defer shellMu.Unlock()

	if sh := shells[sessionID]; sh != nil && sh.isAlive {
		return sh
	}
	sh := newPersistentShell(sessionID, workingDir)
	if sh == nil {
		return nil
	}
	shells[sessionID] = sh
	return sh
}

// ClosePersistentShell 关闭 sessionID 对应的 shell（会话结束后调用）。
//
// TODO: 子 Agent 复用父 Agent 的 SessionID（见 native.NewAgentTool 的 ParentSessionID），
// 因此父请求结束时直接关闭会打断子 Agent 正在执行的命令。待实现：按会话统计活跃执行数，
// 归零时才真正 kill（OnStart +1 / OnFinish -1），子 Agent 复用同会话时计数不会归零。
func ClosePersistentShell(sessionID int64) {
	shellMu.Lock()
	sh := shells[sessionID]
	delete(shells, sessionID)
	shellMu.Unlock()

	if sh != nil {
		sh.Close(context.Background())
	}
}

// CloseAllPersistentShells 关闭所有会话的 shell（进程退出时兜底）。
func CloseAllPersistentShells() {
	shellMu.Lock()
	all := shells
	shells = make(map[int64]*PersistentShell)
	shellMu.Unlock()

	for _, sh := range all {
		sh.Close(context.Background())
	}
}

func newPersistentShell(sessionID int64, cwd string) *PersistentShell {
	// gobrave 无全局 shell 配置：优先沿用 $SHELL，缺省 bash -l。
	shellPath := os.Getenv("SHELL")
	if shellPath == "" {
		shellPath = "/bin/bash"
	}
	shellArgs := []string{"-l"}

	cmd := exec.Command(shellPath, shellArgs...)
	cmd.Dir = cwd

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}

	cmd.Env = append(os.Environ(), "GIT_EDITOR=true")

	err = cmd.Start()
	if err != nil {
		return nil
	}

	shell := &PersistentShell{
		sessionID:    sessionID,
		cmd:          cmd,
		stdin:        stdinPipe.(*os.File),
		isAlive:      true,
		cwd:          cwd,
		commandQueue: make(chan *commandExecution, 10),
	}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "Panic in shell command processor: %v\n", r)
				if shell.markDead() {
					close(shell.commandQueue)
				}
			}
		}()
		shell.processCommands()
	}()

	go func() {
		cmd.Wait()
		if shell.markDead() {
			close(shell.commandQueue)
		}
	}()

	return shell
}

// markDead 把 shell 置为不可用并返回是否是本次调用完成的状态切换（用于只关闭一次队列）。
func (s *PersistentShell) markDead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.isAlive {
		return false
	}
	s.isAlive = false
	return true
}

func (s *PersistentShell) processCommands() {
	for cmd := range s.commandQueue {
		result := s.execCommand(cmd.command, cmd.timeout, cmd.ctx)
		// 调用方（Exec）可能因 ctx 取消或 shell 关闭已返回，避免永久阻塞。
		select {
		case cmd.resultChan <- result:
		case <-cmd.ctx.Done():
		}
	}
}

func (s *PersistentShell) execCommand(command string, timeout time.Duration, ctx context.Context) commandResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.isAlive {
		return commandResult{
			stderr:   "Shell is not alive",
			exitCode: 1,
			err:      errors.New("shell is not alive"),
		}
	}

	tempDir := os.TempDir()
	stdoutFile := filepath.Join(tempDir, fmt.Sprintf("opencode-stdout-%d", time.Now().UnixNano()))
	stderrFile := filepath.Join(tempDir, fmt.Sprintf("opencode-stderr-%d", time.Now().UnixNano()))
	statusFile := filepath.Join(tempDir, fmt.Sprintf("opencode-status-%d", time.Now().UnixNano()))
	cwdFile := filepath.Join(tempDir, fmt.Sprintf("opencode-cwd-%d", time.Now().UnixNano()))

	defer func() {
		os.Remove(stdoutFile)
		os.Remove(stderrFile)
		os.Remove(statusFile)
		os.Remove(cwdFile)
	}()

	fullCommand := fmt.Sprintf(`
eval %s < /dev/null > %s 2> %s
EXEC_EXIT_CODE=$?
pwd > %s
echo $EXEC_EXIT_CODE > %s
`,
		shellQuote(command),
		shellQuote(stdoutFile),
		shellQuote(stderrFile),
		shellQuote(cwdFile),
		shellQuote(statusFile),
	)

	_, err := s.stdin.Write([]byte(fullCommand + "\n"))
	if err != nil {
		return commandResult{
			stderr:   fmt.Sprintf("Failed to write command to shell: %v", err),
			exitCode: 1,
			err:      err,
		}
	}

	interrupted := false

	startTime := time.Now()

	done := make(chan bool)
	go func() {
		for {
			select {
			case <-ctx.Done():
				s.killChildren()
				interrupted = true
				done <- true
				return

			case <-time.After(10 * time.Millisecond):
				if fileExists(statusFile) && fileSize(statusFile) > 0 {
					done <- true
					return
				}

				if timeout > 0 {
					elapsed := time.Since(startTime)
					if elapsed > timeout {
						s.killChildren()
						interrupted = true
						done <- true
						return
					}
				}
			}
		}
	}()

	<-done

	stdout := readFileOrEmpty(stdoutFile)
	stderr := readFileOrEmpty(stderrFile)
	exitCodeStr := readFileOrEmpty(statusFile)
	newCwd := readFileOrEmpty(cwdFile)

	exitCode := 0
	if exitCodeStr != "" {
		fmt.Sscanf(exitCodeStr, "%d", &exitCode)
	} else if interrupted {
		exitCode = 143
		stderr += "\nCommand execution timed out or was interrupted"
	}

	if newCwd != "" {
		s.cwd = strings.TrimSpace(newCwd)
	}

	return commandResult{
		stdout:      stdout,
		stderr:      stderr,
		exitCode:    exitCode,
		interrupted: interrupted,
	}
}

func (s *PersistentShell) killChildren() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}

	pgrepCmd := exec.Command("pgrep", "-P", fmt.Sprintf("%d", s.cmd.Process.Pid))
	output, err := pgrepCmd.Output()
	if err != nil {
		return
	}

	for pidStr := range strings.SplitSeq(string(output), "\n") {
		if pidStr = strings.TrimSpace(pidStr); pidStr != "" {
			var pid int
			fmt.Sscanf(pidStr, "%d", &pid)
			if pid > 0 {
				proc, err := os.FindProcess(pid)
				if err == nil {
					proc.Signal(syscall.SIGTERM)
				}
			}
		}
	}
}

func (s *PersistentShell) Exec(ctx context.Context, command string, timeoutMs int) (string, string, int, bool, error) {
	if !s.isAlive {
		return "", "Shell is not alive", 1, false, errors.New("shell is not alive")
	}

	timeout := time.Duration(timeoutMs) * time.Millisecond

	resultChan := make(chan commandResult, 1)
	s.commandQueue <- &commandExecution{
		command:    command,
		timeout:    timeout,
		resultChan: resultChan,
		ctx:        ctx,
	}

	// shell 被关闭（会话结束）或 ctx 取消时不要永久阻塞。
	select {
	case result := <-resultChan:
		return result.stdout, result.stderr, result.exitCode, result.interrupted, result.err
	case <-ctx.Done():
		return "", "command aborted: " + ctx.Err().Error(), 130, true, nil
	}
}

// Close 实现 Shell：ctx 仅用于接口统一，宿主机进程结束不依赖它。
func (s *PersistentShell) Close(_ context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeLocked()
}

// closeLocked 在持有 s.mu 时结束 shell 进程。
func (s *PersistentShell) closeLocked() {
	if !s.isAlive {
		return
	}
	s.isAlive = false
	// 直接结束进程：stdin 可能正被 execCommand 使用，不阻塞等锁。
	// cmd.Wait 返回后由后台 goroutine 关闭 commandQueue。
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
	_ = s.stdin.Close()
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func readFileOrEmpty(path string) string {
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(content)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func fileSize(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
