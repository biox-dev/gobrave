package utils

import (
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
)

// TestReadGitSyncState 覆盖本地/发布仓库同步状态的推导：
// 从未发布 -> 已发布同步 -> 本地有未提交改动 -> 本地有新提交未发布。
func TestReadGitSyncState(t *testing.T) {
	root := t.TempDir()
	localDir := filepath.Join(root, "data", "p1", "pipeline", "script", "s1")
	storeDir := filepath.Join(root, "store", "s1")
	identity := GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}

	// 1) 两侧都不存在：无状态、也不算有更新。
	if st := ReadGitSyncState(localDir, storeDir); st.HasLocalChanges || st.HasStoreChanges || st.InSync {
		t.Fatalf("empty dirs state = %+v, want all false", st)
	}

	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "main.py"), []byte("print(1)\n"), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	repo, err := EnsureGitRepo(localDir)
	if err != nil {
		t.Fatalf("EnsureGitRepo: %v", err)
	}
	if _, err := CommitAll(repo, "save script s1", identity); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}

	// 2) 本地有提交但从未发布。
	st := ReadGitSyncState(localDir, storeDir)
	if !st.LocalInitialized || st.StoreInitialized {
		t.Fatalf("initialized flags = %+v", st)
	}
	if !st.LocalAhead || !st.HasLocalChanges || st.HasStoreChanges || st.InSync {
		t.Fatalf("unpublished state = %+v, want local ahead", st)
	}

	// 3) 发布后再看：完全同步。
	if _, err := EnsureBareGitRepo(storeDir); err != nil {
		t.Fatalf("EnsureBareGitRepo: %v", err)
	}
	if _, err := PushDirToRepo(t.Context(), localDir, storeDir); err != nil {
		t.Fatalf("PushDirToRepo: %v", err)
	}
	st = ReadGitSyncState(localDir, storeDir)
	if !st.StoreInitialized || st.LocalCommit == "" || st.LocalCommit != st.StoreCommit {
		t.Fatalf("published state = %+v, want equal commits", st)
	}
	if st.HasLocalChanges || st.HasStoreChanges || !st.InSync {
		t.Fatalf("published state = %+v, want in sync", st)
	}

	// 4) 直接改文件（不提交）：本地脏。
	if err := os.WriteFile(filepath.Join(localDir, "main.py"), []byte("print(2)\n"), 0o644); err != nil {
		t.Fatalf("edit script: %v", err)
	}
	st = ReadGitSyncState(localDir, storeDir)
	if !st.LocalDirty || !st.HasLocalChanges || st.HasStoreChanges || st.InSync {
		t.Fatalf("dirty state = %+v, want local dirty only", st)
	}

	// 5) 提交后再看：本地领先 store（有已提交但未发布的改动）。
	repo, err = EnsureGitRepo(localDir)
	if err != nil {
		t.Fatalf("reopen repo: %v", err)
	}
	if _, err := CommitAll(repo, "save script s1", identity); err != nil {
		t.Fatalf("CommitAll again: %v", err)
	}
	st = ReadGitSyncState(localDir, storeDir)
	if st.LocalDirty || !st.LocalAhead || !st.HasLocalChanges || st.HasStoreChanges || st.InSync {
		t.Fatalf("ahead state = %+v, want local ahead without dirty", st)
	}
}

// TestReadGitSyncStateStoreAhead 覆盖 store 领先本地（本地 reset 回旧提交 / 远端有新提交）。
func TestReadGitSyncStateStoreAhead(t *testing.T) {
	root := t.TempDir()
	localDir := filepath.Join(root, "local")
	storeDir := filepath.Join(root, "store")
	identity := GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}

	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "main.py"), []byte("v1\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	repo, err := EnsureGitRepo(localDir)
	if err != nil {
		t.Fatalf("EnsureGitRepo: %v", err)
	}
	if _, err := CommitAll(repo, "v1", identity); err != nil {
		t.Fatalf("CommitAll v1: %v", err)
	}
	firstHead, err := repo.Head()
	if err != nil {
		t.Fatalf("head v1: %v", err)
	}

	if err := os.WriteFile(filepath.Join(localDir, "main.py"), []byte("v2\n"), 0o644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	if _, err := CommitAll(repo, "v2", identity); err != nil {
		t.Fatalf("CommitAll v2: %v", err)
	}
	if _, err := EnsureBareGitRepo(storeDir); err != nil {
		t.Fatalf("EnsureBareGitRepo: %v", err)
	}
	if _, err := PushDirToRepo(t.Context(), localDir, storeDir); err != nil {
		t.Fatalf("PushDirToRepo: %v", err)
	}

	// 本地回退到第一个提交，模拟 store 比本地更新
	// （v2 的 commit 对象仍留在本地对象库中，因此必须靠祖先关系而不是对象存在性判断方向）。
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if err := wt.Reset(&git.ResetOptions{Mode: git.HardReset, Commit: firstHead.Hash()}); err != nil {
		t.Fatalf("reset: %v", err)
	}

	st := ReadGitSyncState(localDir, storeDir)
	if !st.StoreAhead || !st.HasStoreChanges || st.HasLocalChanges || st.InSync {
		t.Fatalf("store ahead state = %+v", st)
	}
}
