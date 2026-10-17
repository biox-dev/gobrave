package utils

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadBuiltinPrompt(t *testing.T) {
	dir := t.TempDir()
	name := "demo"
	path := BuiltinPromptPath(dir, name)
	if got, want := path, filepath.Join(dir, BuiltinPromptDir, "demo.md"); got != want {
		t.Fatalf("BuiltinPromptPath = %q, want %q", got, want)
	}

	// 磁盘缺失 → 回退到内嵌副本；内嵌也不存在则为空串，均不报错。
	got, err := ReadBuiltinPrompt(dir, "no_such_prompt")
	if err != nil {
		t.Fatalf("read missing prompt: %v", err)
	}
	if got != "" {
		t.Fatalf("missing prompt = %q, want empty", got)
	}

	// 写入后读取：首尾空白被裁掉。
	if err := WriteBuiltinPrompt(dir, name, "\n  模板正文\n\n"); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	got, err = ReadBuiltinPrompt(dir, name)
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	if got != "模板正文" {
		t.Fatalf("prompt = %q, want %q", got, "模板正文")
	}

	// 原始内容保留空白（ReadBuiltinPromptFile 不做规整）。
	raw, err := ReadBuiltinPromptFile(dir, name)
	if err != nil {
		t.Fatalf("read raw prompt: %v", err)
	}
	if raw != "\n  模板正文\n\n" {
		t.Fatalf("raw prompt = %q", raw)
	}
}

func TestReadBuiltinPromptFallsBackToEmbedded(t *testing.T) {
	// 空 baseDir 指向不存在的磁盘目录，读取内置提示词名时必须命中 go:embed 副本，
	// 保证 Docker / 单文件部署（运行目录没有 builtin/）也能拿到提示词。
	dir := filepath.Join(t.TempDir(), "nonexistent")

	for name, marker := range map[string]string{
		"default":        "workspace_directory",
		"analysis_coder": "run_analysis_node",
		"article_writer": "科研写作助手",
		"summary":        "生物信息学分析助手",
	} {
		got, err := ReadBuiltinPrompt(dir, name)
		if err != nil {
			t.Fatalf("read embedded prompt %q: %v", name, err)
		}
		if !strings.Contains(got, marker) {
			t.Fatalf("embedded prompt %q = %q, want it to contain %q", name, got, marker)
		}
	}
}

func TestReadBuiltinPromptDiskOverridesEmbedded(t *testing.T) {
	dir := t.TempDir()
	if err := WriteBuiltinPrompt(dir, "default", "磁盘覆盖版本"); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	got, err := ReadBuiltinPrompt(dir, "default")
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	if got != "磁盘覆盖版本" {
		t.Fatalf("prompt = %q, want disk override", got)
	}
}

func TestReadBuiltinPromptPermissionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root 可读任意文件，跳过权限用例")
	}
	dir := t.TempDir()
	name := "denied"
	if err := WriteBuiltinPrompt(dir, name, "x"); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	if err := os.Chmod(BuiltinPromptPath(dir, name), 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if _, err := ReadBuiltinPrompt(dir, name); err == nil {
		t.Fatalf("read unreadable prompt should fail")
	}
}
