package utils

import (
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// GitSyncState 描述「本地工作目录仓库」与「发布目标仓库(store 裸仓库)」之间的同步状态。
//
// 设计取舍：状态完全由磁盘上的 git 元数据通过 go-git 实时推导，不落库。
// 原因：
//  1. “本地有更新”天然包含未提交的工作区改动（脚本文件被直接编辑），
//     任何 DB 字段都无法表达；
//  2. SaveScript / PublishScript / InstallScript 三个写入口都要同步维护 commit_id，
//     而 EnsureBareGitRepo 会重建非裸目录、InstallScript 会 reset --hard，
//     落库字段极易与真实仓库状态漂移；
//  3. 本地目录是小仓库，Worktree.Status 与 HEAD 读取成本很低，无需缓存。
type GitSyncState struct {
	LocalDir string `json:"local_dir,omitempty"`
	StoreDir string `json:"store_dir,omitempty"`

	// LocalInitialized 本地目录是否已是一个 git 仓库（脚本/工作流的版本目录）。
	LocalInitialized bool `json:"local_initialized"`
	// StoreInitialized 发布目标是否已是一个 git 仓库（即是否已发布过）。
	StoreInitialized bool `json:"store_initialized"`

	// LocalCommit 本地工作区 HEAD 的提交 hash（尚无提交时为空）。
	LocalCommit string `json:"local_commit,omitempty"`
	// StoreCommit 发布目标 HEAD 的提交 hash（未发布时为空）。
	StoreCommit string `json:"store_commit,omitempty"`

	// LocalDirty 本地工作区存在未提交改动（新增/修改/删除，含未跟踪文件）。
	LocalDirty bool `json:"local_dirty"`
	// LocalAhead 本地 HEAD 领先 store：存在已提交但未发布的改动。
	LocalAhead bool `json:"local_ahead"`
	// StoreAhead store 领先本地：远端有本地没有的提交（需要 install/同步）。
	StoreAhead bool `json:"store_ahead"`

	// HasLocalChanges 本地有未发布改动 = 未提交改动 或 本地领先 store。
	HasLocalChanges bool `json:"has_local_changes"`
	// HasStoreChanges store 有本地未同步的提交（远端有更新）。
	HasStoreChanges bool `json:"has_store_changes"`
	// InSync 本地干净且两侧 commit 一致。
	InSync bool `json:"in_sync"`

	// Remotes 是 store 裸仓库上配置的远程仓库列表（github / gitee / origin ...），
	// 从磁盘 git 配置实时读取、不落库：发布到远程（PublishStoreRemote）只把地址写成
	// store 仓库的 remote，不经数据库，因此这里就是「已配置的远程仓库」的唯一数据源。
	// 没有 store 仓库或未配置任何 remote 时为空。
	Remotes []GitRemote `json:"remotes,omitempty"`
}

// ReadGitSyncState 读取 localDir（脚本/工作流目录）与 storeDir（发布裸仓库）的同步状态。
//
// 两个目录都可以不存在或不是 git 仓库：此时返回零值语义的状态（不报错），
// 便于接口在“从未保存/从未发布”的情况下也能安全返回。
func ReadGitSyncState(localDir, storeDir string) GitSyncState {
	// 如果 storeDir 不存在，直接返回
	state := GitSyncState{
		LocalDir: strings.TrimSpace(localDir),
		StoreDir: strings.TrimSpace(storeDir),
	}

	localCommit, localDirty, localOK := readWorktreeHead(state.LocalDir)
	state.LocalCommit = localCommit
	state.LocalDirty = localDirty
	state.LocalInitialized = localOK

	storeCommit, storeOK := readRepoHead(state.StoreDir)
	state.StoreCommit = storeCommit
	state.StoreInitialized = storeOK
	// remote 列表同样从 store 仓库实时读取：本地发布时通常只有 origin（本地路径）之外
	// 没有任何 remote，配置过发布目标后才会出现 github / gitee 等条目。
	state.Remotes = ReadGitRemotes(state.StoreDir)

	switch {
	case localCommit == "" && storeCommit == "":
		// 两侧都没有提交：无任何同步动作可做。
	case localCommit == "":
		// 本地还没有提交，但 store 已有内容。
		state.StoreAhead = true
	case storeCommit == "":
		// store 还没有内容（从未发布），本地已有提交。
		state.LocalAhead = true
	case localCommit == storeCommit:
		// 提交一致，是否“有更新”只看未提交的工作区改动。
	default:
		// 两侧 commit 不同，用祖先关系判断方向：
		// store 的 HEAD 是本地的祖先 => 本地领先；反之 => store 领先；
		// 找不到共同祖先（历史被重建）=> 视为分叉，两边都标记。
		switch {
		case commitIsAncestor(state.LocalDir, storeCommit, localCommit):
			state.LocalAhead = true
		case commitIsAncestor(state.StoreDir, localCommit, storeCommit):
			state.StoreAhead = true
		default:
			state.LocalAhead = true
			state.StoreAhead = true
		}
	}

	state.HasLocalChanges = state.LocalDirty || state.LocalAhead
	state.HasStoreChanges = state.StoreAhead
	// 两侧仓库都存在且都没有待同步内容才算“同步”；目录尚未初始化时不算。
	state.InSync = state.LocalInitialized && state.StoreInitialized && !state.HasLocalChanges && !state.HasStoreChanges
	return state
}

// readWorktreeHead 返回非裸仓库的 HEAD hash、工作区是否有改动、以及目录是否为可用仓库。
// 仓库不存在或不是仓库时返回 ("", false, false)。
func readWorktreeHead(dir string) (commit string, dirty bool, ok bool) {
	if dir == "" {
		return "", false, false
	}
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return "", false, false
	}

	if head, err := repo.Head(); err == nil {
		commit = head.Hash().String()
	}
	// HEAD 未出生（尚无提交）时 Status 会把全部文件视为未跟踪，语义上正是“有未发布改动”。
	if wt, err := repo.Worktree(); err == nil {
		if status, err := wt.Status(); err == nil {
			dirty = !status.IsClean()
		}
	}
	return commit, dirty, true
}

// readRepoHead 返回仓库 HEAD hash 以及目录是否为可用仓库（裸/非裸均可）。
//
// HEAD 悬挂（历史遗留的 store：HEAD 指向初始化时的 main，内容却推在别的分支上）时
// 回退到实际分支，与读侧（ReadFileFromGitRepo / FindFileInGitRepo）保持一致，
// 避免同步状态误报成“从未发布”。
func readRepoHead(dir string) (string, bool) {
	if dir == "" {
		return "", false
	}
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return "", false
	}
	hash, err := headCommitHash(repo)
	if err != nil {
		return "", true
	}
	return hash.String(), true
}

// commitIsAncestor 判断 ancestorHash 是否为 descendantHash 的祖先提交。
//
// 两个提交必须都存在于 repoPath 仓库中（对象不存在或仓库历史被重建时返回 false，
// 由调用方兜底成“分叉”）；两者相等时返回 false。
func commitIsAncestor(repoPath, ancestorHash, descendantHash string) bool {
	if repoPath == "" || ancestorHash == "" || descendantHash == "" {
		return false
	}
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return false
	}
	ancestor, err := repo.CommitObject(plumbing.NewHash(ancestorHash))
	if err != nil {
		return false
	}
	descendant, err := repo.CommitObject(plumbing.NewHash(descendantHash))
	if err != nil {
		return false
	}
	if ancestor.Hash == descendant.Hash {
		return false
	}
	isAncestor, err := ancestor.IsAncestor(descendant)
	if err != nil {
		return false
	}
	return isAncestor
}
