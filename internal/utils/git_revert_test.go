package utils

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// newRevertTestRepo 建一个已提交 main.py 的工作区仓库，返回目录。
func newRevertTestRepo(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "script", "demo")
	repo, err := EnsureGitRepo(dir)
	if err != nil {
		t.Fatalf("EnsureGitRepo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('v1')\n"), 0o644); err != nil {
		t.Fatalf("write main.py: %v", err)
	}
	if _, err := CommitAll(repo, "init", GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	return dir
}

// worktreeFileCount 返回工作区未提交改动里的文件数（用 ReadGitDiff 复用同一条只读链路）。
func worktreeFileCount(t *testing.T, dir string) int {
	t.Helper()
	return ReadGitDiff(dir, "").Worktree.FileCount
}

func TestRevertWorktreeFileRestoresModifiedFile(t *testing.T) {
	dir := newRevertTestRepo(t)

	target := filepath.Join(dir, "main.py")
	if err := os.WriteFile(target, []byte("print('v2-modified')\n"), 0o644); err != nil {
		t.Fatalf("modify main.py: %v", err)
	}
	if got := worktreeFileCount(t, dir); got != 1 {
		t.Fatalf("worktree file count before revert = %d, want 1", got)
	}

	if err := RevertWorktreeFile(dir, "main.py"); err != nil {
		t.Fatalf("RevertWorktreeFile: %v", err)
	}

	content, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read main.py: %v", err)
	}
	if string(content) != "print('v1')\n" {
		t.Fatalf("content after revert = %q, want HEAD content", string(content))
	}
	if got := worktreeFileCount(t, dir); got != 0 {
		t.Fatalf("worktree file count after revert = %d, want 0", got)
	}

	// 幂等：内容已与 HEAD 一致时再次回退不报错。
	if err := RevertWorktreeFile(dir, "main.py"); err != nil {
		t.Fatalf("RevertWorktreeFile idempotent: %v", err)
	}
}

func TestRevertWorktreeFileDropsIndexChangeForStagedFile(t *testing.T) {
	dir := newRevertTestRepo(t)

	target := filepath.Join(dir, "main.py")
	if err := os.WriteFile(target, []byte("print('staged')\n"), 0o644); err != nil {
		t.Fatalf("modify main.py: %v", err)
	}
	repo, err := EnsureGitRepo(dir)
	if err != nil {
		t.Fatalf("EnsureGitRepo: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add("main.py"); err != nil {
		t.Fatalf("stage main.py: %v", err)
	}
	// 暂存后即使工作区再改回 HEAD 内容也应保持干净，因此先确认索引指向改动内容。
	if err := RevertWorktreeFile(dir, "main.py"); err != nil {
		t.Fatalf("RevertWorktreeFile: %v", err)
	}

	status, err := wt.Status()
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !status.IsClean() {
		t.Fatalf("worktree status after revert = %s, want clean", status)
	}
}

func TestRevertWorktreeFileRemovesNewFile(t *testing.T) {
	dir := newRevertTestRepo(t)

	newFile := filepath.Join(dir, "extra.py")
	if err := os.WriteFile(newFile, []byte("print('new')\n"), 0o644); err != nil {
		t.Fatalf("write extra.py: %v", err)
	}
	if err := RevertWorktreeFile(dir, "extra.py"); err != nil {
		t.Fatalf("RevertWorktreeFile: %v", err)
	}
	if _, err := os.Stat(newFile); !os.IsNotExist(err) {
		t.Fatalf("extra.py still exists after revert (err=%v)", err)
	}
	if got := worktreeFileCount(t, dir); got != 0 {
		t.Fatalf("worktree file count after revert = %d, want 0", got)
	}

	// 已暂存的新增文件同样应被删除并清掉索引项。
	staged := filepath.Join(dir, "staged.py")
	if err := os.WriteFile(staged, []byte("print('staged new')\n"), 0o644); err != nil {
		t.Fatalf("write staged.py: %v", err)
	}
	repo, err := EnsureGitRepo(dir)
	if err != nil {
		t.Fatalf("EnsureGitRepo: %v", err)
	}
	wt, err := repo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	if _, err := wt.Add("staged.py"); err != nil {
		t.Fatalf("stage staged.py: %v", err)
	}
	if err := RevertWorktreeFile(dir, "staged.py"); err != nil {
		t.Fatalf("RevertWorktreeFile staged: %v", err)
	}
	if _, err := os.Stat(staged); !os.IsNotExist(err) {
		t.Fatalf("staged.py still exists after revert (err=%v)", err)
	}
	if status, err := wt.Status(); err != nil {
		t.Fatalf("status: %v", err)
	} else if !status.IsClean() {
		t.Fatalf("worktree status after staged revert = %s, want clean", status)
	}
}

func TestRevertWorktreeFileErrors(t *testing.T) {
	dir := newRevertTestRepo(t)

	cases := []struct {
		name    string
		dir     string
		relPath string
		wantErr error
	}{
		{"empty path", dir, "  ", ErrGitPathInvalid},
		{"absolute path", dir, "/etc/passwd", ErrGitPathInvalid},
		{"parent escape", dir, "../outside.py", ErrGitPathInvalid},
		{"git dir", dir, ".git/config", ErrGitPathInvalid},
		{"absent file", dir, "missing.py", ErrGitFileAbsent},
		{"not a repo", t.TempDir(), "main.py", ErrGitRepoNotInitialized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := RevertWorktreeFile(tc.dir, tc.relPath)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("RevertWorktreeFile(%q, %q) err = %v, want %v", tc.dir, tc.relPath, err, tc.wantErr)
			}
		})
	}
}
