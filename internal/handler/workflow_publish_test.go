package handler

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestCopyDirReplaceExcluding 覆盖发布时把脚本目录快照进 workflow 目录的核心行为：
// 内容整体替换 dst，同时跳过任意层级的 .git，避免把脚本仓库塞进 workflow 仓库。
func TestCopyDirReplaceExcluding(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "main.R"), "print('hi')\n")
	writeTestFile(t, filepath.Join(src, "script.json"), `{"script_id":"s1"}`)
	writeTestFile(t, filepath.Join(src, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeTestFile(t, filepath.Join(src, "sub", "keep.txt"), "keep\n")
	writeTestFile(t, filepath.Join(src, "sub", ".git", "HEAD"), "nested\n")

	dst := filepath.Join(t.TempDir(), "dst")
	if err := copyDirReplaceExcluding(src, dst, ".git"); err != nil {
		t.Fatalf("copyDirReplaceExcluding: %v", err)
	}

	for _, rel := range []string{"main.R", "script.json", "sub/keep.txt"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s not copied: %v", rel, err)
		}
	}
	for _, rel := range []string{".git", "sub/.git"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("%s must not be copied, stat err = %v", rel, err)
		}
	}

	// dst 已有内容时必须被完全替换，而不是叠加。
	writeTestFile(t, filepath.Join(dst, "stale.txt"), "stale\n")
	if err := copyDirReplaceExcluding(src, dst, ".git"); err != nil {
		t.Fatalf("copyDirReplaceExcluding second run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale file should be removed, stat err = %v", err)
	}

	// 不传 exclude 时保持旧行为（.git 一并复制）。
	if err := copyDirReplace(src, dst); err != nil {
		t.Fatalf("copyDirReplace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git", "HEAD")); err != nil {
		t.Fatalf(".git should be copied without exclusion: %v", err)
	}

	// 源目录不存在时按幂等处理（发布/安装时脚本目录可能还没建）。
	missing := filepath.Join(t.TempDir(), "missing")
	if err := copyDirReplace(missing, dst); err != nil {
		t.Fatalf("copyDirReplace with missing source: %v", err)
	}
	if err := copyDirReplaceExcluding(missing, dst, ".git"); err != nil {
		t.Fatalf("copyDirReplaceExcluding with missing source: %v", err)
	}
}

// TestReadInstalledJSONFromDir 校验发布/安装共用的读盘逻辑：
// 命中时能解析出 id，缺失时返回可被 os.IsNotExist 识别的错误（安装时映射为 404）。
func TestReadInstalledJSONFromDir(t *testing.T) {
	workflowDir := t.TempDir()
	writeTestFile(t, filepath.Join(workflowDir, workflowJSONFileName), `{"workflow_id":"wf-1","workflow":{},"scripts":[]}`)

	workflowPayload, err := readWorkflowJSONFromDir(workflowDir)
	if err != nil {
		t.Fatalf("readWorkflowJSONFromDir: %v", err)
	}
	if workflowPayload.WorkflowID != "wf-1" {
		t.Fatalf("workflow_id = %q, want wf-1", workflowPayload.WorkflowID)
	}

	scriptDir := t.TempDir()
	writeTestFile(t, filepath.Join(scriptDir, scriptJSONFileName), `{"script_id":"s-1","script":{}}`)

	scriptPayload, err := readScriptJSONFromDir(scriptDir)
	if err != nil {
		t.Fatalf("readScriptJSONFromDir: %v", err)
	}
	if scriptPayload.ScriptID != "s-1" {
		t.Fatalf("script_id = %q, want s-1", scriptPayload.ScriptID)
	}

	emptyDir := t.TempDir()
	if _, err := readWorkflowJSONFromDir(emptyDir); !os.IsNotExist(err) {
		t.Fatalf("missing workflow.json err = %v, want os.IsNotExist", err)
	}
	if _, err := readScriptJSONFromDir(emptyDir); !os.IsNotExist(err) {
		t.Fatalf("missing script.json err = %v, want os.IsNotExist", err)
	}

	// 非法 JSON 必须报错，而不是返回零值。
	badDir := t.TempDir()
	writeTestFile(t, filepath.Join(badDir, workflowJSONFileName), `{`)
	if _, err := readWorkflowJSONFromDir(badDir); err == nil || os.IsNotExist(err) {
		t.Fatalf("invalid workflow.json err = %v, want parse error", err)
	}
}

// TestResolveStoreJSONPathPrefersRootFile 校验 script/workflow 两种 store 都在根目录优先命中。
func TestResolveStoreJSONPathPrefersRootFile(t *testing.T) {
	storeDir := t.TempDir()
	writeTestFile(t, filepath.Join(storeDir, scriptJSONFileName), `{"script_id":"root"}`)
	writeTestFile(t, filepath.Join(storeDir, "nested", scriptJSONFileName), `{"script_id":"nested"}`)
	writeTestFile(t, filepath.Join(storeDir, workflowJSONFileName), `{"workflow_id":"root"}`)

	scriptPath, err := resolveStoreScriptJSONPath(storeDir)
	if err != nil {
		t.Fatalf("resolveStoreScriptJSONPath: %v", err)
	}
	if scriptPath != filepath.Join(storeDir, scriptJSONFileName) {
		t.Fatalf("script.json path = %s, want root file", scriptPath)
	}

	workflowPath, err := resolveStoreWorkflowJSONPath(storeDir)
	if err != nil {
		t.Fatalf("resolveStoreWorkflowJSONPath: %v", err)
	}
	if workflowPath != filepath.Join(storeDir, workflowJSONFileName) {
		t.Fatalf("workflow.json path = %s, want root file", workflowPath)
	}

	// 只有嵌套文件时回退到 Walk 查找。
	nestedOnly := t.TempDir()
	writeTestFile(t, filepath.Join(nestedOnly, "nested", scriptJSONFileName), `{"script_id":"nested"}`)
	nestedPath, err := resolveStoreScriptJSONPath(nestedOnly)
	if err != nil {
		t.Fatalf("resolveStoreScriptJSONPath nested: %v", err)
	}
	if nestedPath != filepath.Join(nestedOnly, "nested", scriptJSONFileName) {
		t.Fatalf("nested script.json path = %s", nestedPath)
	}

	if _, err := resolveStoreScriptJSONPath(t.TempDir()); err == nil {
		t.Fatal("resolveStoreScriptJSONPath on empty dir should fail")
	}
}
