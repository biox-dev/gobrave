package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	gitDiffTestIdentityName  = "gobrave"
	gitDiffTestIdentityEmail = "gobrave@123.com"
)

// TestReadGitDiffWorktree 覆盖本地工作区未提交改动的 diff：
// 未初始化 -> 干净仓库 -> 修改/新增/删除/二进制 -> 已提交未发布（unpublished）。
func TestReadGitDiffWorktree(t *testing.T) {
	root := t.TempDir()
	localDir := filepath.Join(root, "data", "p1", "pipeline", "script", "s1")
	storeDir := filepath.Join(root, "store", "s1")
	identity := GitIdentity{Name: gitDiffTestIdentityName, Email: gitDiffTestIdentityEmail}

	// 1) 目录不存在：不算已初始化，也没有变化。
	diff := ReadGitDiff(localDir, storeDir)
	if diff.Initialized || diff.HasChanges || diff.Worktree.FileCount != 0 || diff.Unpublished != nil {
		t.Fatalf("missing dir diff = %+v, want uninitialized and clean", diff)
	}

	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mainPath := filepath.Join(localDir, "main.py")
	oldPath := filepath.Join(localDir, "old.sh")
	if err := os.WriteFile(mainPath, []byte("print(1)\n"), 0o644); err != nil {
		t.Fatalf("write main: %v", err)
	}
	if err := os.WriteFile(oldPath, []byte("echo old\n"), 0o644); err != nil {
		t.Fatalf("write old: %v", err)
	}
	repo, err := EnsureGitRepo(localDir)
	if err != nil {
		t.Fatalf("EnsureGitRepo: %v", err)
	}
	if _, err := CommitAll(repo, "save script s1", identity); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}

	// 2) 干净仓库：无未提交改动。
	diff = ReadGitDiff(localDir, storeDir)
	if !diff.Initialized || diff.HeadCommit == "" {
		t.Fatalf("initialized state = %+v, want initialized with head", diff)
	}
	if diff.HasChanges || diff.Worktree.FileCount != 0 || diff.Worktree.Patch != "" {
		t.Fatalf("clean diff = %+v, want no changes", diff)
	}
	if diff.Unpublished != nil {
		t.Fatalf("clean diff has unpublished section: %+v", diff.Unpublished)
	}

	// 3) 修改 + 新增 + 删除 + 二进制：四类变化都应体现在 worktree 里。
	if err := os.WriteFile(mainPath, []byte("print(2)\nprint(3)\n"), 0o644); err != nil {
		t.Fatalf("modify main: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "io_schema.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write io_schema: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "blob.bin"), []byte{0x00, 0x01, 0x02}, 0o644); err != nil {
		t.Fatalf("write blob: %v", err)
	}
	if err := os.Remove(oldPath); err != nil {
		t.Fatalf("remove old: %v", err)
	}

	diff = ReadGitDiff(localDir, storeDir)
	if !diff.HasChanges || !diff.Worktree.HasChanges {
		t.Fatalf("dirty diff = %+v, want worktree changes", diff)
	}
	byPath := map[string]GitDiffFile{}
	for _, file := range diff.Worktree.Files {
		byPath[file.Path] = file
	}
	if file, ok := byPath["main.py"]; !ok || file.Status != gitDiffStatusModified {
		t.Fatalf("main.py = %+v, want modified", byPath["main.py"])
	}
	if file, ok := byPath["io_schema.json"]; !ok || file.Status != gitDiffStatusAdded {
		t.Fatalf("io_schema.json = %+v, want added", byPath["io_schema.json"])
	}
	if file, ok := byPath["old.sh"]; !ok || file.Status != gitDiffStatusDeleted {
		t.Fatalf("old.sh = %+v, want deleted", byPath["old.sh"])
	}
	binary, ok := byPath["blob.bin"]
	if !ok || !binary.Binary || binary.Diff != "" {
		t.Fatalf("blob.bin = %+v, want binary without text diff", binary)
	}
	if diff.Worktree.FileCount != 4 {
		t.Fatalf("file count = %d, want 4 (%+v)", diff.Worktree.FileCount, diff.Worktree.Files)
	}
	// main.py: +2/-1；io_schema.json: +1；old.sh: -1；二进制不计行数。
	if diff.Worktree.Additions != 3 || diff.Worktree.Deletions != 2 {
		t.Fatalf("line stats = +%d/-%d, want +3/-2", diff.Worktree.Additions, diff.Worktree.Deletions)
	}
	patch := diff.Worktree.Patch
	for _, want := range []string{
		"diff --git a/main.py b/main.py",
		"-print(1)",
		"+print(2)",
		"+print(3)",
		"diff --git a/io_schema.json b/io_schema.json",
		"new file mode 100644",
		"--- /dev/null",
		"deleted file mode 100644",
		"+++ /dev/null",
	} {
		if !strings.Contains(patch, want) {
			t.Fatalf("patch missing %q:\n%s", want, patch)
		}
	}

	// 4) 提交并发布（push 到 store 裸仓库）后本地无变化；再提交一次才出现「已提交未发布」。
	if _, err := CommitAll(repo, "edit script files", identity); err != nil {
		t.Fatalf("CommitAll #1: %v", err)
	}
	if _, err := EnsureBareGitRepo(storeDir); err != nil {
		t.Fatalf("EnsureBareGitRepo: %v", err)
	}
	if err := PushDirToRepo(t.Context(), localDir, storeDir); err != nil {
		t.Fatalf("PushDirToRepo: %v", err)
	}
	diff = ReadGitDiff(localDir, storeDir)
	if diff.HasChanges || diff.Unpublished != nil {
		t.Fatalf("published diff = %+v, want no changes", diff)
	}

	if err := os.WriteFile(mainPath, []byte("print(4)\n"), 0o644); err != nil {
		t.Fatalf("rewrite main: %v", err)
	}
	if _, err := CommitAll(repo, "edit main", identity); err != nil {
		t.Fatalf("CommitAll #2: %v", err)
	}

	diff = ReadGitDiff(localDir, storeDir)
	if diff.Worktree.HasChanges {
		t.Fatalf("worktree should be clean after commit: %+v", diff.Worktree)
	}
	if diff.Unpublished == nil || !diff.Unpublished.HasChanges {
		t.Fatalf("unpublished = %+v, want changes", diff.Unpublished)
	}
	if !diff.HasChanges {
		t.Fatalf("has changes = false, want true (unpublished commit)")
	}
	if diff.Unpublished.BaseCommit == "" || diff.Unpublished.TargetCommit == "" ||
		diff.Unpublished.BaseCommit == diff.Unpublished.TargetCommit {
		t.Fatalf("unpublished commits = %q -> %q, want distinct non-empty", diff.Unpublished.BaseCommit, diff.Unpublished.TargetCommit)
	}
	if !strings.Contains(diff.Unpublished.Patch, "+print(4)") {
		t.Fatalf("unpublished patch missing new content:\n%s", diff.Unpublished.Patch)
	}
}

// TestReadGitDiffUnbornHead 覆盖「目录已是 git 仓库但还没有任何提交」：
// 工作区文件应全部表现为 added。
func TestReadGitDiffUnbornHead(t *testing.T) {
	root := t.TempDir()
	localDir := filepath.Join(root, "data", "p1", "pipeline", "workflow", "w1")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "workflow.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write workflow.json: %v", err)
	}
	if _, err := EnsureGitRepo(localDir); err != nil {
		t.Fatalf("EnsureGitRepo: %v", err)
	}

	diff := ReadGitDiff(localDir, filepath.Join(root, "store", "w1"))
	if !diff.Initialized {
		t.Fatalf("initialized = false, want true: %+v", diff)
	}
	if diff.HeadCommit != "" {
		t.Fatalf("head = %q, want empty for unborn HEAD", diff.HeadCommit)
	}
	if diff.Worktree.FileCount != 1 || !diff.Worktree.HasChanges {
		t.Fatalf("unborn diff = %+v, want one added file", diff.Worktree)
	}
	file := diff.Worktree.Files[0]
	if file.Path != "workflow.json" || file.Status != gitDiffStatusAdded || file.Additions != 1 {
		t.Fatalf("unborn file = %+v, want added workflow.json with 1 addition", file)
	}
}
