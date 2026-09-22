package utils

import (
	"context"
	stderrs "errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
)

// defaultGitCommitMessage 是调用方未提供 commit message 时的兜底文案。
const defaultGitCommitMessage = "update repository"

// storeRemoteName 是脚本目录推送到本地 store 仓库时使用的 remote 名称。
const storeRemoteName = "origin"

// localGitTransportOnce 保证 file 协议只被替换一次。
var localGitTransportOnce sync.Once

// ensureLocalGitTransport 把 go-git 的 file 协议替换为进程内的 git server 实现。
//
// go-git 自带的 file 传输会调用外部 git-upload-pack / git-receive-pack 可执行文件，
// 而生产运行镜像（debian:bookworm-slim）默认不安装 git；换成 go-git 内置的 server
// 后，本地仓库之间的 clone / push 完全在 Go 进程内完成，不再依赖外部 git。
// 该注册是进程级且幂等的，只影响 file 协议（远端 http/ssh 行为不变）。
func ensureLocalGitTransport() {
	localGitTransportOnce.Do(func() {
		client.InstallProtocol("file", server.NewClient(server.DefaultLoader))
	})
}

// GitIdentity 是 git 提交时使用的身份信息，对应 config.GitConfig。
type GitIdentity struct {
	Name  string
	Email string
}

// EnsureGitRepo 在 dir 目录下初始化一个非裸（带工作区）的 git 仓库。
// 若 dir 下已存在仓库，则直接打开并复用，不会清空已有历史。
// DefaultBranch 固定为 main，避免受本机 git 默认分支配置影响。
func EnsureGitRepo(dir string) (*git.Repository, error) {
	if dir == "" {
		return nil, stderrs.New("git repository directory is empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	repo, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.Main},
	})
	if err != nil {
		if stderrs.Is(err, git.ErrRepositoryAlreadyExists) {
			return git.PlainOpen(dir)
		}
		return nil, err
	}
	return repo, nil
}

// CommitAll 把仓库工作区的全部变更（新增/修改/删除）暂存并提交为一个新的 commit。
//
//   - 工作区无变更时返回 (false, nil)，不会产生空提交；
//   - message 为空时使用默认文案 "update repository"；
//   - identity 的 Name/Email 必须非空（调用方应通过 config.ResolveGitIdentity 取值，
//     未配置 git 段时会回退到默认身份）。
//
// 仓库首次提交（HEAD 尚未出生）也能正常工作：会在当前分支上创建根提交。
func CommitAll(repo *git.Repository, message string, identity GitIdentity) (bool, error) {
	if repo == nil {
		return false, stderrs.New("git repository is nil")
	}
	name := strings.TrimSpace(identity.Name)
	email := strings.TrimSpace(identity.Email)
	if name == "" || email == "" {
		return false, stderrs.New("git commit identity (user/email) must not be empty")
	}

	message = strings.TrimSpace(message)
	if message == "" {
		message = defaultGitCommitMessage
	}

	wt, err := repo.Worktree()
	if err != nil {
		return false, err
	}

	status, err := wt.Status()
	if err != nil {
		return false, err
	}
	if status.IsClean() {
		return false, nil
	}

	// 等价于 git add -A：把未跟踪、已修改、已删除的文件全部写入暂存区。
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return false, err
	}

	sig := &object.Signature{Name: name, Email: email, When: time.Now()}
	if _, err := wt.Commit(message, &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		// Status 与 Commit 之间若被其他流程抢先提交，则当前已无变更，按幂等处理。
		if stderrs.Is(err, git.ErrEmptyCommit) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// CommitDirChanges 确保 dir 是一个 git 仓库（不存在则初始化，默认分支 main），
// 并把当前工作区改动提交为一个 commit（无变更时不会产生空提交）。
//
// 这是脚本/工作流导出目录共用的提交步骤：提交路径（Codec 的 WriteCommitScriptFiles /
// WriteCommitWorkflowFiles）落盘后调用它，保证目录内容始终有对应的 git 提交，
// 供后续 PushDirToRepo 推送到 store。
func CommitDirChanges(dir, commitMessage string, identity GitIdentity) error {
	repo, err := EnsureGitRepo(dir)
	if err != nil {
		return fmt.Errorf("failed to init git repository %s: %w", dir, err)
	}
	if _, err := CommitAll(repo, commitMessage, identity); err != nil {
		return fmt.Errorf("failed to commit changes in %s: %w", dir, err)
	}
	return nil
}

// EnsureBareGitRepo 保证 dir 是一个裸（bare）git 仓库并返回它，用作本地推送目标：
//
//   - dir 不存在：初始化裸仓库（默认分支 main）
//   - dir 已是裸仓库：直接复用，保留历史，便于多次 publish 增量推送
//   - dir 存在但不是裸仓库（例如旧版本留下的普通文件产物）：清空后重新初始化
//
// DefaultBranch 固定为 main，避免受本机 git 默认分支配置影响。
func EnsureBareGitRepo(dir string) (*git.Repository, error) {
	ensureLocalGitTransport()

	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, stderrs.New("git repository directory is empty")
	}

	if repo, err := git.PlainOpen(dir); err == nil {
		if cfg, cfgErr := repo.Config(); cfgErr == nil && cfg.Core.IsBare {
			return repo, nil
		}
	} else if !stderrs.Is(err, git.ErrRepositoryNotExists) {
		return nil, err
	}

	// 非裸仓库或残留的普通文件目录：整体重建，避免把工作区文件混进裸仓库。
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	return git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.Main},
		Bare:        true,
	})
}

// PushDirToRepo 把 sourceDir 仓库的当前分支推送到 targetRepoPath 对应的本地仓库。
//
// 等价于 `git remote add/ set-url <origin> <targetRepoPath>` 后再 `git push`：
// 每次调用都会把名为 origin 的 remote 指向最新的 targetRepoPath（base_dir 变更后
// 无需手工改 remote 配置），并以 force 方式推送，使目标仓库与源分支内容完全一致。
//
// 源仓库没有 commit（HEAD 未出生）时返回错误，调用方应提示先保存。
func PushDirToRepo(ctx context.Context, sourceDir, targetRepoPath string) error {
	ensureLocalGitTransport()

	sourceDir = strings.TrimSpace(sourceDir)
	targetRepoPath = strings.TrimSpace(targetRepoPath)
	if sourceDir == "" || targetRepoPath == "" {
		return stderrs.New("git source directory and target repository path must not be empty")
	}

	repo, err := git.PlainOpen(sourceDir)
	if err != nil {
		return fmt.Errorf("open source git repository %q: %w", sourceDir, err)
	}

	head, err := repo.Head()
	if err != nil {
		if stderrs.Is(err, plumbing.ErrReferenceNotFound) {
			return fmt.Errorf("source git repository %q has no commit yet", sourceDir)
		}
		return err
	}

	// remote 已存在时先删除再加回，等价于 git remote set-url，避免残留旧地址。
	if delErr := repo.DeleteRemote(storeRemoteName); delErr != nil && !stderrs.Is(delErr, git.ErrRemoteNotFound) {
		return delErr
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{
		Name: storeRemoteName,
		URLs: []string{targetRepoPath},
	}); err != nil {
		return err
	}

	// "+" 前缀表示允许非快进更新：store 是脚本目录的发布镜像，需要与源分支完全一致。
	refSpec := gitconfig.RefSpec(fmt.Sprintf("+%s:%s", head.Name(), head.Name()))
	if err := repo.PushContext(ctx, &git.PushOptions{
		RemoteName: storeRemoteName,
		RefSpecs:   []gitconfig.RefSpec{refSpec},
	}); err != nil {
		return fmt.Errorf("push %q to %q: %w", sourceDir, targetRepoPath, err)
	}
	return nil
}

// SyncWorktreeFromRepo 让 targetDir 的工作区与 srcRepoPath 仓库保持一致：
//
//   - targetDir 已是 git 仓库：从 srcRepoPath fetch 后 reset --hard，覆盖本地改动
//   - targetDir 不存在或为空目录：从 srcRepoPath clone（非裸克隆，带工作区）
//   - targetDir 存在已有内容但不是 git 仓库（旧版本安装留下的普通文件目录）：
//     就地初始化仓库后再 reset --hard，语义同"覆盖本地"
//
// srcRepoPath 既可以是裸仓库（publish 产物），也可以是普通工作区仓库。
func SyncWorktreeFromRepo(ctx context.Context, targetDir, srcRepoPath string) error {
	ensureLocalGitTransport()

	targetDir = strings.TrimSpace(targetDir)
	srcRepoPath = strings.TrimSpace(srcRepoPath)
	if targetDir == "" || srcRepoPath == "" {
		return stderrs.New("git target directory and source repository path must not be empty")
	}

	repo, openErr := git.PlainOpen(targetDir)
	if openErr == nil {
		return resetWorktreeToRemote(ctx, repo, targetDir, srcRepoPath)
	}
	if !stderrs.Is(openErr, git.ErrRepositoryNotExists) {
		return openErr
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(targetDir)
	if err != nil {
		return err
	}

	if len(entries) == 0 {
		if _, err := git.PlainCloneContext(ctx, targetDir, false, &git.CloneOptions{URL: srcRepoPath}); err != nil {
			return fmt.Errorf("clone %q to %q: %w", srcRepoPath, targetDir, err)
		}
		return nil
	}

	repo, err = EnsureGitRepo(targetDir)
	if err != nil {
		return err
	}
	return resetWorktreeToRemote(ctx, repo, targetDir, srcRepoPath)
}

// fetchFromLocalRepo 从本地源仓库 fetch；若因“本地独有的提交”导致协商失败
// （plumbing.ErrObjectNotFound，对外表现为 "object not found"），清理源仓库不认识的
// 本地 ref 后重试一次。
//
// 背景：go-git 的 fetch 协商（Remote.fetch -> getHaves）会把本地仓库里每个 ref 的可达提交
// （每个 ref 最多 100 个祖先提交）当作 haves 发给对端。源仓库是本地路径时，对端就是
// ensureLocalGitTransport 注册的进程内 go-git server，它的 upload-pack 用
// revlist.Objects(storer, haves, nil) 展开 haves，且不允许对象缺失：只要有一个 have
// 在源仓库里不存在，整次 fetch 就会以 plumbing.ErrObjectNotFound 失败。
// 真实的 git 服务端会忽略自己不认识 have，go-git 的进程内服务端不会，这就是同一份仓库
// 用 git 命令行能同步、走 go-git 却报 "object not found" 的原因。
//
// 触发场景：目标目录里存在本地提交（编辑脚本/工作流后提交但尚未 publish，或 store 之后被
// 上游强制改写），而该提交在 store 里并不存在。这类提交在随后的 reset --hard / 分支重指里
// 本来就会被丢弃，所以这里先删掉对应 ref 再重试，避免同步被这种“本地独有提交”卡死。
func fetchFromLocalRepo(ctx context.Context, repo *git.Repository, opts *git.FetchOptions) error {
	err := repo.FetchContext(ctx, opts)
	if err == nil || !stderrs.Is(err, plumbing.ErrObjectNotFound) {
		return err
	}

	// 源仓库地址取自 remote 配置；远端 URL 或未配置 remote 时 PlainOpen 会失败，
	// 此时保留原始错误。
	source, openErr := git.PlainOpen(remoteURL(repo, opts.RemoteName))
	if openErr != nil {
		return err
	}
	if pruneErr := pruneLocalRefsUnknownToSource(repo, source); pruneErr != nil {
		return err
	}
	return repo.FetchContext(ctx, opts)
}

// pruneLocalRefsUnknownToSource 删除 repo 中那些提交对象在 source 仓库里不存在的 ref，
// 避免它们被 getHaves 当成 haves 发给对端（见 fetchFromLocalRepo 的说明）。
// 只有 source 明确返回 ErrObjectNotFound 的 ref 才会被删除，其它错误一律不动。
func pruneLocalRefsUnknownToSource(repo, source *git.Repository) error {
	iter, err := repo.References()
	if err != nil {
		return err
	}

	var stale []plumbing.ReferenceName
	iterErr := iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference {
			return nil
		}
		if objErr := source.Storer.HasEncodedObject(ref.Hash()); stderrs.Is(objErr, plumbing.ErrObjectNotFound) {
			stale = append(stale, ref.Name())
		}
		return nil
	})
	// 先把迭代器关掉再改 refs，避免边遍历边删除。
	iter.Close()
	if iterErr != nil {
		return iterErr
	}

	for _, name := range stale {
		if err := repo.Storer.RemoveReference(name); err != nil {
			return fmt.Errorf("remove local reference %q: %w", name, err)
		}
	}
	return nil
}

// remoteURL 返回 repo 中指定 remote 的第一个地址，取不到时返回空串。
func remoteURL(repo *git.Repository, remoteName string) string {
	cfg, err := repo.Config()
	if err != nil {
		return ""
	}
	rc, ok := cfg.Remotes[remoteName]
	if !ok || len(rc.URLs) == 0 {
		return ""
	}
	return rc.URLs[0]
}

// resetWorktreeToRemote 让已存在的仓库工作区与远端仓库 srcRepoPath 完全一致：
// 重置 origin 地址 -> fetch -> reset --hard 到远端同名分支。
func resetWorktreeToRemote(ctx context.Context, repo *git.Repository, targetDir, srcRepoPath string) error {
	if delErr := repo.DeleteRemote(storeRemoteName); delErr != nil && !stderrs.Is(delErr, git.ErrRemoteNotFound) {
		return delErr
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{
		Name:  storeRemoteName,
		URLs:  []string{srcRepoPath},
		Fetch: []gitconfig.RefSpec{gitconfig.RefSpec("+refs/heads/*:refs/remotes/" + storeRemoteName + "/*")},
	}); err != nil {
		return err
	}

	branch := plumbing.Main.Short()
	if head, err := repo.Head(); err == nil && head.Name().IsBranch() {
		branch = head.Name().Short()
	}

	if err := fetchFromLocalRepo(ctx, repo, &git.FetchOptions{
		RemoteName: storeRemoteName,
		RefSpecs:   []gitconfig.RefSpec{gitconfig.RefSpec("+refs/heads/*:refs/remotes/" + storeRemoteName + "/*")},
		Force:      true,
	}); err != nil && !stderrs.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetch %q: %w", srcRepoPath, err)
	}

	remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName(storeRemoteName, branch), true)
	if err != nil {
		return fmt.Errorf("resolve remote branch %q of %q: %w", branch, srcRepoPath, err)
	}

	// 就地初始化出来的空仓库 HEAD 处于未出生状态，先建出同名分支，
	// 否则 Reset 无法定位当前分支（go-git 会报 reference not found）。
	if _, refErr := repo.Reference(plumbing.NewBranchReferenceName(branch), false); stderrs.Is(refErr, plumbing.ErrReferenceNotFound) {
		if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(branch), remoteRef.Hash())); err != nil {
			return err
		}
	} else if refErr != nil {
		return refErr
	}

	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	if err := wt.Reset(&git.ResetOptions{Commit: remoteRef.Hash(), Mode: git.HardReset}); err != nil {
		return fmt.Errorf("reset worktree of %q to %s: %w", targetDir, remoteRef.Hash(), err)
	}
	return nil
}

// ReadFileFromGitRepo 读取仓库 HEAD 提交中指定相对路径（"/" 分隔）的文件内容。
//
// 同时支持裸仓库与普通仓库：本地发布的 store 是裸仓库，没有工作区文件，封面图等
// 只能从 git 对象里读取。
func ReadFileFromGitRepo(repoPath, relPath string) ([]byte, error) {
	ensureLocalGitTransport()

	repoPath = strings.TrimSpace(repoPath)
	relPath = strings.Trim(strings.TrimSpace(relPath), "/")
	if repoPath == "" || relPath == "" {
		return nil, stderrs.New("git repository path and file path must not be empty")
	}

	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, err
	}
	head, err := repo.Head()
	if err != nil {
		return nil, err
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	file, err := tree.File(relPath)
	if err != nil {
		return nil, err
	}
	content, err := file.Contents()
	if err != nil {
		return nil, err
	}
	return []byte(content), nil
}

// FindFileInGitRepo 在仓库 HEAD 提交的文件树里按文件名查找文件，返回仓库内的相对路径
// （"/" 分隔），可直接交给 ReadFileFromGitRepo 读取。
//
// 查找口径与旧的文件系统查找保持一致：根目录优先，其次按路径顺序查找（忽略大小写）。
// 同时支持裸仓库与普通仓库：store 是裸仓库，没有工作区文件，只能从 git 对象里找。
func FindFileInGitRepo(repoPath, fileName string) (string, error) {
	ensureLocalGitTransport()

	repoPath = strings.TrimSpace(repoPath)
	fileName = strings.TrimSpace(fileName)
	if repoPath == "" || fileName == "" {
		return "", stderrs.New("git repository path and file name must not be empty")
	}

	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", err
	}
	head, err := repo.Head()
	if err != nil {
		return "", err
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return "", err
	}
	tree, err := commit.Tree()
	if err != nil {
		return "", err
	}

	// 顶层文件优先命中。
	if file, fileErr := tree.File(fileName); fileErr == nil && file != nil {
		return fileName, nil
	}

	files := tree.Files()
	defer files.Close()
	for {
		file, nextErr := files.Next()
		if stderrs.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return "", nextErr
		}
		if strings.EqualFold(path.Base(file.Name), fileName) {
			return file.Name, nil
		}
	}

	return "", fmt.Errorf("%s not found in repository %s", fileName, repoPath)
}

// FetchBareRepoFromOrigin 把裸仓库更新到 origin 的最新提交。
//
// 裸仓库没有工作区，Worktree().Pull 不可用（会报 worktree not available），因此等价实现为：
// force fetch origin（刷新 refs/remotes/origin/*）后，把 HEAD 指向的本地分支指向同名远端分支。
//
// 远端没有新提交时返回 git.NoErrAlreadyUpToDate，与 Worktree().Pull 的语义一致，
// 调用方可用 stderrs.Is(err, git.NoErrAlreadyUpToDate) 判断“已是最新”。
func FetchBareRepoFromOrigin(ctx context.Context, repoPath string) error {
	ensureLocalGitTransport()

	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return stderrs.New("git repository path is empty")
	}

	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return err
	}

	head, err := repo.Head()
	if err != nil {
		return err
	}
	branch := plumbing.Main.Short()
	if head.Name().IsBranch() {
		branch = head.Name().Short()
	}

	if fetchErr := fetchFromLocalRepo(ctx, repo, &git.FetchOptions{
		RemoteName: storeRemoteName,
		RefSpecs:   []gitconfig.RefSpec{gitconfig.RefSpec("+refs/heads/*:refs/remotes/" + storeRemoteName + "/*")},
		Force:      true,
	}); fetchErr != nil && !stderrs.Is(fetchErr, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetch %q: %w", repoPath, fetchErr)
	}

	remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName(storeRemoteName, branch), true)
	if err != nil {
		return fmt.Errorf("resolve remote branch %q of %q: %w", branch, repoPath, err)
	}
	if remoteRef.Hash() == head.Hash() {
		return git.NoErrAlreadyUpToDate
	}

	// 裸仓库没有工作区可以 reset，直接让本地分支指向远端提交（HEAD 是指向该分支的符号引用）。
	if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(branch), remoteRef.Hash())); err != nil {
		return err
	}
	return nil
}
