package utils

import (
	"context"
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

func TestEnsureBareGitRepo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "store", "script-1")

	repo, err := EnsureBareGitRepo(dir)
	if err != nil {
		t.Fatalf("EnsureBareGitRepo create: %v", err)
	}
	if repo == nil {
		t.Fatal("EnsureBareGitRepo returned nil repository")
	}
	// 裸仓库没有工作区：仓库文件直接位于 dir 下，不存在 .git 子目录。
	if _, err := os.Stat(filepath.Join(dir, "config")); err != nil {
		t.Fatalf("expected bare repository config file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatalf("bare repository should not have .git directory, stat err = %v", err)
	}
	cfg, err := repo.Config()
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !cfg.Core.IsBare {
		t.Fatal("expected Core.IsBare = true")
	}
	head, err := repo.Reference(plumbing.HEAD, false)
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	if want := plumbing.NewBranchReferenceName("main"); head.Target() != want {
		t.Fatalf("bare default branch = %s, want %s", head.Target(), want)
	}

	// 二次调用应复用已有裸仓库。
	repoAgain, err := EnsureBareGitRepo(dir)
	if err != nil {
		t.Fatalf("EnsureBareGitRepo reuse: %v", err)
	}
	if repoAgain == nil {
		t.Fatal("EnsureBareGitRepo reuse returned nil repository")
	}

	// 非裸仓库或残留普通文件目录应被清空重建为裸仓库。
	nonBare := filepath.Join(t.TempDir(), "store", "legacy")
	if _, err := EnsureGitRepo(nonBare); err != nil {
		t.Fatalf("EnsureGitRepo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nonBare, "script.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write legacy file: %v", err)
	}
	if _, err := EnsureBareGitRepo(nonBare); err != nil {
		t.Fatalf("EnsureBareGitRepo over non-bare dir: %v", err)
	}
	if _, err := os.Stat(filepath.Join(nonBare, "script.json")); !os.IsNotExist(err) {
		t.Fatalf("legacy worktree file should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(nonBare, "config")); err != nil {
		t.Fatalf("expected bare repository config file: %v", err)
	}
}

// TestPushDirToRepoAndSyncWorktree 覆盖 publish/install 的完整链路：
// 脚本目录 push 到 store 裸仓库，再把 store 同步回脚本目录（clone 与 pull 两种情况）。
//
// 同时把 PATH 置空，确保本地仓库之间的 clone / push 完全由 go-git 进程内实现完成，
// 不依赖外部的 git-upload-pack / git-receive-pack（生产镜像是 debian-slim，未装 git）。
func TestPushDirToRepoAndSyncWorktree(t *testing.T) {
	ctx := context.Background()
	identity := GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}
	t.Setenv("PATH", t.TempDir())

	root := t.TempDir()
	srcDir := filepath.Join(root, "script", "script-1")
	if _, err := EnsureGitRepo(srcDir); err != nil {
		t.Fatalf("EnsureGitRepo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.R"), []byte("print('v1')\n"), 0o644); err != nil {
		t.Fatalf("write main.R: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "script.json"), []byte(`{"script_id":"script-1"}`), 0o644); err != nil {
		t.Fatalf("write script.json: %v", err)
	}

	srcRepo, err := git.PlainOpen(srcDir)
	if err != nil {
		t.Fatalf("open source repo: %v", err)
	}
	if _, err := CommitAll(srcRepo, "save script script-1", identity); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	firstHead, err := srcRepo.Head()
	if err != nil {
		t.Fatalf("source head: %v", err)
	}

	storeDir := filepath.Join(root, "store", "script-1")
	if _, err := EnsureBareGitRepo(storeDir); err != nil {
		t.Fatalf("EnsureBareGitRepo: %v", err)
	}
	if err := PushDirToRepo(ctx, srcDir, storeDir); err != nil {
		t.Fatalf("PushDirToRepo: %v", err)
	}

	// store 裸仓库应拿到与脚本目录一致的 main 分支。
	storeRepo, err := git.PlainOpen(storeDir)
	if err != nil {
		t.Fatalf("open store repo: %v", err)
	}
	storeRef, err := storeRepo.Reference(plumbing.NewBranchReferenceName("main"), true)
	if err != nil {
		t.Fatalf("store main ref: %v", err)
	}
	if storeRef.Hash() != firstHead.Hash() {
		t.Fatalf("store main = %s, want %s", storeRef.Hash(), firstHead.Hash())
	}

	// 目标目录不存在：应 clone 出工作区并带上 script.json。
	targetDir := filepath.Join(root, "install", "script-1")
	if err := SyncWorktreeFromRepo(ctx, targetDir, storeDir); err != nil {
		t.Fatalf("SyncWorktreeFromRepo clone: %v", err)
	}
	assertFileContent(t, filepath.Join(targetDir, "main.R"), "print('v1')\n")
	if _, err := os.Stat(filepath.Join(targetDir, "script.json")); err != nil {
		t.Fatalf("cloned script.json: %v", err)
	}
	if _, err := git.PlainOpen(targetDir); err != nil {
		t.Fatalf("cloned target is not a git repository: %v", err)
	}

	// 脚本目录继续提交并再次 push，store 分支应前移。
	if err := os.WriteFile(filepath.Join(srcDir, "main.R"), []byte("print('v2')\n"), 0o644); err != nil {
		t.Fatalf("rewrite main.R: %v", err)
	}
	if _, err := CommitAll(srcRepo, "save script script-1 v2", identity); err != nil {
		t.Fatalf("CommitAll v2: %v", err)
	}
	if err := PushDirToRepo(ctx, srcDir, storeDir); err != nil {
		t.Fatalf("PushDirToRepo v2: %v", err)
	}

	// 目标目录已有仓库：本地改动（含未提交修改）应被 pull 覆盖。
	if err := os.WriteFile(filepath.Join(targetDir, "local.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatalf("write local file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "main.R"), []byte("print('local')\n"), 0o644); err != nil {
		t.Fatalf("modify tracked file: %v", err)
	}
	if err := SyncWorktreeFromRepo(ctx, targetDir, storeDir); err != nil {
		t.Fatalf("SyncWorktreeFromRepo pull: %v", err)
	}
	assertFileContent(t, filepath.Join(targetDir, "main.R"), "print('v2')\n")

	// 目标目录存在内容但不是 git 仓库（旧版本安装留下的普通文件目录）：
	// 应就地初始化仓库并覆盖为 store 内容，语义与 pull 一致。
	legacyDir := filepath.Join(root, "install", "legacy")
	if err := os.MkdirAll(legacyDir, 0o755); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacyDir, "main.R"), []byte("print('legacy')\n"), 0o644); err != nil {
		t.Fatalf("write legacy main.R: %v", err)
	}
	if err := SyncWorktreeFromRepo(ctx, legacyDir, storeDir); err != nil {
		t.Fatalf("SyncWorktreeFromRepo on legacy dir: %v", err)
	}
	assertFileContent(t, filepath.Join(legacyDir, "main.R"), "print('v2')\n")
	if _, err := git.PlainOpen(legacyDir); err != nil {
		t.Fatalf("legacy dir was not adopted as a git repository: %v", err)
	}

	// 源仓库没有任何提交时不能 push。
	emptyDir := filepath.Join(root, "script", "empty")
	if _, err := EnsureGitRepo(emptyDir); err != nil {
		t.Fatalf("EnsureGitRepo empty: %v", err)
	}
	if err := PushDirToRepo(ctx, emptyDir, filepath.Join(root, "store", "empty")); err == nil {
		t.Fatal("PushDirToRepo with unborn HEAD should fail")
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, string(got), want)
	}
}

// TestPushDirToRepoForceOverwritesRemote 明确 PushDirToRepo 的冲突语义：
// 本地分支与远端分叉（非快进）时不会报错，而是直接用本地覆盖远端，远端独有的提交会被丢弃。
func TestPushDirToRepoForceOverwritesRemote(t *testing.T) {
	ctx := context.Background()
	identity := GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}
	t.Setenv("PATH", t.TempDir())

	root := t.TempDir()
	commitFile := func(dir, name, content, message string) plumbing.Hash {
		t.Helper()
		if _, err := EnsureGitRepo(dir); err != nil {
			t.Fatalf("EnsureGitRepo %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s/%s: %v", dir, name, err)
		}
		repo, err := git.PlainOpen(dir)
		if err != nil {
			t.Fatalf("open %s: %v", dir, err)
		}
		if _, err := CommitAll(repo, message, identity); err != nil {
			t.Fatalf("CommitAll %s: %v", dir, err)
		}
		return repoHeadHash(t, dir)
	}

	srcDir := filepath.Join(root, "script", "script-1")
	firstLocal := commitFile(srcDir, "main.R", "print('v1')\n", "v1")

	storeDir := filepath.Join(root, "store", "script-1")
	if _, err := EnsureBareGitRepo(storeDir); err != nil {
		t.Fatalf("EnsureBareGitRepo: %v", err)
	}
	if err := PushDirToRepo(ctx, srcDir, storeDir); err != nil {
		t.Fatalf("PushDirToRepo initial: %v", err)
	}
	if got := repoHeadHash(t, storeDir); got != firstLocal {
		t.Fatalf("store head = %s, want %s", got, firstLocal)
	}

	// 模拟"远端已有人推送"：从 store 克隆到别处，提交一个远端独有的提交再推回 store。
	otherDir := filepath.Join(root, "other")
	if err := SyncWorktreeFromRepo(ctx, otherDir, storeDir); err != nil {
		t.Fatalf("SyncWorktreeFromRepo other: %v", err)
	}
	remoteOnly := commitFile(otherDir, "remote.txt", "remote only\n", "remote only")
	if err := PushDirToRepo(ctx, otherDir, storeDir); err != nil {
		t.Fatalf("PushDirToRepo remote: %v", err)
	}
	if got := repoHeadHash(t, storeDir); got != remoteOnly {
		t.Fatalf("store head = %s, want %s", got, remoteOnly)
	}

	// 本地独立提交一个与 remoteOnly 分叉的提交（两者无祖先关系），再 push：
	// 非快进更新不应报错，本地应直接覆盖远端。
	localHead := commitFile(srcDir, "main.R", "print('v2')\n", "v2")
	if localHead == remoteOnly {
		t.Fatal("expected local and remote heads to diverge")
	}
	if err := PushDirToRepo(ctx, srcDir, storeDir); err != nil {
		t.Fatalf("PushDirToRepo on diverged history should force-update, got: %v", err)
	}
	if got := repoHeadHash(t, storeDir); got != localHead {
		t.Fatalf("store head after conflicting push = %s, want local %s", got, localHead)
	}

	// 远端独有内容应被本地内容完全替换。
	checkDir := filepath.Join(root, "check")
	if err := SyncWorktreeFromRepo(ctx, checkDir, storeDir); err != nil {
		t.Fatalf("SyncWorktreeFromRepo check: %v", err)
	}
	assertFileContent(t, filepath.Join(checkDir, "main.R"), "print('v2')\n")
	if _, err := os.Stat(filepath.Join(checkDir, "remote.txt")); !os.IsNotExist(err) {
		t.Fatalf("remote-only file should be gone after force push, stat err = %v", err)
	}
}

func repoHeadHash(t *testing.T, dir string) plumbing.Hash {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head %s: %v", dir, err)
	}
	return head.Hash()
}
