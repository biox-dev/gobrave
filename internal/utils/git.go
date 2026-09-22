package utils

import (
	"context"
	stderrs "errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"sort"
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

const (
	// defaultBranchName 是新建仓库的默认分支名，也是分支兜底顺序里的首选。
	defaultBranchName = "main"
	// branchRefPrefix 是本地分支引用的前缀（按它枚举仓库里实际存在的分支）。
	branchRefPrefix = "refs/heads/"
)

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
// 返回值 pushed 表示目标仓库这次是否真的被更新：
//   - true：远端分支前移了；
//   - false：远端分支已经是同一个提交（go-git 返回 git.NoErrAlreadyUpToDate），
//     「没有新内容可推送」属于幂等成功，调用方应据此给用户提示而不是报错
//     （例如重复 publish 同一个未再修改的脚本/工作流）。
//
// 发布后会把目标裸仓库（store）的 HEAD 切到本次发布的分支：store 的分支跟随工作目录
// （publish(dev) => store 变成 dev），不再固定为初始化时的 main。这样 install / 读导出
// 文件 / 检查更新都只需要认 store 的 HEAD，不必依赖源目录或 store 的历史分支名；
// 之前没有这一步时，从非 main 分支发布会让 store 的 HEAD 悬空（reference not found）
// 或读到旧分支上的旧内容。
//
// 源仓库没有 commit（HEAD 未出生）时返回错误，调用方应提示先保存。
func PushDirToRepo(ctx context.Context, sourceDir, targetRepoPath string) (bool, error) {
	ensureLocalGitTransport()

	sourceDir = strings.TrimSpace(sourceDir)
	targetRepoPath = strings.TrimSpace(targetRepoPath)
	if sourceDir == "" || targetRepoPath == "" {
		return false, stderrs.New("git source directory and target repository path must not be empty")
	}

	repo, err := git.PlainOpen(sourceDir)
	if err != nil {
		return false, fmt.Errorf("open source git repository %q: %w", sourceDir, err)
	}

	head, err := repo.Head()
	if err != nil {
		if stderrs.Is(err, plumbing.ErrReferenceNotFound) {
			return false, fmt.Errorf("source git repository %q has no commit yet", sourceDir)
		}
		return false, err
	}

	// remote 已存在时先删除再加回，等价于 git remote set-url，避免残留旧地址。
	if delErr := repo.DeleteRemote(storeRemoteName); delErr != nil && !stderrs.Is(delErr, git.ErrRemoteNotFound) {
		return false, delErr
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{
		Name: storeRemoteName,
		URLs: []string{targetRepoPath},
	}); err != nil {
		return false, err
	}

	// "+" 前缀表示允许非快进更新：store 是脚本目录的发布镜像，需要与源分支完全一致。
	// 源分支名原样写入 store（store 的分支跟随工作目录），随后再把 store 的 HEAD 切过去。
	branch := head.Name().Short()
	refSpec := gitconfig.RefSpec(fmt.Sprintf("+%s:%s", head.Name(), head.Name()))
	upToDate := false
	if err := repo.PushContext(ctx, &git.PushOptions{
		RemoteName: storeRemoteName,
		RefSpecs:   []gitconfig.RefSpec{refSpec},
	}); err != nil {
		// 远端已是同一个提交时 go-git 返回 NoErrAlreadyUpToDate：这不是失败，
		// 只是没有新内容可推送，转成 (false, nil) 让调用方回复提示信息。
		if !stderrs.Is(err, git.NoErrAlreadyUpToDate) {
			return false, fmt.Errorf("push %q to %q: %w", sourceDir, targetRepoPath, err)
		}
		upToDate = true
	}

	// 无论这次是否真的推了新提交，都要对齐目标裸仓库的 HEAD：
	// 历史遗留的 store（HEAD 指向初始化时的 main，内容却推在别的分支上）会在这里自愈；
	// 缺少这一步就会出现「push 成功但读侧 reference not found / 读到旧分支内容」。
	if err := alignBareRepoHead(targetRepoPath, branch); err != nil {
		return false, err
	}
	return !upToDate, nil
}

// SyncWorktreeFromRepo 让 targetDir 的工作区与 srcRepoPath 仓库保持一致：
//
//   - targetDir 已是 git 仓库：从 srcRepoPath fetch 后把 HEAD 切到源仓库的分支再 reset --hard
//   - targetDir 不存在或为空目录：从 srcRepoPath clone（非裸克隆，带工作区）
//   - targetDir 存在已有内容但不是 git 仓库（旧版本安装留下的普通文件目录）：
//     就地初始化仓库后再 reset --hard，语义同"覆盖本地"
//
// 三种情况都以源仓库（install 场景下是 store 裸仓库）的分支为准：执行完 targetDir 的
// 当前分支与源仓库一致（见 resolveRepoBranch / resetWorktreeToRemote），目标目录原有的
// 其它分支保留不删，只是不再被 HEAD 指向。
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
		// 显式指定要检出的分支：目标分支跟随源仓库（store）的分支。
		// clone 默认按源仓库 HEAD 解析，而历史遗留的 store 可能存在 HEAD 悬挂，
		// 这种情况不指定分支会直接 reference not found。
		cloneOpts := &git.CloneOptions{URL: srcRepoPath}
		if branch, branchErr := sourceRepoBranch(srcRepoPath); branchErr == nil {
			cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(branch)
		}
		if _, err := git.PlainCloneContext(ctx, targetDir, false, cloneOpts); err != nil {
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

// referenceBranchNames 返回仓库中指定前缀下真实存在的分支名（已排序）。
//
// prefix 形如 "refs/heads/" 或 "refs/remotes/origin/"；只统计哈希引用，
// 符号引用（HEAD、refs/remotes/<remote>/HEAD）不计入。
func referenceBranchNames(repo *git.Repository, prefix string) []string {
	if repo == nil {
		return nil
	}
	iter, err := repo.References()
	if err != nil {
		return nil
	}

	var names []string
	iterErr := iter.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() != plumbing.HashReference {
			return nil
		}
		name := ref.Name().String()
		if !strings.HasPrefix(name, prefix) {
			return nil
		}
		short := strings.TrimPrefix(name, prefix)
		if short == "" || short == "HEAD" {
			return nil
		}
		names = append(names, short)
		return nil
	})
	iter.Close()
	if iterErr != nil {
		return nil
	}

	sort.Strings(names)
	return names
}

// resolveRepoBranch 返回仓库「当前使用的分支名」，用于跨仓库同步时对齐两侧分支：
//
//  1. HEAD 指向且真实存在的分支（正常情况）；
//  2. 否则（HEAD 悬挂 / 未出生 / 游离）取仓库实际存在的分支：优先 main、其次 master，
//     再退化为名称排序第一的分支；
//  3. 仓库里一个分支都没有时返回 main（全新仓库的初始化分支）。
//
// 不能直接用 repo.Head()：go-git 在 HEAD 指向不存在的分支时报 reference not found，
// 而历史遗留的 store 正是这种状态（HEAD 指向初始化时的 main，内容却推在其它分支上）。
// 这里用确定性的兜底顺序，保证同一仓库多次调用得到同一个分支名。
func resolveRepoBranch(repo *git.Repository) string {
	if repo == nil {
		return defaultBranchName
	}
	if head, err := repo.Head(); err == nil && head.Name().IsBranch() {
		return head.Name().Short()
	}

	branches := referenceBranchNames(repo, branchRefPrefix)
	if len(branches) == 0 {
		return defaultBranchName
	}
	for _, candidate := range []string{defaultBranchName, plumbing.Master.Short()} {
		for _, name := range branches {
			if name == candidate {
				return name
			}
		}
	}
	return branches[0]
}

// headCommitHash 返回仓库 HEAD 对应的提交 hash。
//
// HEAD 正常时等同于 repo.Head().Hash()；HEAD 悬挂时回退到 resolveRepoBranch 选出的分支，
// 让「读导出文件 / 回填元数据 / 读同步状态」在历史遗留的 store 上仍然可用，
// 而不是直接抛 reference not found。
func headCommitHash(repo *git.Repository) (plumbing.Hash, error) {
	if repo == nil {
		return plumbing.ZeroHash, stderrs.New("git repository is nil")
	}
	if head, err := repo.Head(); err == nil {
		return head.Hash(), nil
	}
	ref, err := repo.Reference(plumbing.NewBranchReferenceName(resolveRepoBranch(repo)), true)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return ref.Hash(), nil
}

// alignRepoHead 把仓库的 HEAD 切到指定分支（幂等：已经在该分支上时不写任何 ref）。
//
// go-git 没有 checkout：裸仓库只要改 HEAD 这个符号引用；带工作区的仓库由调用方随后
// reset --hard 对齐工作区。
func alignRepoHead(repo *git.Repository, branch string) error {
	if repo == nil {
		return stderrs.New("git repository is nil")
	}
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return stderrs.New("branch name is empty")
	}

	target := plumbing.NewBranchReferenceName(branch)
	if head, err := repo.Reference(plumbing.HEAD, false); err == nil && head.Type() == plumbing.SymbolicReference && head.Target() == target {
		return nil
	}
	return repo.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, target))
}

// alignBareRepoHead 把裸仓库（store）的 HEAD 切到指定分支。
//
// 只处理裸仓库：非裸目录带工作区，直接改 HEAD 会让工作区与分支脱节（历史遗留用法，
// 例如把普通仓库目录当作 store 使用），这种情况保持原样不动。
func alignBareRepoHead(repoPath, branch string) error {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return fmt.Errorf("open target git repository %q: %w", repoPath, err)
	}
	cfg, err := repo.Config()
	if err != nil {
		return fmt.Errorf("read target git repository config %q: %w", repoPath, err)
	}
	if !cfg.Core.IsBare {
		return nil
	}
	return alignRepoHead(repo, branch)
}

// sourceRepoBranch 打开仓库并返回它「当前使用的分支名」。
//
// install 时目标目录的分支跟随 store，就是靠这个取值（见 resetWorktreeToRemote）。
func sourceRepoBranch(repoPath string) (string, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return "", err
	}
	return resolveRepoBranch(repo), nil
}

// pickRemoteBranch 在指定 remote 的远端分支里挑选本次要拉取的分支。
//
// 首选与本地同名（want）的分支；远端没有同名分支时，若远端只有一个分支就直接跟随它
// （覆盖「本地 store 分支被 publish 改成 dev，远端默认分支仍是 main」这种情况），
// 否则优先 main / master，都不匹配时返回带可用分支列表的错误。
func pickRemoteBranch(repo *git.Repository, remoteName, want string) (string, error) {
	names := referenceBranchNames(repo, "refs/remotes/"+remoteName+"/")
	if len(names) == 0 {
		return "", fmt.Errorf("remote %q has no branch to fetch", remoteName)
	}
	for _, name := range names {
		if name == want {
			return name, nil
		}
	}
	if len(names) == 1 {
		return names[0], nil
	}
	for _, candidate := range []string{defaultBranchName, plumbing.Master.Short()} {
		for _, name := range names {
			if name == candidate {
				return name, nil
			}
		}
	}
	return "", fmt.Errorf("remote %q has no branch %q (available: %s)", remoteName, want, strings.Join(names, ", "))
}

// resetWorktreeToRemote 让已存在的仓库工作区与远端仓库 srcRepoPath 完全一致：
// 重置 origin 地址 -> fetch -> 把 HEAD 切到源仓库的分支 -> reset --hard。
//
// 分支一律跟随源仓库（install 场景下是 store 裸仓库，见 resolveRepoBranch）：
// reset --hard 只移动当前分支的提交指针、不会改分支名，所以必须显式切换 HEAD，
// 否则目标目录会停留在旧分支名上，下次 publish 又按旧名字推回 store。
// 目标目录原有的其它分支保留不删。
func resetWorktreeToRemote(ctx context.Context, repo *git.Repository, targetDir, srcRepoPath string) error {
	branch, err := sourceRepoBranch(srcRepoPath)
	if err != nil {
		return fmt.Errorf("open source git repository %q: %w", srcRepoPath, err)
	}

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

	if err := fetchFromLocalRepo(ctx, repo, &git.FetchOptions{
		RemoteName: storeRemoteName,
		RefSpecs:   []gitconfig.RefSpec{gitconfig.RefSpec("+refs/heads/*:refs/remotes/" + storeRemoteName + "/*")},
		Force:      true,
	}); err != nil && !stderrs.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetch %q: %w", srcRepoPath, err)
	}

	remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName(storeRemoteName, branch), true)
	if err != nil {
		return fmt.Errorf("resolve remote branch %q of %q (available: %s): %w",
			branch, srcRepoPath, strings.Join(referenceBranchNames(repo, "refs/remotes/"+storeRemoteName+"/"), ", "), err)
	}

	// 就地初始化出来的空仓库、或本地还没有 store 那个分支时，先建出同名分支，
	// 否则切 HEAD / Reset 都无法定位该分支（go-git 会报 reference not found）。
	if _, refErr := repo.Reference(plumbing.NewBranchReferenceName(branch), false); stderrs.Is(refErr, plumbing.ErrReferenceNotFound) {
		if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(branch), remoteRef.Hash())); err != nil {
			return err
		}
	} else if refErr != nil {
		return refErr
	}

	// 切到源仓库的分支后再 reset --hard：工作区内容与当前分支名都对齐到 store。
	if err := alignRepoHead(repo, branch); err != nil {
		return err
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
// 只能从 git 对象里读取。HEAD 悬挂（历史遗留的 store）时按 resolveRepoBranch 回退到
// 实际分支，不直接报 reference not found。
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
	hash, err := headCommitHash(repo)
	if err != nil {
		return nil, err
	}
	commit, err := repo.CommitObject(hash)
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
// HEAD 悬挂（历史遗留的 store）时按 resolveRepoBranch 回退到实际分支。
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
	hash, err := headCommitHash(repo)
	if err != nil {
		return "", err
	}
	commit, err := repo.CommitObject(hash)
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

// FetchBareRepoFromRemote 把裸仓库更新到指定 remote 的最新提交。
//
// 裸仓库没有工作区，Worktree().Pull 不可用（会报 worktree not available），因此等价实现为：
// force fetch <remoteName>（刷新 refs/remotes/<remoteName>/*）后，把本地分支指向远端分支，
// 并把 HEAD 切到该分支。remoteName 为空时回退 storeRemoteName（origin）。
//
// 分支选取：优先本地 store 当前的分支（resolveRepoBranch，HEAD 悬挂也能容错）；
// 远端没有同名分支时跟随远端实际存在的分支（只有一个分支，或 main / master），
// 并把 store 的 HEAD 一起切过去，保证后续「publish 跟随本地分支、install 跟随 store 分支」
// 这条链路始终自洽。
//
// store 裸仓库上可以配置多个远端（origin / github / gitee ...，见 ReadGitRemotes），
// 「检查更新」（/store/redownload）因此可以指定从哪个 remote 拉取。
//
// 远端没有新提交时返回 git.NoErrAlreadyUpToDate，与 Worktree().Pull 的语义一致，
// 调用方可用 stderrs.Is(err, git.NoErrAlreadyUpToDate) 判断“已是最新”。
func FetchBareRepoFromRemote(ctx context.Context, repoPath, remoteName string) error {
	ensureLocalGitTransport()

	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return stderrs.New("git repository path is empty")
	}
	remoteName = strings.TrimSpace(remoteName)
	if remoteName == "" {
		remoteName = storeRemoteName
	}

	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return err
	}

	branch := resolveRepoBranch(repo)
	// 还没有任何提交的 store（headErr != nil）也允许 fetch 建分支，只是无法判断“已是最新”。
	headHash, headErr := headCommitHash(repo)

	if fetchErr := fetchFromLocalRepo(ctx, repo, &git.FetchOptions{
		RemoteName: remoteName,
		RefSpecs:   []gitconfig.RefSpec{gitconfig.RefSpec("+refs/heads/*:refs/remotes/" + remoteName + "/*")},
		Force:      true,
	}); fetchErr != nil && !stderrs.Is(fetchErr, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("fetch %q: %w", repoPath, fetchErr)
	}

	remoteRef, err := repo.Reference(plumbing.NewRemoteReferenceName(remoteName, branch), true)
	if err != nil {
		// 远端没有与 store 同名的分支（例如 store 分支被 publish 改成 dev，
		// 而远端默认分支仍是 main）：挑一个远端实际存在的分支跟随。
		if branch, err = pickRemoteBranch(repo, remoteName, branch); err != nil {
			return fmt.Errorf("resolve remote branch of %q on remote %q: %w", repoPath, remoteName, err)
		}
		if remoteRef, err = repo.Reference(plumbing.NewRemoteReferenceName(remoteName, branch), true); err != nil {
			return fmt.Errorf("resolve remote branch %q of %q: %w", branch, repoPath, err)
		}
	}
	if headErr == nil && remoteRef.Hash() == headHash {
		return git.NoErrAlreadyUpToDate
	}

	// 裸仓库没有工作区可以 reset，直接让本地分支指向远端提交，并把 HEAD 切到该分支。
	if err := repo.Storer.SetReference(plumbing.NewHashReference(plumbing.NewBranchReferenceName(branch), remoteRef.Hash())); err != nil {
		return err
	}
	return alignRepoHead(repo, branch)
}

// GitRemote 是仓库中配置的一个 git remote：名称 + 全部地址。
//
// 同一 remote 可以配置多个 URL（git 的 remote.<name>.url 可重复），因此这里用切片表达。
// store 表不再保存「目标远程地址」列，发布到远程时写入的信息全部保存在 store 裸仓库的
// remote 配置里，读取侧（git_state.remotes）也从这里实时推导，不落库。
type GitRemote struct {
	Name string   `json:"name"`
	URLs []string `json:"urls"`
}

// ReadGitRemotes 读取仓库配置里的 remote 列表（按名称排序，输出稳定）。
//
// 目录不存在、不是仓库或没有 remote 时返回 nil（不报错），
// 便于接口在「从未发布 / 从未配置远程」的情况下安全返回。
func ReadGitRemotes(repoPath string) []GitRemote {
	repoPath = strings.TrimSpace(repoPath)
	if repoPath == "" {
		return nil
	}
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil
	}
	remotes, err := repo.Remotes()
	if err != nil {
		return nil
	}

	result := make([]GitRemote, 0, len(remotes))
	for _, remote := range remotes {
		if remote == nil {
			continue
		}
		cfg := remote.Config()
		if cfg == nil {
			continue
		}
		urls := make([]string, 0, len(cfg.URLs))
		for _, raw := range cfg.URLs {
			if trimmed := strings.TrimSpace(raw); trimmed != "" {
				urls = append(urls, trimmed)
			}
		}
		if len(urls) == 0 {
			continue
		}
		result = append(result, GitRemote{Name: cfg.Name, URLs: urls})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}

// EnsureGitRemote 保证 repoPath 仓库里存在指向 remoteURL 的 remote。
//
//   - remoteURL 已经配置在某个 remote 上：直接复用该 remote，added=false（跳过添加）；
//   - 否则按地址主机名推导 remote 名（github.com → github、gitee.com → gitee，
//     其余取主机名首段，取不到时用 "remote"），同名被其它地址占用时追加 "-2"、"-3"…
//     保证名称唯一，added=true。
//
// 只写仓库配置、不做任何网络操作；真正的 push 由调用方后续实现。
// store 是裸仓库，配置文件就是仓库本身，因此这里对裸仓库同样适用。
func EnsureGitRemote(repoPath, remoteURL string) (remote GitRemote, added bool, err error) {
	repoPath = strings.TrimSpace(repoPath)
	remoteURL = strings.TrimSpace(remoteURL)
	if repoPath == "" {
		return GitRemote{}, false, stderrs.New("git repository path is empty")
	}
	if remoteURL == "" {
		return GitRemote{}, false, stderrs.New("git remote url is empty")
	}

	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return GitRemote{}, false, err
	}

	existing := ReadGitRemotes(repoPath)
	for _, item := range existing {
		for _, configured := range item.URLs {
			if configured == remoteURL {
				return item, false, nil
			}
		}
	}

	name := uniqueGitRemoteName(gitRemoteNameFromURL(remoteURL), existing)
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{
		Name: name,
		URLs: []string{remoteURL},
	}); err != nil {
		return GitRemote{}, false, err
	}
	return GitRemote{Name: name, URLs: []string{remoteURL}}, true, nil
}

// gitRemoteNameFromURL 把 git 地址映射成一个可读的 remote 名：
// github.com → github、gitee.com → gitee、gitlab.example.com → gitlab，
// 解析不出主机名时回退 "remote"。
func gitRemoteNameFromURL(rawURL string) string {
	host := gitRemoteHost(rawURL)
	if host == "" {
		return "remote"
	}
	name := host
	if idx := strings.Index(name, "."); idx > 0 {
		name = name[:idx]
	}
	return name
}

// gitRemoteHost 从 ssh（git@host:owner/repo.git、ssh://git@host/owner/repo.git）
// 或 http(s) 地址里取出主机名，取不到时返回空串。
func gitRemoteHost(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "git@") {
		rest := strings.TrimPrefix(rawURL, "git@")
		if idx := strings.Index(rest, ":"); idx > 0 {
			return rest[:idx]
		}
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.Hostname()
}

// uniqueGitRemoteName 保证 remote 名合法且不与已有 remote 冲突：
// 非法字符替换为 "-"，名称被占用时追加 "-2"、"-3"…
func uniqueGitRemoteName(name string, existing []GitRemote) string {
	name = sanitizeGitRemoteName(name)
	if name == "" {
		name = "remote"
	}

	used := make(map[string]bool, len(existing))
	for _, item := range existing {
		used[item.Name] = true
	}
	if !used[name] {
		return name
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if !used[candidate] {
			return candidate
		}
	}
}

// sanitizeGitRemoteName 只保留 git remote 名允许的字符（字母/数字/.-_），
// 其余（含空格、斜杠）统一替换为 "-"。
func sanitizeGitRemoteName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}
