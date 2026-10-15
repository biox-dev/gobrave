package utils

import (
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureReportWorktreeGitRepoCreatesRepository 首次调用应初始化仓库并报告 created=true。
func TestEnsureReportWorktreeGitRepoCreatesRepository(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "report", "1")

	repo, created, err := EnsureReportWorktreeGitRepo(dir)
	if err != nil {
		t.Fatalf("EnsureReportWorktreeGitRepo: %v", err)
	}
	if repo == nil {
		t.Fatal("repo is nil")
	}
	if !created {
		t.Fatal("created = false, want true for a fresh directory")
	}

	// 再次调用应复用已有仓库（不重复初始化）。
	if _, createdAgain, err := EnsureReportWorktreeGitRepo(dir); err != nil {
		t.Fatalf("EnsureReportWorktreeGitRepo (second): %v", err)
	} else if createdAgain {
		t.Fatal("created = true on second call, want false")
	}
}

// TestEnsureReportWorktreeGitRepoAdoptsPlainDirectory 目录已存在普通文件时也应能就地初始化，
// 且已有文件保留为未跟踪改动（不会被删除）。
func TestEnsureReportWorktreeGitRepoAdoptsPlainDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "report", "2")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	file := filepath.Join(dir, "output.md")
	if err := os.WriteFile(file, []byte("# Report\n"), 0o644); err != nil {
		t.Fatalf("write output.md: %v", err)
	}

	repo, created, err := EnsureReportWorktreeGitRepo(dir)
	if err != nil {
		t.Fatalf("EnsureReportWorktreeGitRepo: %v", err)
	}
	if !created {
		t.Fatal("created = false, want true when adopting a plain directory")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("output.md should be preserved: %v", err)
	}

	// 已有内容在首次提交前表现为未提交改动。
	if _, err := CommitAll(repo, "init project report", GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if got := ReadGitDiff(dir, "").Worktree.FileCount; got != 0 {
		t.Fatalf("worktree file count after init commit = %d, want 0", got)
	}
}

// TestEnsureReportWorktreeGitRepoEmptyDir 空目录名应返回 ErrReportDirEmpty。
func TestEnsureReportWorktreeGitRepoEmptyDir(t *testing.T) {
	if _, _, err := EnsureReportWorktreeGitRepo("   "); err != ErrReportDirEmpty {
		t.Fatalf("err = %v, want ErrReportDirEmpty", err)
	}
}

// TestInitReportWorktreeGitRepoIdempotent 惰性初始化应是幂等 no-op。
func TestInitReportWorktreeGitRepoIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "report", "3")
	if err := InitReportWorktreeGitRepo(dir); err != nil {
		t.Fatalf("InitReportWorktreeGitRepo: %v", err)
	}
	if err := InitReportWorktreeGitRepo(dir); err != nil {
		t.Fatalf("InitReportWorktreeGitRepo (second): %v", err)
	}
}

// TestReadReportWorktreeGitState 覆盖未初始化 / 已初始化干净 / 已初始化有改动三种状态。
func TestReadReportWorktreeGitState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "report", "4")

	state := ReadReportWorktreeGitState(dir)
	if state.Initialized || state.Dirty || state.Commit != "" {
		t.Fatalf("state for missing dir = %+v, want zero value", state)
	}

	repo, _, err := EnsureReportWorktreeGitRepo(dir)
	if err != nil {
		t.Fatalf("EnsureReportWorktreeGitRepo: %v", err)
	}
	// 空目录提交不产生提交（CommitAll 无变更时是 no-op），HEAD 仍未出生。
	if err := os.WriteFile(filepath.Join(dir, "output.md"), []byte("# v1\n"), 0o644); err != nil {
		t.Fatalf("write output.md: %v", err)
	}
	if _, err := CommitAll(repo, "init", GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}

	state = ReadReportWorktreeGitState(dir)
	if !state.Initialized || state.Dirty || state.Commit == "" {
		t.Fatalf("state after init = %+v, want initialized+clean+commit", state)
	}

	if err := os.WriteFile(filepath.Join(dir, "output.md"), []byte("# changed\n"), 0o644); err != nil {
		t.Fatalf("write output.md: %v", err)
	}
	if state = ReadReportWorktreeGitState(dir); !state.Dirty {
		t.Fatalf("state after edit = %+v, want dirty", state)
	}
}

// TestReportWorktreeHasFile 覆盖存在性判断（含空入参）。
func TestReportWorktreeHasFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "report", "5")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if ReportWorktreeHasFile(dir, "output.md") {
		t.Fatal("has file = true for missing file")
	}
	if ReportWorktreeHasFile(dir, "") {
		t.Fatal("has file = true for empty filename")
	}
	if err := os.WriteFile(filepath.Join(dir, "output.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write output.md: %v", err)
	}
	if !ReportWorktreeHasFile(dir, "output.md") {
		t.Fatal("has file = false for existing file")
	}
}
