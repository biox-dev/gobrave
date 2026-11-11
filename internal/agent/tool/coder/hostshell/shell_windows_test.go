//go:build windows

package hostshell

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

func newTestShell(t *testing.T) *PersistentShell {
	t.Helper()
	sh := newPersistentShell(0, t.TempDir())
	if sh == nil {
		t.Fatal("newPersistentShell returned nil")
	}
	t.Cleanup(func() { sh.Close(context.Background()) })
	return sh
}

func execForTest(t *testing.T, sh *PersistentShell, command string, timeoutMs int) (string, string, int, bool) {
	t.Helper()
	stdout, stderr, exitCode, interrupted, err := sh.Exec(context.Background(), command, timeoutMs)
	if err != nil {
		t.Fatalf("Exec(%q) returned error: %v", command, err)
	}
	return stdout, stderr, exitCode, interrupted
}

func TestPersistentShellExecBasic(t *testing.T) {
	sh := newTestShell(t)

	stdout, stderr, exitCode, interrupted := execForTest(t, sh, "Write-Output 'hello'", 30000)
	if strings.TrimSpace(stdout) != "hello" {
		t.Fatalf("stdout = %q, want hello", stdout)
	}
	if stderr != "" {
		t.Fatalf("stderr = %q, want empty", stderr)
	}
	if exitCode != 0 || interrupted {
		t.Fatalf("exitCode = %d, interrupted = %v, want 0/false", exitCode, interrupted)
	}
}

func TestPersistentShellKeepsState(t *testing.T) {
	sh := newTestShell(t)

	execForTest(t, sh, "$env:GOBRAVE_SHELL_TEST = 'kept'", 30000)
	stdout, _, _, _ := execForTest(t, sh, "Write-Output $env:GOBRAVE_SHELL_TEST", 30000)
	if strings.TrimSpace(stdout) != "kept" {
		t.Fatalf("env var not preserved, got %q", stdout)
	}

	execForTest(t, sh, "Set-Location $env:TEMP", 30000)
	stdout, _, _, _ = execForTest(t, sh, "(Get-Location).Path", 30000)
	if !strings.EqualFold(strings.TrimSpace(stdout), strings.TrimSpace(os.TempDir())) {
		t.Fatalf("cwd not preserved, got %q", stdout)
	}
}

func TestPersistentShellNativeExitCode(t *testing.T) {
	sh := newTestShell(t)

	_, _, exitCode, interrupted := execForTest(t, sh, "cmd /c exit 5", 30000)
	if exitCode != 5 || interrupted {
		t.Fatalf("exitCode = %d, interrupted = %v, want 5/false", exitCode, interrupted)
	}
}

func TestPersistentShellNativeStderrIsClean(t *testing.T) {
	sh := newTestShell(t)

	_, stderr, exitCode, _ := execForTest(t, sh, "cmd /c 'echo oops 1>&2'", 30000)
	if !strings.Contains(stderr, "oops") {
		t.Fatalf("stderr = %q, want it to contain oops", stderr)
	}
	// PowerShell 直接 2> 重定向会把原生 stderr 包成 ErrorRecord，带上 At line / CategoryInfo 噪音。
	if strings.Contains(stderr, "CategoryInfo") || strings.Contains(stderr, "At line:") {
		t.Fatalf("stderr contains PowerShell ErrorRecord noise: %q", stderr)
	}
	if exitCode != 0 {
		t.Fatalf("exitCode = %d, want 0", exitCode)
	}
}

func TestPersistentShellTerminatingErrorKeepsShell(t *testing.T) {
	sh := newTestShell(t)

	_, stderr, exitCode, interrupted := execForTest(t, sh, "Write-Output 'before'; throw 'kaboom'", 30000)
	if !strings.Contains(stderr, "kaboom") {
		t.Fatalf("stderr = %q, want it to contain kaboom", stderr)
	}
	if exitCode != 1 || interrupted {
		t.Fatalf("exitCode = %d, interrupted = %v, want 1/false", exitCode, interrupted)
	}

	// 终止性错误后必须还能继续用同一个 shell（否则每次都要等超时）。
	stdout, _, _, _ := execForTest(t, sh, "Write-Output 'still-alive'", 30000)
	if strings.TrimSpace(stdout) != "still-alive" {
		t.Fatalf("shell not reusable after terminating error, stdout = %q", stdout)
	}
}

func TestPersistentShellNonASCIIAndMultiline(t *testing.T) {
	sh := newTestShell(t)

	stdout, _, _, _ := execForTest(t, sh, "Write-Output '中文-测试'", 30000)
	if !strings.Contains(stdout, "中文-测试") {
		t.Fatalf("stdout = %q, want it to contain 中文-测试", stdout)
	}

	command := "if ($true) {\n  Write-Output 'multi'\n}\nWrite-Output 'tail'"
	stdout, _, _, _ = execForTest(t, sh, command, 30000)
	if !strings.Contains(stdout, "multi") || !strings.Contains(stdout, "tail") {
		t.Fatalf("multiline command output = %q", stdout)
	}
}

func TestPersistentShellCmdletFailureExitCode(t *testing.T) {
	sh := newTestShell(t)

	_, _, exitCode, _ := execForTest(t, sh, "Get-ChildItem 'Z:\\definitely-missing-path'", 30000)
	if exitCode != 1 {
		t.Fatalf("exitCode = %d, want 1", exitCode)
	}
}

func TestPersistentShellTimeoutKillsChildrenKeepsShell(t *testing.T) {
	sh := newTestShell(t)

	_, _, _, interrupted := execForTest(t, sh, "cmd /c 'ping -n 20 127.0.0.1'", 500)
	if !interrupted {
		t.Fatal("expected interrupted = true")
	}
	if !sh.isRunning() {
		t.Fatal("shell should survive when only child processes are killed")
	}

	stdout, _, _, _ := execForTest(t, sh, "Write-Output 'after-timeout'", 30000)
	if strings.TrimSpace(stdout) != "after-timeout" {
		t.Fatalf("shell not reusable after timeout, stdout = %q", stdout)
	}
}

func TestPersistentShellTimeoutInProcessCommandKillsShell(t *testing.T) {
	sh := newTestShell(t)

	_, _, _, interrupted := execForTest(t, sh, "Start-Sleep -Seconds 30", 300)
	if !interrupted {
		t.Fatal("expected interrupted = true")
	}
	if sh.isRunning() {
		t.Fatal("in-process command cannot be interrupted, shell should be terminated")
	}

	// 会话侧会通过 GetPersistentShell 重新拿到新 shell。
	replacement := GetPersistentShell(424242, t.TempDir())
	if replacement == nil {
		t.Fatal("GetPersistentShell returned nil")
	}
	defer ClosePersistentShell(424242)
	stdout, _, _, _ := execForTest(t, replacement, "Write-Output 'recovered'", 30000)
	if strings.TrimSpace(stdout) != "recovered" {
		t.Fatalf("replacement shell stdout = %q", stdout)
	}
}

func TestPersistentShellExecAfterClose(t *testing.T) {
	sh := newPersistentShell(0, t.TempDir())
	if sh == nil {
		t.Fatal("newPersistentShell returned nil")
	}
	sh.Close(context.Background())

	_, _, _, _, err := sh.Exec(context.Background(), "Write-Output 'x'", 5000)
	if err == nil {
		t.Fatal("expected error after Close")
	}
}

func TestPersistentShellTimeoutDoesNotHangForever(t *testing.T) {
	sh := newTestShell(t)

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _, interrupted, _ := sh.Exec(context.Background(), "Start-Sleep -Seconds 60", 200)
		if !interrupted {
			t.Errorf("expected interrupted result")
		}
	}()

	select {
	case <-done:
	case <-time.After(killGrace + cancelGrace + 5*time.Second):
		t.Fatal("Exec did not return in time after timeout")
	}
}
