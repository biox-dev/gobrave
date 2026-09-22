package utils

import (
	"bytes"
	stderrs "errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/pmezard/go-difflib/difflib"
)

// 组件目录（脚本/工作流工作区仓库）的只读 diff 能力。
//
// 与 ReadGitSyncState 一样，diff 完全由磁盘上的 git 元数据与工作区文件实时推导，不落库：
//
//   - Worktree：工作区相对本地 HEAD 的未提交改动（等价 `git diff HEAD`，含未跟踪文件）；
//   - Unpublished：本地 HEAD 相对 store HEAD 的提交差异（已提交但未发布的内容）。
//
// 二者合起来覆盖「本地有变化」的两种形态（未提交 / 未发布），供前端在 git_state
// 显示「本地有变化」时查看具体改动内容。两个目录都可以不存在或不是 git 仓库，
// 此时返回 Initialized=false 的空结果（不报错）。
const (
	// gitDiffContextLines unified diff 的上下文行数（与 git 默认一致）。
	gitDiffContextLines = 3
	// gitDiffMaxFileBytes 参与文本 diff 的单文件大小上限；超过只标记变化、不产出内容。
	gitDiffMaxFileBytes = 1 << 20
	// gitDiffMaxPatchBytes patch 文本总长上限，超出后截断并置 Truncated。
	gitDiffMaxPatchBytes = 1 << 20
	// gitDiffMaxFiles 单个 section 最多返回的文件数，超出后截断并置 Truncated。
	gitDiffMaxFiles = 200
)

// git diff 的文件状态取值（与 `git diff --name-status` 的字母含义一致，小写形式对外）。
const (
	gitDiffStatusAdded    = "added"
	gitDiffStatusModified = "modified"
	gitDiffStatusDeleted  = "deleted"
)

// GitDiffFile 描述一个文件在工作区/两个提交之间的变化。
//
// Diff 是该文件的 unified diff 文本（含 ---/+++ 头，不含 `diff --git` 行）；
// 二进制或超大文件内容不参与 diff，只通过 Binary/Omitted 标记。
type GitDiffFile struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Binary    bool   `json:"binary,omitempty"`
	Omitted   bool   `json:"omitted,omitempty"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Diff      string `json:"diff,omitempty"`
}

// GitDiffSection 是一组「旧快照 -> 新快照」的 diff 结果。
//
// BaseCommit/TargetCommit 为空表示该侧没有提交（HEAD 尚未出生），
// 此时快照按空内容处理（工作区文件全部表现为 added）。
type GitDiffSection struct {
	BaseCommit   string        `json:"base_commit,omitempty"`
	TargetCommit string        `json:"target_commit,omitempty"`
	HasChanges   bool          `json:"has_changes"`
	FileCount    int           `json:"file_count"`
	Additions    int           `json:"additions"`
	Deletions    int           `json:"deletions"`
	Truncated    bool          `json:"truncated"`
	Files        []GitDiffFile `json:"files"`
	Patch        string        `json:"patch"`
}

// GitDiffResult 是组件目录的本地变化汇总。
//
// HasChanges = Worktree.HasChanges || Unpublished.HasChanges，即「本地有变化」的两种形态。
type GitDiffResult struct {
	RepoDir     string          `json:"repo_dir"`
	Initialized bool            `json:"initialized"`
	HeadCommit  string          `json:"head_commit,omitempty"`
	HasChanges  bool            `json:"has_changes"`
	Worktree    GitDiffSection  `json:"worktree"`
	Unpublished *GitDiffSection `json:"unpublished,omitempty"`
}

// gitDiffBlob 是参与 diff 的一份文件内容。
type gitDiffBlob struct {
	content []byte
	size    int64
	binary  bool
	// omitted 表示文件超过大小上限、内容未读取（只按大小比较）。
	omitted bool
}

// equals 判断两份内容是否一致。超大文件未读内容，退化为按大小比较。
func (b gitDiffBlob) equals(other gitDiffBlob) bool {
	if b.omitted || other.omitted {
		return b.omitted && other.omitted && b.size == other.size
	}
	return bytes.Equal(b.content, other.content)
}

// ReadGitDiff 读取 localDir（脚本/工作流目录）的本地变化：
//
//   - 工作区相对本地 HEAD 的未提交改动（含未跟踪文件、HEAD 尚未出生时的全部文件）；
//   - 本地 HEAD 相对 storeDir HEAD 的提交差异（两侧都有提交且 commit 不同时才有）。
//
// localDir 不是 git 仓库时返回 Initialized=false 的空结果，便于接口在「从未保存」
// 的情况下也能安全返回。
func ReadGitDiff(localDir, storeDir string) GitDiffResult {
	localDir = strings.TrimSpace(localDir)
	storeDir = strings.TrimSpace(storeDir)

	result := GitDiffResult{RepoDir: localDir, Worktree: GitDiffSection{Files: []GitDiffFile{}}}

	localCommit, _, initialized := readWorktreeHead(localDir)
	result.Initialized = initialized
	result.HeadCommit = localCommit
	if !initialized {
		return result
	}

	// 未提交改动：本地 HEAD（可能为空）-> 工作区文件。
	baseBlobs, baseErr := readTreeBlobs(localDir, localCommit)
	if baseErr != nil {
		baseBlobs = map[string]gitDiffBlob{}
	}
	result.Worktree = buildGitDiffSection(localCommit, "", baseBlobs, readWorktreeBlobs(localDir))
	result.HasChanges = result.Worktree.HasChanges

	// 已提交未发布：store HEAD -> 本地 HEAD。
	storeCommit, storeOK := readRepoHead(storeDir)
	if !storeOK || storeCommit == "" || localCommit == "" || storeCommit == localCommit {
		return result
	}
	storeBlobs, storeErr := readTreeBlobs(storeDir, storeCommit)
	if storeErr != nil {
		return result
	}
	localBlobs, localErr := readTreeBlobs(localDir, localCommit)
	if localErr != nil {
		return result
	}
	unpublished := buildGitDiffSection(storeCommit, localCommit, storeBlobs, localBlobs)
	if unpublished.HasChanges {
		result.Unpublished = &unpublished
		result.HasChanges = true
	}
	return result
}

// buildGitDiffSection 比较 base（旧）与 target（新）两份文件快照，生成统一的 section。
//
// 不读 git 索引：只要内容不同就算变化，因此未跟踪文件自然表现为 added；
// 单文件与整体 patch 都受大小上限约束，超出只截断并置 Truncated。
func buildGitDiffSection(baseCommit, targetCommit string, base, target map[string]gitDiffBlob) GitDiffSection {
	section := GitDiffSection{
		BaseCommit:   baseCommit,
		TargetCommit: targetCommit,
		Files:        make([]GitDiffFile, 0),
	}

	paths := make([]string, 0, len(base)+len(target))
	for path := range base {
		paths = append(paths, path)
	}
	for path := range target {
		if _, ok := base[path]; !ok {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)

	var patch strings.Builder
	patchFull := false
	for _, path := range paths {
		oldBlob, inBase := base[path]
		newBlob, inTarget := target[path]
		if inBase && inTarget && oldBlob.equals(newBlob) {
			continue
		}
		if len(section.Files) >= gitDiffMaxFiles {
			section.Truncated = true
			break
		}

		file := GitDiffFile{Path: path}
		switch {
		case !inBase:
			file.Status = gitDiffStatusAdded
		case !inTarget:
			file.Status = gitDiffStatusDeleted
		default:
			file.Status = gitDiffStatusModified
		}
		if (inBase && oldBlob.binary) || (inTarget && newBlob.binary) {
			file.Binary = true
		}
		if (inBase && oldBlob.omitted) || (inTarget && newBlob.omitted) {
			file.Omitted = true
		}

		// 二选一：二进制/超大文件只有状态没有内容 diff。
		if !file.Binary && !file.Omitted {
			var oldText, newText string
			if inBase {
				oldText = string(oldBlob.content)
			}
			if inTarget {
				newText = string(newBlob.content)
			}
			text, additions, deletions := unifiedDiffText(path, file.Status, oldText, newText)
			file.Additions, file.Deletions = additions, deletions
			file.Diff = text
			section.Additions += additions
			section.Deletions += deletions

			// patch 超限后不再累加文本，但文件仍登记在 Files 里（只是没有整体 patch）。
			if !patchFull {
				if patch.Len() >= gitDiffMaxPatchBytes {
					patchFull = true
					section.Truncated = true
				} else {
					patch.WriteString(diffGitHeader(path, file.Status))
					patch.WriteString(text)
				}
			}
		}

		section.Files = append(section.Files, file)
	}

	section.FileCount = len(section.Files)
	section.HasChanges = section.FileCount > 0
	section.Patch = patch.String()
	return section
}

// diffGitHeader 生成 `diff --git` 头（新增/删除文件额外补上 mode 行，与 git 输出对齐）。
func diffGitHeader(path, status string) string {
	var builder strings.Builder
	builder.WriteString("diff --git a/" + path + " b/" + path + "\n")
	switch status {
	case gitDiffStatusAdded:
		builder.WriteString("new file mode 100644\n")
	case gitDiffStatusDeleted:
		builder.WriteString("deleted file mode 100644\n")
	}
	return builder.String()
}

// unifiedDiffText 生成单文件的 unified diff 文本（---/+++ 头 + hunk），
// 并返回新增/删除行数。新增文件旧侧为 /dev/null，删除文件新侧为 /dev/null。
func unifiedDiffText(path, status, oldText, newText string) (string, int, int) {
	fromFile, toFile := "a/"+path, "b/"+path
	switch status {
	case gitDiffStatusAdded:
		fromFile = "/dev/null"
	case gitDiffStatusDeleted:
		toFile = "/dev/null"
	}

	text, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        splitDiffLines(oldText),
		B:        splitDiffLines(newText),
		FromFile: fromFile,
		ToFile:   toFile,
		Context:  gitDiffContextLines,
	})
	if err != nil {
		return "", 0, 0
	}
	additions, deletions := countDiffLines(text)
	return text, additions, deletions
}

// splitDiffLines 把文件内容切成「每行都以 \n 结尾」的行列表（difflib 的输入约定），
// 末行没有换行时补齐，避免行数统计与 diff 结果出现偏差。
func splitDiffLines(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.SplitAfter(content, "\n")
	if last := len(lines) - 1; lines[last] == "" {
		lines = lines[:last]
	} else {
		lines[last] += "\n"
	}
	return lines
}

// countDiffLines 统计 unified diff 文本里的新增/删除行数（跳过 ---/+++ 文件头）。
func countDiffLines(text string) (additions, deletions int) {
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			additions++
		case strings.HasPrefix(line, "-"):
			deletions++
		}
	}
	return additions, deletions
}

// readTreeBlobs 读取仓库某个提交的全部文件内容（键为 "/" 分隔的相对路径）。
//
// repoPath 可以是裸仓库（store）也可以是普通工作区仓库；commitHash 为空时返回空快照
// （HEAD 尚未出生：工作区文件应全部视为新增）。
func readTreeBlobs(repoPath, commitHash string) (map[string]gitDiffBlob, error) {
	blobs := make(map[string]gitDiffBlob)
	if strings.TrimSpace(repoPath) == "" || strings.TrimSpace(commitHash) == "" {
		return blobs, nil
	}

	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, err
	}
	commit, err := repo.CommitObject(plumbing.NewHash(commitHash))
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}

	files := tree.Files()
	defer files.Close()
	for {
		file, nextErr := files.Next()
		if stderrs.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nil, nextErr
		}
		blobs[file.Name] = readObjectFileBlob(file)
	}
	return blobs, nil
}

// readObjectFileBlob 读取提交树中的一个文件内容；超大文件只记大小、不读内容。
func readObjectFileBlob(file *object.File) gitDiffBlob {
	if file == nil {
		return gitDiffBlob{omitted: true}
	}
	if file.Size > int64(gitDiffMaxFileBytes) {
		return gitDiffBlob{omitted: true, size: file.Size}
	}

	reader, err := file.Reader()
	if err != nil {
		return gitDiffBlob{omitted: true, size: file.Size}
	}
	defer reader.Close()

	data, err := io.ReadAll(io.LimitReader(reader, int64(gitDiffMaxFileBytes)+1))
	if err != nil || len(data) > gitDiffMaxFileBytes {
		return gitDiffBlob{omitted: true, size: file.Size}
	}
	blob := newGitDiffBlob(data)
	blob.size = file.Size
	return blob
}

// readWorktreeBlobs 读取工作区目录的全部文件内容（跳过 .git 与非普通文件）。
//
// 读不了的条目直接跳过：diff 是只读展示能力，不应因单个坏文件整体失败。
func readWorktreeBlobs(dir string) map[string]gitDiffBlob {
	blobs := make(map[string]gitDiffBlob)
	if strings.TrimSpace(dir) == "" {
		return blobs
	}

	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)

		info, err := entry.Info()
		if err != nil {
			return nil
		}
		if info.Size() > int64(gitDiffMaxFileBytes) {
			blobs[rel] = gitDiffBlob{omitted: true, size: info.Size()}
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		blob := newGitDiffBlob(data)
		blob.size = info.Size()
		blobs[rel] = blob
		return nil
	})
	return blobs
}

// newGitDiffBlob 按内容判定文本/二进制：含 NUL 字节或不是合法 UTF-8 都按二进制处理，
// 只标记变化、不产出文本 diff（与 git 的粗略判定一致）。
func newGitDiffBlob(data []byte) gitDiffBlob {
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return gitDiffBlob{binary: true, size: int64(len(data))}
	}
	return gitDiffBlob{content: data, size: int64(len(data))}
}
