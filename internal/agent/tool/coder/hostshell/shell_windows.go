//go:build windows

package hostshell

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// PersistentShell 是常驻 PowerShell 会话（对齐 shell_unix.go 的语义）：
//
// 同一个会话的命令写入常驻进程的 stdin，因此环境变量、函数、当前目录在多次调用之间保留。
// 不用 cmd.exe：它是行解释器，没有脚本块作用域、转义规则复杂，无法可靠地回读 cwd/退出码。
type PersistentShell struct {
	// sessionID 是 shell 的归属会话：同一个 Agent 请求内的多次调用复用，请求结束即关闭。
	sessionID    int64
	cmd          *exec.Cmd
	stdin        *os.File
	isAlive      bool
	cwd          string
	mu           sync.Mutex
	commandQueue chan *commandExecution
	// exited 在常驻进程退出时关闭：让排队 / 等待中的调用立刻失败，而不是一直等到超时。
	exited chan struct{}
	// deathOnce 保证 exited 只关闭一次（进程退出、主动关闭、超时兜底都会走到这里）。
	deathOnce sync.Once
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

const (
	// maxCapturedErrors 是 $Error 的上限：stderr 通过 $Error 捕获，默认 256 条会截断大量输出。
	maxCapturedErrors = 10000
	// killGrace 是杀掉命令子进程后等待哨兵写出的时间。
	killGrace = 2 * time.Second
	// cancelGrace 是 ctx 取消 / shell 退出后等待命令结果的时间。
	cancelGrace = 3 * time.Second
)

// GetPersistentShell 返回 sessionID 对应的持久 PowerShell：同一会话内复用（cwd / 环境变量保留），
// 会话不同则各自新建，可并发执行。会话结束后调用方必须调用 ClosePersistentShell 释放进程。
func GetPersistentShell(sessionID int64, workingDir string) *PersistentShell {
	shellMu.Lock()
	defer shellMu.Unlock()

	if sh := shells[sessionID]; sh != nil && sh.isRunning() {
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
	// -Command - ：从 stdin 逐行读取并执行，进程常驻。
	cmd := exec.Command(powershellPath(), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", "-")
	cmd.Dir = cwd

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil
	}

	cmd.Env = append(os.Environ(), "GIT_EDITOR=true")

	if err := cmd.Start(); err != nil {
		return nil
	}

	shell := &PersistentShell{
		sessionID:    sessionID,
		cmd:          cmd,
		stdin:        stdinPipe.(*os.File),
		isAlive:      true,
		cwd:          cwd,
		commandQueue: make(chan *commandExecution, 10),
		exited:       make(chan struct{}),
	}

	// $Error 是 stderr 的捕获缓冲（见 execCommand），默认上限只有 256 条，
	// 输出大量 stderr 行（npm / go 等）时会被悄悄截断，这里抬高上限。
	_, _ = shell.stdin.Write([]byte(fmt.Sprintf("$MaximumErrorCount = %d\n", maxCapturedErrors)))

	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "Panic in shell command processor: %v\n", r)
				shell.markDead()
			}
		}()
		shell.processCommands()
	}()

	go func() {
		_ = cmd.Wait()
		shell.markDead()
	}()

	return shell
}

// markDead 把 shell 置为不可用并关闭 exited（只生效一次），可重复调用。
func (s *PersistentShell) markDead() {
	s.deathOnce.Do(func() {
		s.mu.Lock()
		s.isAlive = false
		s.mu.Unlock()
		close(s.exited)
	})
}

func (s *PersistentShell) isRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.isAlive
}

func (s *PersistentShell) processCommands() {
	for {
		select {
		case <-s.exited:
			return
		case cmd := <-s.commandQueue:
			result := s.execCommand(cmd.command, cmd.timeout, cmd.ctx)
			// resultChan 有 1 个缓冲，调用方即使已返回（ctx 取消）也不会阻塞。
			cmd.resultChan <- result
		}
	}
}

func (s *PersistentShell) execCommand(command string, timeout time.Duration, ctx context.Context) commandResult {
	s.mu.Lock()
	alive := s.isAlive
	stdin := s.stdin
	s.mu.Unlock()

	if !alive {
		return commandResult{
			stderr:   "Shell is not alive",
			exitCode: 1,
			err:      errors.New("shell is not alive"),
		}
	}

	tempDir := os.TempDir()
	prefix := filepath.Join(tempDir, fmt.Sprintf("gobrave-%d", time.Now().UnixNano()))
	stdoutFile := prefix + ".stdout"
	stderrFile := prefix + ".stderr"
	statusFile := prefix + ".status"
	cwdFile := prefix + ".cwd"
	commandFiles := []string{stdoutFile, stderrFile, statusFile, cwdFile}

	cleanup := func() {
		removeCommandFiles(commandFiles)
	}

	if _, err := stdin.Write([]byte(buildWindowsWrapper(command, stdoutFile, stderrFile, statusFile, cwdFile))); err != nil {
		cleanup()
		return commandResult{
			stderr:   fmt.Sprintf("Failed to write command to shell: %v", err),
			exitCode: 1,
			err:      err,
		}
	}

	startTime := time.Now()
	interrupted := false
	dead := false

	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()

wait:
	for {
		if statusFileReady(statusFile) {
			break
		}
		select {
		case <-s.exited:
			dead = true
			break wait
		case <-ctx.Done():
			interrupted = true
			s.stopCommand(statusFile)
			break wait
		case <-ticker.C:
			if timeout > 0 && time.Since(startTime) > timeout {
				interrupted = true
				s.stopCommand(statusFile)
				break wait
			}
		}
	}

	stdout := readPowerShellOutput(stdoutFile)
	stderr := readPowerShellOutput(stderrFile)
	exitCodeStr := strings.TrimSpace(readPowerShellOutput(statusFile))
	newCwd := strings.TrimSpace(readPowerShellOutput(cwdFile))

	// 进程退出与哨兵写出可能同时发生：只要哨兵已写出，就说明命令完整跑完了。
	if dead && statusFileReady(statusFile) {
		dead = false
	}

	exitCode := 0
	if code, err := strconv.Atoi(exitCodeStr); err == nil {
		exitCode = code
	} else if interrupted || dead {
		exitCode = 143
	}

	if dead {
		// 常驻进程在命令执行期间退出（例如命令里调用了 exit）：哨兵永远不会出现，
		// 这里把它标记为不可用，后续调用会拿到新的 shell。
		s.markDead()
	}

	if newCwd != "" {
		s.mu.Lock()
		s.cwd = newCwd
		s.mu.Unlock()
	}

	if interrupted {
		stderr = appendStderrLine(stderr, "Command execution timed out or was interrupted")
	} else if dead {
		stderr = appendStderrLine(stderr, "Shell exited before the command completed")
	}

	cleanup()

	return commandResult{
		stdout:      stdout,
		stderr:      stderr,
		exitCode:    exitCode,
		interrupted: interrupted,
	}
}

// stopCommand 结束正在执行的命令，尽量保住常驻 shell（cwd / 环境变量）。
//
// 原生命令（git / go / npm ...）是 shell 的子进程，杀掉进程树后命令会继续走到
// finally 写出哨兵；而 Start-Sleep、死循环这类进程内命令无法从外部打断，
// 只能结束整个 shell 兜底，否则后续调用会被永久卡在命令队列里。
func (s *PersistentShell) stopCommand(statusFile string) {
	s.killChildren()
	if waitForStatus(statusFile, killGrace) {
		return
	}
	s.terminate()
}

// killChildren 只结束 shell 的子进程（原生命令），不杀 shell 本身。
// Windows 没有 pgrep，用 CIM 枚举 ParentProcessId 再按进程树 taskkill。
func (s *PersistentShell) killChildren() {
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	script := fmt.Sprintf(
		"$ErrorActionPreference = 'SilentlyContinue'; "+
			"Get-CimInstance Win32_Process -Filter \"ParentProcessId = %d\" | "+
			"ForEach-Object { & taskkill.exe /T /F /PID $_.ProcessId | Out-Null }",
		s.cmd.Process.Pid,
	)
	_ = exec.Command(powershellPath(), "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script).Run()
}

// terminate 结束常驻 shell（并通知等待方），用于会话结束和无法中断的命令兜底。
func (s *PersistentShell) terminate() {
	if !s.isRunning() {
		return
	}
	s.markDead()

	s.mu.Lock()
	proc := s.cmd.Process
	stdin := s.stdin
	s.mu.Unlock()

	if proc != nil {
		_ = proc.Kill()
	}
	if stdin != nil {
		_ = stdin.Close()
	}
}

// buildWindowsWrapper 生成「执行 command 并写出哨兵文件」的一条 PowerShell 语句。
//
// 与 unix 分支的 eval / pwd / $? 三件套等价：
//   - 命令以 base64 传入：stdin 在 Windows 上按 OEM 代码页解码，直接写中文会被破坏，
//     base64 还顺带解决了多行脚本与引号转义；
//   - stdout 重定向到临时文件；stderr 先收集为文本再写文件：PowerShell 会把原生进程的
//     stderr 包装成带 "At line / CategoryInfo" 的 ErrorRecord，直接 2> 会把噪音一起写进去；
//   - try / finally 保证命令抛出终止性错误时也能写出 cwd 与退出码（哨兵），否则调用方
//     只能等到超时；
//   - 不能用 `<` 做输入重定向：PowerShell 保留该运算符，整行会直接解析失败。
func buildWindowsWrapper(command, stdoutFile, stderrFile, statusFile, cwdFile string) string {
	// $? / LASTEXITCODE 必须在用户命令所在的脚本块里读取：
	// -Command - 模式下，外层语句（尤其是带重定向时）拿不到脚本块的真实执行状态。
	payload := command + "\n" +
		"$global:__gobraveOk = $?\n" +
		"$global:__gobraveNative = $global:LASTEXITCODE\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(payload))

	return fmt.Sprintf(
		"$__gbCmd = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String('%s')); "+
			"$Error.Clear(); $global:LASTEXITCODE = $null; $global:__gobraveOk = $true; $global:__gobraveNative = $null; "+
			"$__gbCode = 0; $__gbCaught = ''; "+
			"$__gbErrWriter = New-Object System.IO.StringWriter; $__gbOldErr = [Console]::Error; [Console]::SetError($__gbErrWriter); "+
			"try { & ([ScriptBlock]::Create($__gbCmd)) 1> %s 2> $null } "+
			"catch { $__gbCode = 1; $__gbCaught = $_.Exception.Message; if ($_.Exception.InnerException) { $__gbCaught = $_.Exception.InnerException.Message } } "+
			"finally { "+
			"[Console]::SetError($__gbOldErr); "+
			"$__gbErrs = @($Error | ForEach-Object { $_.ToString() }); [Array]::Reverse($__gbErrs); "+
			"$__gbDirect = $__gbErrWriter.ToString(); "+
			"if ($__gbDirect) { $__gbErrs += @($__gbDirect -split \"`r?`n\" | Where-Object { $_ -ne '' }) }; "+
			"if (($__gbErrs.Count -eq 0) -and $__gbCaught) { $__gbErrs = @($__gbCaught) }; "+
			"if ($null -ne $global:__gobraveNative) { $__gbCode = $global:__gobraveNative } elseif ($global:__gobraveOk -eq $false) { $__gbCode = 1 }; "+
			"[IO.File]::WriteAllLines(%s, $__gbErrs); "+
			"[IO.File]::WriteAllText(%s, (Get-Location).Path); "+
			"[IO.File]::WriteAllText(%s, $__gbCode.ToString()) }\n",
		encoded,
		psQuote(stdoutFile),
		psQuote(stderrFile),
		psQuote(cwdFile),
		psQuote(statusFile),
	)
}

func (s *PersistentShell) Exec(ctx context.Context, command string, timeoutMs int) (string, string, int, bool, error) {
	if err := ctx.Err(); err != nil {
		return "", "command aborted: " + err.Error(), 130, true, nil
	}
	if !s.isRunning() {
		return "", "Shell is not alive", 1, false, errors.New("shell is not alive")
	}

	execution := &commandExecution{
		command:    command,
		timeout:    time.Duration(timeoutMs) * time.Millisecond,
		resultChan: make(chan commandResult, 1),
		ctx:        ctx,
	}

	// shell 被关闭（会话结束）或 ctx 取消时不要永久阻塞。
	select {
	case s.commandQueue <- execution:
	case <-s.exited:
		return "", "Shell is not alive", 1, false, errors.New("shell is not alive")
	case <-ctx.Done():
		return "", "command aborted: " + ctx.Err().Error(), 130, true, nil
	}

	select {
	case result := <-execution.resultChan:
		return result.stdout, result.stderr, result.exitCode, result.interrupted, result.err
	case <-ctx.Done():
		// execCommand 会结束子进程并尽快写出哨兵，短暂等待以返回真实的部分输出。
		select {
		case result := <-execution.resultChan:
			return result.stdout, result.stderr, result.exitCode, result.interrupted, result.err
		case <-time.After(cancelGrace):
			return "", "command aborted: " + ctx.Err().Error(), 130, true, nil
		}
	case <-s.exited:
		// 进程已退出；命令可能是随进程一起结束的，优先返回它的结果。
		select {
		case result := <-execution.resultChan:
			return result.stdout, result.stderr, result.exitCode, result.interrupted, result.err
		case <-time.After(cancelGrace):
			return "", "Shell exited before the command completed", 1, false, nil
		}
	}
}

// Close 实现 Shell：ctx 仅用于接口统一，宿主机进程结束不依赖它。
func (s *PersistentShell) Close(_ context.Context) {
	s.terminate()
}

// powershellPath 优先使用 pwsh（PowerShell 7+，默认 UTF-8），回退到 Windows PowerShell。
func powershellPath() string {
	if p, err := exec.LookPath("pwsh"); err == nil {
		return p
	}
	return "powershell.exe"
}

// psQuote 把路径转为 PowerShell 单引号字面量（单引号内的 ' 需写成两个 '）。
func psQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// appendStderrLine 给 stderr 追加一行说明（已有内容时补换行）。
func appendStderrLine(stderr, line string) string {
	if stderr == "" {
		return line
	}
	if !strings.HasSuffix(stderr, "\n") {
		stderr += "\n"
	}
	return stderr + line
}

func statusFileReady(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Size() > 0
}

// removeCommandFiles 删除命令的临时文件。
//
// 进程刚被结束时 PowerShell 可能仍持有重定向文件句柄，删除会失败，这里短暂重试。
func removeCommandFiles(files []string) {
	for attempt := 0; attempt < 6; attempt++ {
		pending := 0
		for _, file := range files {
			if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
				pending++
			}
		}
		if pending == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForStatus 在 d 时间内等待哨兵出现。
func waitForStatus(path string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if statusFileReady(path) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// readPowerShellOutput 读取 shell 写出的文件内容。
//
// Windows PowerShell 5.1 的 `>` 重定向默认写 UTF-16LE（带 BOM），pwsh 7 写 UTF-8，
// 这里按 BOM（含无 BOM 的 UTF-16LE 兜底判断）解码，避免把 UTF-16 当 UTF-8 读成乱码。
func readPowerShellOutput(path string) string {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return ""
	}

	switch {
	case len(data) >= 2 && data[0] == 0xFF && data[1] == 0xFE:
		return decodeUTF16(data[2:], true)
	case len(data) >= 2 && data[0] == 0xFE && data[1] == 0xFF:
		return decodeUTF16(data[2:], false)
	case len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF:
		return string(data[3:])
	case len(data) >= 2 && data[1] == 0 && data[0] != 0:
		// 无 BOM 的 UTF-16LE：ASCII 内容的高字节为 0。
		return decodeUTF16(data, true)
	}
	return string(data)
}

func decodeUTF16(data []byte, littleEndian bool) string {
	units := make([]uint16, 0, len(data)/2)
	for i := 0; i+1 < len(data); i += 2 {
		if littleEndian {
			units = append(units, uint16(data[i])|uint16(data[i+1])<<8)
		} else {
			units = append(units, uint16(data[i])<<8|uint16(data[i+1]))
		}
	}
	return string(utf16.Decode(units))
}
