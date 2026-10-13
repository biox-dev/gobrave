package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
