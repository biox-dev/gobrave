package skill

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

const sampleSKILL = `---
name: demo-skill
description: A demo skill for tests.
user-invocable: true
---

# Demo
Follow these steps.
`

func writeSkill(t *testing.T, dir, name, content string) string {
	t.Helper()
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(skillDir, SkillFileName)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestParseContent(t *testing.T) {
	s, err := ParseContent([]byte(sampleSKILL))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if s.Name != "demo-skill" || s.Description != "A demo skill for tests." {
		t.Fatalf("metadata = %+v", s)
	}
	if !s.UserInvocable {
		t.Fatalf("user-invocable not parsed")
	}
	if !strings.Contains(s.Instructions, "Follow these steps.") {
		t.Fatalf("instructions = %q", s.Instructions)
	}
}

func TestParseContentRequiresFrontmatter(t *testing.T) {
	if _, err := ParseContent([]byte("no frontmatter")); err == nil {
		t.Fatalf("expected error for missing frontmatter")
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		skill   Skill
		wantErr bool
	}{
		{"ok", Skill{Name: "a-b", Description: "d"}, false},
		{"empty name", Skill{Description: "d"}, true},
		{"bad name", Skill{Name: "Bad_Name", Description: "d"}, true},
		{"empty description", Skill{Name: "a"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.skill.Validate()
			if tc.wantErr != (err != nil) {
				t.Fatalf("Validate() err = %v, wantErr = %v", err, tc.wantErr)
			}
		})
	}
}

func TestDiscoverAndRegistry(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, dir, "demo-skill", sampleSKILL)
	writeSkill(t, dir, "broken", "---\nname: broken\n---\n") // 缺 description

	skills, states := DiscoverWithStates([]string{dir})
	if len(skills) != 1 || skills[0].Name != "demo-skill" {
		t.Fatalf("skills = %+v", skills)
	}

	var sawError bool
	for _, st := range states {
		if st.State == StateError {
			sawError = true
		}
	}
	if !sawError {
		t.Fatalf("expected an error state for the invalid skill")
	}

	reg := NewRegistryWith(skills...)
	if !reg.Has("demo-skill") {
		t.Fatalf("registry missing demo-skill")
	}
	if got := reg.Names(); len(got) != 1 || got[0] != "demo-skill" {
		t.Fatalf("names = %v", got)
	}
}

func TestRegistryPromptBlock(t *testing.T) {
	reg := NewRegistryWith(
		&Skill{Name: "alpha", Description: "first", SkillFilePath: "alpha/SKILL.md"},
		&Skill{Name: "beta", Description: "second", SkillFilePath: "beta/SKILL.md"},
		&Skill{Name: "hidden", Description: "quiet", DisableModelInvocation: true, SkillFilePath: "x"},
	)

	block := reg.PromptBlock()
	if !strings.Contains(block, "<available_skills>") || !strings.Contains(block, "alpha") {
		t.Fatalf("block = %q", block)
	}
	if !strings.Contains(block, "<name>alpha</name>") || !strings.Contains(block, "<description>first</description>") {
		t.Fatalf("block should contain name and description: %q", block)
	}
	if strings.Contains(block, "<location>") {
		t.Fatalf("block must not contain locations (skills are loaded via get_skill): %q", block)
	}
	if strings.Contains(block, "hidden") {
		t.Fatalf("block should exclude disable-model-invocation skills: %q", block)
	}
	// 元信息模式：不应注入指令正文。
	if strings.Contains(block, "Instructions") {
		t.Fatalf("block must not contain instruction bodies")
	}

	// 仅选中 beta。
	only := reg.PromptBlock("beta")
	if strings.Contains(only, "alpha") || !strings.Contains(only, "beta") {
		t.Fatalf("filtered block = %q", only)
	}

	if got := reg.PromptBlock("missing"); got != "" {
		t.Fatalf("missing selection = %q, want empty", got)
	}
	if got := NewRegistry().PromptBlock(); got != "" {
		t.Fatalf("empty registry = %q, want empty", got)
	}
}

func TestGetSkillTool(t *testing.T) {
	dir := t.TempDir()
	path := writeSkill(t, dir, "demo-skill", sampleSKILL)
	if err := os.MkdirAll(filepath.Join(filepath.Dir(path), "references"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), "references", "design.md"), []byte("# Design"), 0o644); err != nil {
		t.Fatalf("write resource: %v", err)
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), ManifestFileName), []byte(`{"version":"1.2.0"}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	s, err := Parse(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx := tool.WithSkillResolver(context.Background(), NewRegistryWith(s).Resolver())

	out := runTool(t, ctx, GetSkill(), `{"name":"demo-skill"}`)
	var got GetSkillOutput
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	if got.Error != "" {
		t.Fatalf("unexpected error: %s", got.Error)
	}
	if got.Name != "demo-skill" || got.Version != "1.2.0" {
		t.Fatalf("meta = %+v", got)
	}
	if !strings.Contains(got.Instructions, "Follow these steps.") {
		t.Fatalf("instructions = %q", got.Instructions)
	}
	if len(got.Resources) != 1 || got.Resources[0] != "references/design.md" {
		t.Fatalf("resources = %v", got.Resources)
	}

	// 版本精确匹配。
	if out := runTool(t, ctx, GetSkill(), `{"name":"demo-skill","version":"1.2.0"}`); strings.Contains(string(out), `"error"`) {
		t.Fatalf("exact version should succeed: %s", out)
	}
	if out := runTool(t, ctx, GetSkill(), `{"name":"demo-skill","version":"9.9.9"}`); !strings.Contains(string(out), "is not available") {
		t.Fatalf("mismatched version should fail: %s", out)
	}
	// 未知技能。
	if out := runTool(t, ctx, GetSkill(), `{"name":"nope"}`); !strings.Contains(string(out), "not found") {
		t.Fatalf("unknown skill should fail: %s", out)
	}
}

func TestLoadSkillResourceTool(t *testing.T) {
	dir := t.TempDir()
	path := writeSkill(t, dir, "demo-skill", sampleSKILL)
	resDir := filepath.Join(filepath.Dir(path), "references")
	if err := os.MkdirAll(resDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(resDir, "design.md"), []byte("# Design"), 0o644); err != nil {
		t.Fatalf("write resource: %v", err)
	}

	s, err := Parse(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ctx := tool.WithSkillResolver(context.Background(), NewRegistryWith(s).Resolver())

	out := runTool(t, ctx, LoadSkillResource(), `{"name":"demo-skill","path":"references/design.md"}`)
	var got LoadSkillResourceOutput
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	if got.Error != "" || got.Content != "# Design" {
		t.Fatalf("result = %+v", got)
	}
	if !filepath.IsAbs(got.AbsPath) {
		t.Fatalf("abs_path = %q", got.AbsPath)
	}

	// 目录穿越被拒绝。
	if out := runTool(t, ctx, LoadSkillResource(), `{"name":"demo-skill","path":"../../etc/passwd"}`); !strings.Contains(string(out), "escapes the skill directory") {
		t.Fatalf("traversal should be rejected: %s", out)
	}
	// 绝对路径被规范化到技能目录内（不会读到技能目录外的文件）。
	if out := runTool(t, ctx, LoadSkillResource(), `{"name":"demo-skill","path":"/etc/passwd"}`); !strings.Contains(string(out), "no such file or directory") {
		t.Fatalf("absolute path must stay inside the skill dir: %s", out)
	}
	// 不允许用本工具重复读取 SKILL.md。
	if out := runTool(t, ctx, LoadSkillResource(), `{"name":"demo-skill","path":"SKILL.md"}`); !strings.Contains(string(out), "get_skill") {
		t.Fatalf("SKILL.md should point at get_skill: %s", out)
	}
}

// runTool 执行一个工具并返回其 JSON 结果内容。
func runTool(t *testing.T, ctx context.Context, tl tool.Tool, args string) []byte {
	t.Helper()
	res, err := tl.Execute(ctx, json.RawMessage(args))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return out
}

func TestDeduplicateAndFilter(t *testing.T) {
	a1 := &Skill{Name: "dup", Description: "builtin"}
	a2 := &Skill{Name: "dup", Description: "user"}
	other := &Skill{Name: "keep", Description: "k"}

	deduped := Deduplicate([]*Skill{a1, a2, other})
	if len(deduped) != 2 {
		t.Fatalf("deduped = %+v", deduped)
	}
	for _, s := range deduped {
		if s.Name == "dup" && s.Description != "user" {
			t.Fatalf("last occurrence should win: %+v", s)
		}
	}

	filtered := Filter(deduped, []string{"dup"})
	if len(filtered) != 1 || filtered[0].Name != "keep" {
		t.Fatalf("filtered = %+v", filtered)
	}
}
