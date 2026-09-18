package utils

import (
	stderrs "errors"
	"os"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// defaultGitCommitMessage 是调用方未提供 commit message 时的兜底文案。
const defaultGitCommitMessage = "update repository"

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
