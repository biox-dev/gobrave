package utils

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

func TestEnsureGitRepo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "script", "demo")

	repo, err := EnsureGitRepo(dir)
	if err != nil {
		t.Fatalf("EnsureGitRepo create: %v", err)
	}
	if repo == nil {
		t.Fatal("EnsureGitRepo returned nil repository")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("expected .git directory: %v", err)
	}
	head, err := repo.Reference(plumbing.HEAD, false)
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	if want := plumbing.NewBranchReferenceName("main"); head.Target() != want {
		t.Fatalf("default branch = %s, want %s", head.Target(), want)
	}
	// 全新仓库尚未产生提交，HEAD 处于未出生状态。
	if _, err := repo.Head(); !errors.Is(err, plumbing.ErrReferenceNotFound) {
		t.Fatalf("fresh repo Head() err = %v, want ErrReferenceNotFound", err)
	}

	// 二次调用应复用已有仓库，而不是报 ErrRepositoryAlreadyExists。
	repoAgain, err := EnsureGitRepo(dir)
	if err != nil {
		t.Fatalf("EnsureGitRepo reuse: %v", err)
	}
	if repoAgain == nil {
		t.Fatal("EnsureGitRepo reuse returned nil repository")
	}
	if _, err := repoAgain.Worktree(); err != nil {
		t.Fatalf("worktree on reused repo: %v", err)
	}

	// 目录内已有普通文件时也应能初始化。
	dir2 := filepath.Join(t.TempDir(), "script", "with-file")
	if err := os.MkdirAll(dir2, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir2, "main.py"), []byte("print('hi')\n"), 0o644); err != nil {
		t.Fatalf("write script: %v", err)
	}
	if _, err := EnsureGitRepo(dir2); err != nil {
		t.Fatalf("EnsureGitRepo with existing file: %v", err)
	}

	if _, err := git.PlainOpen(dir2); err != nil {
		t.Fatalf("repo not openable by go-git: %v", err)
	}
}

func TestCommitAll(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "script", "demo")
	repo, err := EnsureGitRepo(dir)
	if err != nil {
		t.Fatalf("EnsureGitRepo: %v", err)
	}
	identity := GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}

	// 工作区无变更时不产生空提交。
	if committed, err := CommitAll(repo, "noop", identity); err != nil || committed {
		t.Fatalf("CommitAll clean worktree = (%v, %v), want (false, nil)", committed, err)
	}

	// 首次提交（HEAD 未出生）应成功创建根提交。
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('hi')\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	committed, err := CommitAll(repo, "feat: add main.py", identity)
	if err != nil {
		t.Fatalf("CommitAll first: %v", err)
	}
	if !committed {
		t.Fatal("CommitAll first = false, want true")
	}

	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if want := plumbing.NewBranchReferenceName("main"); head.Name() != want {
		t.Fatalf("head branch = %s, want %s", head.Name(), want)
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("commit object: %v", err)
	}
	if commit.Message != "feat: add main.py" {
		t.Fatalf("commit message = %q", commit.Message)
	}
	if commit.Author.Name != identity.Name || commit.Author.Email != identity.Email {
		t.Fatalf("author = %s <%s>, want %s <%s>", commit.Author.Name, commit.Author.Email, identity.Name, identity.Email)
	}
	if len(commit.ParentHashes) != 0 {
		t.Fatalf("root commit parents = %v, want none", commit.ParentHashes)
	}

	// 修改文件后应产生第二个提交，并在 message 为空时使用默认文案。
	if err := os.WriteFile(filepath.Join(dir, "main.py"), []byte("print('hello')\n"), 0o644); err != nil {
		t.Fatalf("rewrite file: %v", err)
	}
	if committed, err := CommitAll(repo, "   ", identity); err != nil || !committed {
		t.Fatalf("CommitAll modified = (%v, %v), want (true, nil)", committed, err)
	}
	head, err = repo.Head()
	if err != nil {
		t.Fatalf("head after modify: %v", err)
	}
	commit, err = repo.CommitObject(head.Hash())
	if err != nil {
		t.Fatalf("commit object after modify: %v", err)
	}
	if commit.Message != defaultGitCommitMessage {
		t.Fatalf("default commit message = %q, want %q", commit.Message, defaultGitCommitMessage)
	}
	if len(commit.ParentHashes) != 1 {
		t.Fatalf("second commit parents = %v, want 1 parent", commit.ParentHashes)
	}

	// 身份为空必须报错，而不是写出没有 author 的提交。
	if _, err := CommitAll(repo, "x", GitIdentity{}); err == nil {
		t.Fatal("CommitAll with empty identity should fail")
	}
	if _, err := CommitAll(nil, "x", identity); err == nil {
		t.Fatal("CommitAll with nil repository should fail")
	}
}
