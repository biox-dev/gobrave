package utils

import (
	stderrs "errors"
	"os"
	"path"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// 组件目录（脚本/工作流工作区仓库）的单文件回退能力。
//
// 与 ReadGitDiff 正好相反：diff 把工作区改动展示出来，本能力把「单个文件」的未提交
// 改动丢掉，恢复成最近一次提交（本地 HEAD）的版本，供 git-diff 视图里的「撤回」按钮调用。
//
// 语义等价于 `git checkout HEAD -- <path>`（新增文件则等同删除后再从索引移除）：
//
//   - 文件在 HEAD 中存在：用 HEAD 的 blob 覆盖工作区文件，并把索引项指回 HEAD；
//   - 文件不在 HEAD 中（工作区新增）：从工作区删除，并移除索引项；
//   - 两侧都不存在：视为没有可撤回的内容，返回 ErrGitFileAbsent。
//
// 只处理单个文件，不动其它文件的改动，也不产生新的提交（撤回的是未提交改动）。
var (
	// ErrGitRepoNotInitialized 表示本地目录不是 git 仓库，无法回退。
	ErrGitRepoNotInitialized = stderrs.New("local directory is not a git repository")
	// ErrGitFileAbsent 表示该路径既不在 HEAD 也不在工作区，没有可撤回的内容。
	ErrGitFileAbsent = stderrs.New("file is not part of HEAD or the worktree")
	// ErrGitPathInvalid 表示文件路径不是仓库内的相对路径（空、绝对路径或越界）。
	ErrGitPathInvalid = stderrs.New("file path must be a relative path inside the repository")
)

// RevertWorktreeFile 把 dir（脚本/工作流工作区仓库）里的单个文件回退到本地 HEAD 版本。
//
// relPath 是以 "/" 分隔、相对仓库根目录的文件路径（与 git-diff 返回的 file.path 一致）。
// 调用方需自行把「非 git 仓库 / 路径非法 / 无可撤回内容」映射为合适的接口错误，
// 因此这里返回可判定的哨兵错误（ErrGitRepoNotInitialized / ErrGitPathInvalid / ErrGitFileAbsent）。
func RevertWorktreeFile(dir, relPath string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ErrGitRepoNotInitialized
	}
	relPath, err := cleanRepoRelPath(relPath)
	if err != nil {
		return err
	}

	repo, err := git.PlainOpen(dir)
	if err != nil {
		if stderrs.Is(err, git.ErrRepositoryNotExists) {
			return ErrGitRepoNotInitialized
		}
		return err
	}

	// HEAD 尚未出生（从未提交）时 headCommitHash 会返回 reference not found：
	// 此时工作区文件都应视为新增，但仍需要一个基线，故直接按「非仓库」处理更直观。
	headHash, err := headCommitHash(repo)
	if err != nil {
		return ErrGitRepoNotInitialized
	}
	commit, err := repo.CommitObject(headHash)
	if err != nil {
		return err
	}
	tree, err := commit.Tree()
	if err != nil {
		return err
	}

	absPath := filepath.Join(dir, filepath.FromSlash(relPath))
	file, fileErr := tree.File(relPath)
	if fileErr != nil && !stderrs.Is(fileErr, object.ErrFileNotFound) {
		return fileErr
	}

	if fileErr == nil {
		// HEAD 里有该文件：用 HEAD 内容覆盖工作区，并把索引指回 HEAD blob。
		return restoreFileFromHead(repo, file, absPath, relPath)
	}

	// HEAD 里没有：只有当工作区确实存在这个文件时才算「撤回新增」，否则无内容可撤。
	if _, statErr := os.Stat(absPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return ErrGitFileAbsent
		}
		return statErr
	}
	return removeWorktreeFile(repo, absPath, relPath)
}

// restoreFileFromHead 用 HEAD 中的 blob 覆盖工作区文件，并让索引与 HEAD 保持一致。
//
// 直接写回 HEAD 的 blob 内容，因此即使该改动此前被 `git add` 过（索引指向改动后的
// blob），随后的 wt.Add 也会把索引项改回 HEAD 的内容；未被暂存时 Add 是幂等 no-op。
func restoreFileFromHead(repo *git.Repository, file *object.File, absPath, relPath string) error {
	content, err := file.Contents()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if file.Mode == filemode.Executable {
		perm = 0o755
	}
	if err := os.WriteFile(absPath, []byte(content), perm); err != nil {
		return err
	}

	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	_, err = wt.Add(relPath)
	return err
}

// removeWorktreeFile 删除工作区里新增的文件，并移除对应的索引项（暂存过也要清掉）。
func removeWorktreeFile(repo *git.Repository, absPath, relPath string) error {
	if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
		return err
	}

	idx, err := repo.Storer.Index()
	if err != nil {
		return err
	}
	if _, err := idx.Remove(relPath); err != nil {
		// 未暂存的新增文件本来就没有索引项，属于幂等成功。
		if stderrs.Is(err, index.ErrEntryNotFound) {
			return nil
		}
		return err
	}
	return repo.Storer.SetIndex(idx)
}

// cleanRepoRelPath 校验并规整以 "/" 分隔的仓库内相对路径，拒绝空值、绝对路径与越界路径。
func cleanRepoRelPath(relPath string) (string, error) {
	raw := strings.TrimSpace(relPath)
	if raw == "" || strings.HasPrefix(raw, "/") {
		return "", ErrGitPathInvalid
	}
	clean := path.Clean(raw)
	if clean == "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", ErrGitPathInvalid
	}
	if clean == ".git" || strings.HasPrefix(clean, ".git/") {
		return "", ErrGitPathInvalid
	}
	return clean, nil
}
