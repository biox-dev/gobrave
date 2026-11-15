package wsl

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestKeeperAliveForCurrentProcess 验证本进程被识别为存活会话。
func TestKeeperAliveForCurrentProcess(t *testing.T) {
	k := &keeper{PID: os.Getpid(), StartedAt: time.Now()}
	if !k.alive() {
		t.Fatalf("keeper of current process (%d) reports dead", os.Getpid())
	}
}

// TestKeeperAliveRejectsReusedPID 验证 pid 相同但创建时间对不上时判为已结束。
func TestKeeperAliveRejectsReusedPID(t *testing.T) {
	k := &keeper{PID: os.Getpid(), StartedAt: time.Now().Add(-time.Hour)}
	if k.alive() {
		t.Fatal("keeper with mismatched start time must not be treated as alive")
	}
}

// TestKeeperAliveForExitedProcess 验证已退出进程判为已结束。
func TestKeeperAliveForExitedProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()

	if (&keeper{PID: pid, StartedAt: time.Now()}).alive() {
		t.Fatalf("exited process %d reported alive", pid)
	}
	if processAlive(pid) {
		t.Fatalf("processAlive(%d) = true for an exited process", pid)
	}
}

// TestKeeperRecordRoundTrip 验证 keeper.json 的读写与 pid 复用校验。
func TestKeeperRecordRoundTrip(t *testing.T) {
	p := New(Config{BaseDir: t.TempDir()})
	in := &keeper{PID: os.Getpid(), Detach: true, Owner: os.Getpid(), StartedAt: time.Now()}
	p.writeKeeper(nil, in)

	out := p.readKeeper(nil)
	if out == nil {
		t.Fatal("readKeeper returned nil")
	}
	if out.PID != in.PID || out.Detach != in.Detach || out.Owner != in.Owner {
		t.Fatalf("readKeeper = %+v, want %+v", out, in)
	}
	if !out.alive() {
		t.Fatal("adopted keeper must be alive for the current process")
	}
	if _, err := os.Stat(filepath.Join(p.layout.Root(), "keeper.json")); err != nil {
		t.Fatalf("keeper record not written: %v", err)
	}
}

// TestBootDistroReusesInProcessKeeper 验证本进程已有存活 keeper 时不再拉起新会话。
func TestBootDistroReusesInProcessKeeper(t *testing.T) {
	p := New(Config{BaseDir: t.TempDir()})
	p.keeper = &keeper{PID: os.Getpid(), Owner: os.Getpid(), StartedAt: time.Now()}

	// 走复用分支：不会执行任何 wsl 命令（本机没有同名发行版也不会报错）。
	if err := p.bootDistro(nil, "test-distro"); err != nil {
		t.Fatalf("bootDistro: %v", err)
	}
	if p.keeper == nil || p.keeper.PID != os.Getpid() {
		t.Fatalf("keeper = %+v, want reuse of current process", p.keeper)
	}
}

// TestBootDistroAdoptsDetachedKeeper 验证 detach 模式下认领上次 gobrave 留下的会话，
// 而不是重复拉起（重复拉起会让常驻会话越积越多）。
func TestBootDistroAdoptsDetachedKeeper(t *testing.T) {
	p := New(Config{BaseDir: t.TempDir(), Detach: true})
	p.writeKeeper(nil, &keeper{PID: os.Getpid(), Detach: true, Owner: os.Getpid(), StartedAt: time.Now()})

	if err := p.bootDistro(nil, "test-distro"); err != nil {
		t.Fatalf("bootDistro: %v", err)
	}
	if p.keeper == nil || p.keeper.PID != os.Getpid() {
		t.Fatalf("keeper = %+v, want adoption of recorded keeper", p.keeper)
	}
}
