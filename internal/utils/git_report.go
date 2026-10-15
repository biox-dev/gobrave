package utils

import (
	stderrs "errors"
	"os"
	"path/filepath"
	"strings"

	git "github.com/go-git/go-git/v5"
)

// 报告工作目录（<baseDir>/data/<projectId>/report/<reportId>）的 git 能力。
//
// 报告目录是「报告自身文件」output.md 的落盘位置，结构与组件目录（脚本/工作流）
// 完全一致，因此复用同一套 internal/utils 里的 git 原语：
//
//   - 没有 store 侧，同步状态只有「本地是否初始化 + 是否有未提交改动」；
//   - diff / 提交 / 单文件撤回分别复用 ReadGitDiff、CommitAll、RevertWorktreeFile。
//
// 与组件目录的差异只有一处：报告目录的仓库是按需（首次写入）惰性初始化的，
// 见 EnsureReportWorktreeGitRepo。

// ErrReportDirEmpty 表示报告工作目录为空，无法定位 git 仓库。
var ErrReportDirEmpty = stderrs.New("report working directory is empty")

// EnsureReportWorktreeGitRepo 保证 dir（报告工作目录）是一个带工作区的 git 仓库。
//
//   - 目录不存在：创建后初始化（默认分支 main）；
//   - 目录存在但不是仓库（首次保存报告文件留下的普通文件目录）：就地初始化，
//     已有文件留作未跟踪改动，等待用户显式提交；
//   - 目录已是仓库：直接打开复用。
//
// 返回的 repo 与 initialized 组合表达两种"新建"情况：
//   - 目录第一次成为仓库时 initialized=true（调用方需据此决定是否自动补一次提交）；
//   - 目录本来就是仓库时 initialized=false。
func EnsureReportWorktreeGitRepo(dir string) (*git.Repository, bool, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, false, ErrReportDirEmpty
	}

	if repo, err := git.PlainOpen(dir); err == nil {
		return repo, false, nil
	} else if !stderrs.Is(err, git.ErrRepositoryNotExists) {
		return nil, false, err
	}

	// EnsureGitRepo 会先 MkdirAll，因此「目录尚不存在」与「目录存在但非仓库」走同一路径。
	repo, err := EnsureGitRepo(dir)
	if err != nil {
		return nil, false, err
	}
	return repo, true, nil
}

// InitReportWorktreeGitRepo 惰性初始化报告工作目录的 git 仓库：目录已存在仓库时是 no-op。
//
// 供 GetProjectReportFile / UpdateProjectReportFile 在「报告文件被创建或更新」时调用，
// 保证报告目录从第一次落盘起就纳入版本管理（前端随即可以查看变化 / 提交 / 撤回）。
// 初始化失败不阻断文件读写，由调用方决定是否忽略（best-effort）。
func InitReportWorktreeGitRepo(dir string) error {
	_, _, err := EnsureReportWorktreeGitRepo(dir)
	return err
}

// ReportWorktreeGitState 是报告工作目录的 git 状态（供前端渲染状态标签）。
//
// 报告没有「发布目标」，因此不含 store 相关的字段；语义与 GitSyncState 的本地部分一致。
type ReportWorktreeGitState struct {
	Dir         string `json:"dir,omitempty"`
	Commit      string `json:"commit,omitempty"`
	Initialized bool   `json:"initialized"`
	Dirty       bool   `json:"dirty"`
}

// ReadReportWorktreeGitState 读取报告工作目录的 git 状态；目录不存在或不是仓库时返回零值。
//
// Initialized 以「是否有可用提交（HEAD 已出生）」为准，而非「目录下是否已有 .git」：
// 报告目录是惰性初始化的，而空目录初始化后不会产生提交（CommitAll 无变更时是 no-op），
// 此时没有任何可对比的基线，查看变化 / 撤回都无从谈起，对外按「未初始化」表达更直观；
// 首次写入报告文件后的基线提交会把它翻成 true。
func ReadReportWorktreeGitState(dir string) ReportWorktreeGitState {
	dir = strings.TrimSpace(dir)
	state := ReportWorktreeGitState{Dir: dir}

	commit, dirty, ok := readWorktreeHead(dir)
	state.Commit = commit
	state.Dirty = dirty
	state.Initialized = ok && commit != ""
	return state
}

// reportWorktreeFilePath 返回报告工作目录下某个文件的绝对路径（仅用于存在性判断）。
func reportWorktreeFilePath(dir, filename string) string {
	return filepath.Join(dir, filename)
}

// ReportWorktreeHasFile 判断报告工作目录下是否存在指定文件。
func ReportWorktreeHasFile(dir, filename string) bool {
	dir = strings.TrimSpace(dir)
	filename = strings.TrimSpace(filename)
	if dir == "" || filename == "" {
		return false
	}
	_, err := os.Stat(reportWorktreeFilePath(dir, filename))
	return err == nil
}
