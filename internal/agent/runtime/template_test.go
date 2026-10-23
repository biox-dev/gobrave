package agent

import (
	"strings"
	"testing"
)

// TestRenderSystemPromptTemplate 校验用变量渲染 Profile 系统提示词模板的行为：
// 变量替换、未定义变量静默为空、{% if %} 守卫按需输出整行。
func TestRenderSystemPromptTemplate(t *testing.T) {
	// 空模板 → 空串、无错误。
	out, err := RenderSystemPromptTemplate("   ", map[string]any{"a": 1})
	if err != nil || out != "" {
		t.Fatalf("empty template: out=%q err=%v", out, err)
	}

	// 变量替换。
	out, err = RenderSystemPromptTemplate("workspace_directory: {{ workspace_directory }}", map[string]any{
		"workspace_directory": "/data/proj",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if out != "workspace_directory: /data/proj" {
		t.Fatalf("out = %q", out)
	}

	// 未定义变量 + {% if %} 守卫：整行不出现。
	out, err = RenderSystemPromptTemplate("base{% if output_file %}\noutput_file: {{ output_file }}{% endif %}", nil)
	if err != nil {
		t.Fatalf("render nil vars: %v", err)
	}
	if strings.Contains(out, "output_file:") {
		t.Fatalf("guarded line should be omitted, got %q", out)
	}
	if strings.TrimSpace(out) != "base" {
		t.Fatalf("out = %q, want base", out)
	}

	// 守卫命中时输出整行。
	out, err = RenderSystemPromptTemplate("base{% if output_file %}\noutput_file: {{ output_file }}{% endif %}", map[string]any{
		"output_file": "/tmp/output.md",
	})
	if err != nil {
		t.Fatalf("render with output_file: %v", err)
	}
	if !strings.Contains(out, "output_file: /tmp/output.md") {
		t.Fatalf("out = %q", out)
	}
}
