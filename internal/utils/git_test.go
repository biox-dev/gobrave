package utils

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
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
	pushed, err := PushDirToRepo(ctx, srcDir, storeDir)
	if err != nil {
		t.Fatalf("PushDirToRepo: %v", err)
	}
	if !pushed {
		t.Fatal("PushDirToRepo first push should report pushed=true")
	}

	// 源仓库没有再提交时重复 push：远端已是同一个提交，应报 pushed=false 且不报错。
	pushed, err = PushDirToRepo(ctx, srcDir, storeDir)
	if err != nil {
		t.Fatalf("PushDirToRepo up-to-date should not fail: %v", err)
	}
	if pushed {
		t.Fatal("PushDirToRepo on unchanged source should report pushed=false")
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
	if pushed, err := PushDirToRepo(ctx, srcDir, storeDir); err != nil {
		t.Fatalf("PushDirToRepo v2: %v", err)
	} else if !pushed {
		t.Fatal("PushDirToRepo v2 should report pushed=true")
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
	if _, err := PushDirToRepo(ctx, emptyDir, filepath.Join(root, "store", "empty")); err == nil {
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
	if _, err := PushDirToRepo(ctx, srcDir, storeDir); err != nil {
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
	if _, err := PushDirToRepo(ctx, otherDir, storeDir); err != nil {
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
	if _, err := PushDirToRepo(ctx, srcDir, storeDir); err != nil {
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

// TestFetchBareRepoFromRemote 覆盖「裸仓库 pull」的语义（ReDownloadStore 用）：
// 裸仓库没有工作区，不能 Worktree().Pull，fetch 指定 remote 后本地分支应跟到远端同名分支，
// 且无更新时返回 git.NoErrAlreadyUpToDate（与 PullContext 一致）。
// remoteName 为空时回退 origin（下载 store 时 clone 出来的默认上游）。
func TestFetchBareRepoFromRemote(t *testing.T) {
	ctx := context.Background()
	identity := GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}
	// 只用进程内 file 传输：裸仓库之间不需要外部 git 可执行文件。
	t.Setenv("PATH", t.TempDir())

	root := t.TempDir()
	originDir := filepath.Join(root, "script", "script-1")
	originRepo, err := EnsureGitRepo(originDir)
	if err != nil {
		t.Fatalf("EnsureGitRepo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(originDir, "main.R"), []byte("print('v1')\n"), 0o644); err != nil {
		t.Fatalf("write main.R: %v", err)
	}
	if _, err := CommitAll(originRepo, "v1", identity); err != nil {
		t.Fatalf("CommitAll v1: %v", err)
	}

	// publish 产物：远端裸仓库。
	storeDir := filepath.Join(root, "store", "script-1")
	if _, err := EnsureBareGitRepo(storeDir); err != nil {
		t.Fatalf("EnsureBareGitRepo: %v", err)
	}
	if _, err := PushDirToRepo(ctx, originDir, storeDir); err != nil {
		t.Fatalf("PushDirToRepo v1: %v", err)
	}

	// DownloadStore 产物：从远端裸克隆出来的 store 裸仓库（origin 即上游）。
	cloneDir := filepath.Join(root, "download", "script-1")
	if _, err := git.PlainCloneContext(ctx, cloneDir, true, &git.CloneOptions{URL: storeDir}); err != nil {
		t.Fatalf("bare clone: %v", err)
	}
	cloneRepo, err := git.PlainOpen(cloneDir)
	if err != nil {
		t.Fatalf("open cloned repo: %v", err)
	}
	if _, err := cloneRepo.Worktree(); err == nil {
		t.Fatal("cloneDir should be a bare repository without worktree")
	}
	if got := repoHeadHash(t, cloneDir); got != repoHeadHash(t, storeDir) {
		t.Fatalf("cloned head = %s, want %s", got, repoHeadHash(t, storeDir))
	}

	// 远端无新提交：报告“已是最新”，不报错（remoteName 为空 => 回退 origin）。
	if err := FetchBareRepoFromRemote(ctx, cloneDir, ""); !errors.Is(err, git.NoErrAlreadyUpToDate) {
		t.Fatalf("fetch without changes = %v, want NoErrAlreadyUpToDate", err)
	}

	// store 裸仓库上可以有多个远端：再挂一个名为 github 的 remote 指向同一上游，
	// 覆盖「指定 remote 拉取」（/store/redownload 的 remote_name）。
	if _, err := cloneRepo.CreateRemote(&gitconfig.RemoteConfig{Name: "github", URLs: []string{storeDir}}); err != nil {
		t.Fatalf("CreateRemote github: %v", err)
	}

	// 源更新并再次 push 到远端后，从 github 拉取应把本地分支前移，且文件内容随之更新。
	if err := os.WriteFile(filepath.Join(originDir, "main.R"), []byte("print('v2')\n"), 0o644); err != nil {
		t.Fatalf("rewrite main.R: %v", err)
	}
	if _, err := CommitAll(originRepo, "v2", identity); err != nil {
		t.Fatalf("CommitAll v2: %v", err)
	}
	if _, err := PushDirToRepo(ctx, originDir, storeDir); err != nil {
		t.Fatalf("PushDirToRepo v2: %v", err)
	}

	if err := FetchBareRepoFromRemote(ctx, cloneDir, "github"); err != nil {
		t.Fatalf("FetchBareRepoFromRemote github: %v", err)
	}
	if got, want := repoHeadHash(t, cloneDir), repoHeadHash(t, storeDir); got != want {
		t.Fatalf("cloned head after fetch = %s, want %s", got, want)
	}
	content, err := ReadFileFromGitRepo(cloneDir, "main.R")
	if err != nil {
		t.Fatalf("ReadFileFromGitRepo: %v", err)
	}
	if string(content) != "print('v2')\n" {
		t.Fatalf("main.R after fetch = %q, want v2", string(content))
	}

	// origin 与 github 指向同一上游，此时已无更新。
	if err := FetchBareRepoFromRemote(ctx, cloneDir, "origin"); !errors.Is(err, git.NoErrAlreadyUpToDate) {
		t.Fatalf("second fetch = %v, want NoErrAlreadyUpToDate", err)
	}

	// 源再次更新后，默认（remoteName 为空）也应拉到最新。
	if err := os.WriteFile(filepath.Join(originDir, "main.R"), []byte("print('v3')\n"), 0o644); err != nil {
		t.Fatalf("rewrite main.R to v3: %v", err)
	}
	if _, err := CommitAll(originRepo, "v3", identity); err != nil {
		t.Fatalf("CommitAll v3: %v", err)
	}
	if _, err := PushDirToRepo(ctx, originDir, storeDir); err != nil {
		t.Fatalf("PushDirToRepo v3: %v", err)
	}
	if err := FetchBareRepoFromRemote(ctx, cloneDir, ""); err != nil {
		t.Fatalf("FetchBareRepoFromRemote default remote: %v", err)
	}
	content, err = ReadFileFromGitRepo(cloneDir, "main.R")
	if err != nil {
		t.Fatalf("ReadFileFromGitRepo v3: %v", err)
	}
	if string(content) != "print('v3')\n" {
		t.Fatalf("main.R after default fetch = %q, want v3", string(content))
	}
}

// TestFindFileInGitRepo 覆盖裸仓库内的文件名查找：根目录优先，其次任意层级（忽略大小写）。
func TestFindFileInGitRepo(t *testing.T) {
	srcDir := filepath.Join(t.TempDir(), "src")
	repo, err := EnsureGitRepo(srcDir)
	if err != nil {
		t.Fatalf("EnsureGitRepo: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "script", "s1"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "Script.JSON"), []byte(`{"script_id":"root"}`), 0o644); err != nil {
		t.Fatalf("write root file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "script", "s1", "script.json"), []byte(`{"script_id":"nested"}`), 0o644); err != nil {
		t.Fatalf("write nested file: %v", err)
	}
	if _, err := CommitAll(repo, "init", GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}

	// 根目录（忽略大小写）命中，不会命中嵌套同名文件。
	relPath, err := FindFileInGitRepo(srcDir, "script.json")
	if err != nil {
		t.Fatalf("FindFileInGitRepo root: %v", err)
	}
	if relPath != "Script.JSON" {
		t.Fatalf("relPath = %s, want root Script.JSON", relPath)
	}

	// 只有嵌套文件时返回嵌套相对路径。
	nestedDir := filepath.Join(t.TempDir(), "nested-src")
	nestedRepo, err := EnsureGitRepo(nestedDir)
	if err != nil {
		t.Fatalf("EnsureGitRepo nested: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(nestedDir, "a", "b"), 0o755); err != nil {
		t.Fatalf("mkdir a/b: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nestedDir, "a", "b", "workflow.json"), []byte(`{"workflow_id":"wf"}`), 0o644); err != nil {
		t.Fatalf("write nested workflow.json: %v", err)
	}
	if _, err := CommitAll(nestedRepo, "init", GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}); err != nil {
		t.Fatalf("CommitAll nested: %v", err)
	}
	if relPath, err = FindFileInGitRepo(nestedDir, "workflow.json"); err != nil {
		t.Fatalf("FindFileInGitRepo nested: %v", err)
	} else if relPath != "a/b/workflow.json" {
		t.Fatalf("relPath = %s, want a/b/workflow.json", relPath)
	}

	// 找不到 / 不是仓库：都要报错而不是返回空路径。
	if _, err := FindFileInGitRepo(nestedDir, "missing.json"); err == nil {
		t.Fatal("FindFileInGitRepo on missing file should fail")
	}
	if _, err := FindFileInGitRepo(filepath.Join(t.TempDir(), "not-a-repo"), "workflow.json"); err == nil {
		t.Fatal("FindFileInGitRepo on non-repository should fail")
	}
}

// TestStoreBranchFollowsPublishAndInstall 固定「分支跟随」约定：
//
//   - publish：store 的分支跟随工作目录（publish(dev) => store 变成 dev），并切换 store 的 HEAD；
//     换分支发布后读出的是新内容，旧分支按约定保留不删；
//   - install：目标目录的分支跟随 store（并切换 HEAD），目标目录已存在时同样生效；
//   - 历史遗留的坏 store（HEAD 悬挂在 main、内容在 master 上）在读取与安装时都能自愈。
func TestStoreBranchFollowsPublishAndInstall(t *testing.T) {
	ctx := context.Background()
	identity := GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}
	t.Setenv("PATH", t.TempDir())

	root := t.TempDir()
	// sourceWithBranch 建一个指定分支的脚本目录，并提交一个 script.json。
	sourceWithBranch := func(name, branch, content string) string {
		t.Helper()
		dir := filepath.Join(root, "script", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
			InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName(branch)},
		})
		if err != nil {
			t.Fatalf("init %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "script.json"), []byte(content), 0o644); err != nil {
			t.Fatalf("write script.json: %v", err)
		}
		if _, err := CommitAll(repo, "init "+branch, identity); err != nil {
			t.Fatalf("CommitAll %s: %v", dir, err)
		}
		return dir
	}
	branchOf := func(dir string) string {
		t.Helper()
		repo, err := git.PlainOpen(dir)
		if err != nil {
			t.Fatalf("open %s: %v", dir, err)
		}
		head, err := repo.Head()
		if err != nil {
			t.Fatalf("head %s: %v", dir, err)
		}
		return head.Name().Short()
	}

	storeDir := filepath.Join(root, "store", "script-1")
	if _, err := EnsureBareGitRepo(storeDir); err != nil {
		t.Fatalf("EnsureBareGitRepo: %v", err)
	}

	// 1) publish(dev)：store 的分支跟随成 dev，导出文件立刻可读。
	devDir := sourceWithBranch("dev-src", "dev", `{"script_id":"s1","v":"dev"}`)
	if pushed, err := PushDirToRepo(ctx, devDir, storeDir); err != nil || !pushed {
		t.Fatalf("PushDirToRepo dev = (%v, %v), want (true, nil)", pushed, err)
	}
	if got := branchOf(storeDir); got != "dev" {
		t.Fatalf("store branch after publish(dev) = %q, want dev", got)
	}
	assertStoreScriptJSON(t, storeDir, `{"script_id":"s1","v":"dev"}`)

	// 2) install 到不存在的目标目录：分支跟随 store(dev)。
	targetDir := filepath.Join(root, "install", "s1")
	if err := SyncWorktreeFromRepo(ctx, targetDir, storeDir); err != nil {
		t.Fatalf("SyncWorktreeFromRepo clone: %v", err)
	}
	if got := branchOf(targetDir); got != "dev" {
		t.Fatalf("installed branch = %q, want dev", got)
	}
	assertFileContent(t, filepath.Join(targetDir, "script.json"), `{"script_id":"s1","v":"dev"}`)

	// 3) 换成 main 分支再发布：store 切到 main，读出的是新内容（不是留在 dev 上的旧内容）。
	mainDir := sourceWithBranch("main-src", "main", `{"script_id":"s1","v":"main"}`)
	if _, err := PushDirToRepo(ctx, mainDir, storeDir); err != nil {
		t.Fatalf("PushDirToRepo main: %v", err)
	}
	if got := branchOf(storeDir); got != "main" {
		t.Fatalf("store branch after publish(main) = %q, want main", got)
	}
	assertStoreScriptJSON(t, storeDir, `{"script_id":"s1","v":"main"}`)

	// 旧分支保留不删（约定如此，install 之后仍可能有人按名字回溯）。
	storeRepo, err := git.PlainOpen(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := storeRepo.Reference(plumbing.NewBranchReferenceName("dev"), true); err != nil {
		t.Fatalf("unused branch dev should be kept: %v", err)
	}

	// 4) install 到已存在且分支为 dev 的目标目录：应切到 store 的 main 并覆盖内容。
	if err := SyncWorktreeFromRepo(ctx, targetDir, storeDir); err != nil {
		t.Fatalf("SyncWorktreeFromRepo pull: %v", err)
	}
	if got := branchOf(targetDir); got != "main" {
		t.Fatalf("target branch after install = %q, want main", got)
	}
	assertFileContent(t, filepath.Join(targetDir, "script.json"), `{"script_id":"s1","v":"main"}`)

	// 5) 历史遗留的坏 store：HEAD 悬挂在 main（不存在），内容在 master 上。
	legacyStore := filepath.Join(root, "store", "legacy")
	if _, err := EnsureBareGitRepo(legacyStore); err != nil {
		t.Fatalf("EnsureBareGitRepo legacy: %v", err)
	}
	legacySrc := sourceWithBranch("legacy-src", "master", `{"script_id":"s-legacy"}`)
	if _, err := PushDirToRepo(ctx, legacySrc, legacyStore); err != nil {
		t.Fatalf("PushDirToRepo legacy: %v", err)
	}
	legacyRepo, err := git.PlainOpen(legacyStore)
	if err != nil {
		t.Fatalf("open legacy store: %v", err)
	}
	if err := legacyRepo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName("main"))); err != nil {
		t.Fatalf("hang legacy HEAD: %v", err)
	}
	if _, err := legacyRepo.Head(); !errors.Is(err, plumbing.ErrReferenceNotFound) {
		t.Fatalf("legacy Head() err = %v, want reference not found", err)
	}
	// 读取侧应回退到实际分支 master，而不是报 reference not found。
	assertStoreScriptJSON(t, legacyStore, `{"script_id":"s-legacy"}`)
	// 安装也应成功，并且目标目录分支跟随实际分支 master。
	legacyTarget := filepath.Join(root, "install", "legacy")
	if err := SyncWorktreeFromRepo(ctx, legacyTarget, legacyStore); err != nil {
		t.Fatalf("SyncWorktreeFromRepo legacy: %v", err)
	}
	if got := branchOf(legacyTarget); got != "master" {
		t.Fatalf("legacy install branch = %q, want master", got)
	}
	// 再次 publish 会把坏 store 的 HEAD 修正到发布分支。
	if _, err := PushDirToRepo(ctx, legacySrc, legacyStore); err != nil {
		t.Fatalf("PushDirToRepo legacy again: %v", err)
	}
	if got := branchOf(legacyStore); got != "master" {
		t.Fatalf("legacy store branch after republish = %q, want master", got)
	}
}

// assertStoreScriptJSON 从 store 裸仓库当前分支读取 script.json 并比对内容。
func assertStoreScriptJSON(t *testing.T, storeDir, want string) {
	t.Helper()
	relPath, err := FindFileInGitRepo(storeDir, "script.json")
	if err != nil {
		t.Fatalf("FindFileInGitRepo %s: %v", storeDir, err)
	}
	content, err := ReadFileFromGitRepo(storeDir, relPath)
	if err != nil {
		t.Fatalf("ReadFileFromGitRepo %s: %v", storeDir, err)
	}
	if string(content) != want {
		t.Fatalf("store script.json = %q, want %q", string(content), want)
	}
}

// TestFetchBareRepoFromRemoteFollowsRemoteBranch 覆盖「检查更新」的分支兜底：
// 本地 store 的分支被 publish 改成 dev 后，远端默认分支仍是 main，此时不应报
// reference not found，而应跟随远端实际存在的分支，并把 store 的 HEAD 一起切过去。
func TestFetchBareRepoFromRemoteFollowsRemoteBranch(t *testing.T) {
	ctx := context.Background()
	identity := GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}
	t.Setenv("PATH", t.TempDir())

	root := t.TempDir()
	// 上游：只有 main 分支的裸仓库（模拟 GitHub 上默认分支为 main 的仓库）。
	upstreamSrc := filepath.Join(root, "upstream-src")
	if _, err := EnsureGitRepo(upstreamSrc); err != nil {
		t.Fatalf("EnsureGitRepo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(upstreamSrc, "script.json"), []byte(`{"v":"upstream"}`), 0o644); err != nil {
		t.Fatalf("write script.json: %v", err)
	}
	upstreamRepo, err := git.PlainOpen(upstreamSrc)
	if err != nil {
		t.Fatalf("open upstream src: %v", err)
	}
	if _, err := CommitAll(upstreamRepo, "upstream", identity); err != nil {
		t.Fatalf("CommitAll upstream: %v", err)
	}
	upstream := filepath.Join(root, "upstream.git")
	if _, err := EnsureBareGitRepo(upstream); err != nil {
		t.Fatalf("EnsureBareGitRepo upstream: %v", err)
	}
	if _, err := PushDirToRepo(ctx, upstreamSrc, upstream); err != nil {
		t.Fatalf("PushDirToRepo upstream: %v", err)
	}

	// DownloadStore 产物：从上游裸克隆出来的 store（origin 指向上游，分支 main）。
	storeDir := filepath.Join(root, "store", "script-1")
	if _, err := git.PlainCloneContext(ctx, storeDir, true, &git.CloneOptions{URL: upstream}); err != nil {
		t.Fatalf("bare clone store: %v", err)
	}

	// 本地 publish(dev)：store 的分支变成 dev，内容也换成 dev，但上游仍然只有 main。
	devSrc := filepath.Join(root, "dev-src")
	if err := os.MkdirAll(devSrc, 0o755); err != nil {
		t.Fatalf("mkdir dev-src: %v", err)
	}
	devRepo, err := git.PlainInitWithOptions(devSrc, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("dev")},
	})
	if err != nil {
		t.Fatalf("init dev-src: %v", err)
	}
	if err := os.WriteFile(filepath.Join(devSrc, "script.json"), []byte(`{"v":"dev"}`), 0o644); err != nil {
		t.Fatalf("write dev script.json: %v", err)
	}
	if _, err := CommitAll(devRepo, "dev", identity); err != nil {
		t.Fatalf("CommitAll dev: %v", err)
	}
	if _, err := PushDirToRepo(ctx, devSrc, storeDir); err != nil {
		t.Fatalf("PushDirToRepo dev: %v", err)
	}
	assertStoreScriptJSON(t, storeDir, `{"v":"dev"}`)

	// 检查更新：远端没有 dev 分支，应跟随远端唯一的分支 main，并把 store 切回 main。
	if err := FetchBareRepoFromRemote(ctx, storeDir, ""); err != nil {
		t.Fatalf("FetchBareRepoFromRemote: %v", err)
	}
	storeRepo, err := git.PlainOpen(storeDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	head, err := storeRepo.Head()
	if err != nil {
		t.Fatalf("store head: %v", err)
	}
	if got := head.Name().Short(); got != "main" {
		t.Fatalf("store branch after fetch = %q, want main", got)
	}
	assertStoreScriptJSON(t, storeDir, `{"v":"upstream"}`)

	// 已经跟到远端最新：再次检查更新应报告“已是最新”。
	if err := FetchBareRepoFromRemote(ctx, storeDir, ""); !errors.Is(err, git.NoErrAlreadyUpToDate) {
		t.Fatalf("second fetch = %v, want NoErrAlreadyUpToDate", err)
	}
}

// redactGitURLCredentials 必须抹掉 http(s) 地址里的密码（token），否则它会随错误信息/
// 接口 details 泄出；用户名与其它地址形态保持原样。
func TestRedactGitURLCredentials(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "https password is stripped",
			in:   "https://alice:ghp_secret@github.com/owner/repo.git",
			want: "https://alice@github.com/owner/repo.git",
		},
		{
			name: "https without credentials is unchanged",
			in:   "https://github.com/owner/repo.git",
			want: "https://github.com/owner/repo.git",
		},
		{
			name: "ssh scp-like url is unchanged",
			in:   "git@gitee.com:owner/repo.git",
			want: "git@gitee.com:owner/repo.git",
		},
		{
			name: "local path is unchanged",
			in:   "/tmp/store/remote.git",
			want: "/tmp/store/remote.git",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactGitURLCredentials(tc.in); got != tc.want {
				t.Fatalf("redactGitURLCredentials(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// resolveGitPushAuth 只在能拿到真正可用的凭据时才返回 AuthMethod，其余情况一律匿名：
// 本地路径绝不能带认证，未带凭据的 https 也不带（否则 go-git 会发出空的 BasicAuth）。
func TestResolveGitPushAuth(t *testing.T) {
	for _, raw := range []string{
		"",
		"https://github.com/owner/repo.git",
		"/tmp/store/remote.git",
		"file:///tmp/store/remote.git",
	} {
		auth, err := resolveGitPushAuth(raw)
		if err != nil {
			t.Fatalf("resolveGitPushAuth(%q) error = %v", raw, err)
		}
		if auth != nil {
			t.Fatalf("resolveGitPushAuth(%q) = %v, want nil (anonymous)", raw, auth)
		}
	}

	auth, err := resolveGitPushAuth("https://alice:ghp_secret@github.com/owner/repo.git")
	if err != nil {
		t.Fatalf("resolveGitPushAuth(https with creds) error = %v", err)
	}
	basic, ok := auth.(*githttp.BasicAuth)
	if !ok {
		t.Fatalf("resolveGitPushAuth(https with creds) = %T, want *http.BasicAuth", auth)
	}
	if basic.Username != "alice" || basic.Password != "ghp_secret" {
		t.Fatalf("basic auth = %s/%s, want alice/ghp_secret", basic.Username, basic.Password)
	}
}
