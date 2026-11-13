package skill

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/builtin"
)

// 内置技能来自 builtin/skills 下的 SKILL.md，必须能解析并通过校验，
// 否则启动写入用户目录后会被 Discover 判定为非法。
func TestBuiltinSkillsParse(t *testing.T) {
	names := BuiltinNames()
	if len(names) == 0 {
		t.Fatalf("no builtin skills declared")
	}
	skills := Builtin()
	if len(skills) != len(names) {
		t.Fatalf("Builtin() returned %d skills, want %d (read/parse failures are skipped silently)", len(skills), len(names))
	}
	for _, sk := range skills {
		if err := sk.Validate(); err != nil {
			t.Fatalf("builtin skill %q invalid: %v", sk.Name, err)
		}
		if strings.TrimSpace(sk.Instructions) == "" {
			t.Fatalf("builtin skill %q has empty instructions", sk.Name)
		}
	}
}

// 内嵌副本随二进制分发，打包后必须含有全部内置技能；列表与内嵌目录应一致。
func TestBuiltinSkillsEmbedded(t *testing.T) {
	entries, err := fs.ReadDir(builtin.SkillFiles, builtin.SkillFSRoot)
	if err != nil {
		t.Fatalf("read embedded skills: %v", err)
	}
	var embedded []string
	for _, e := range entries {
		if _, err := fs.Stat(builtin.SkillFiles, path.Join(builtin.SkillFSRoot, e.Name(), SkillFileName)); err == nil {
			embedded = append(embedded, e.Name())
		}
	}
	if len(embedded) == 0 {
		t.Fatalf("embedded filesystem has no builtin skills")
	}
	for _, name := range embedded {
		if !IsBuiltin(name) {
			t.Fatalf("embedded skill %q missing from BuiltinNames(): %v", name, BuiltinNames())
		}
	}
}

// firstBuiltinName 返回列表中第一个内置技能名，供具体用例避免硬编码技能名。
func firstBuiltinName(t *testing.T) string {
	t.Helper()
	names := BuiltinNames()
	if len(names) == 0 {
		t.Fatalf("no builtin skills found")
	}
	return names[0]
}

// 新增内置技能只需在 builtin/skills 下新建 <name>/SKILL.md，无需改代码即可被发现。
func TestBuiltinSkillsDiscoveredByScan(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "brand-new-skill", "---\nname: brand-new-skill\ndescription: workspace skill\n---\n\nfrom scan\n")
	// 没有 SKILL.md 的目录应被忽略。
	if err := os.MkdirAll(filepath.Join(dir, "not-a-skill"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	t.Cleanup(func() { SetBuiltinSkillDir("") })
	SetBuiltinSkillDir(dir)

	if !IsBuiltin("brand-new-skill") {
		t.Fatalf("new skill directory not discovered: %v", BuiltinNames())
	}
	if IsBuiltin("not-a-skill") {
		t.Fatalf("directory without SKILL.md should be ignored")
	}
	var found *Skill
	for _, s := range Builtin() {
		if s.Name == "brand-new-skill" {
			found = s
		}
	}
	if found == nil || !strings.Contains(found.Instructions, "from scan") {
		t.Fatalf("builtin skill content not loaded from disk")
	}
}

// writeSkillFile 在 base/<name>/SKILL.md 写入技能文件。
func writeSkillFile(t *testing.T, base, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(base, name), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(base, name, SkillFileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// 磁盘上的 builtin/skills 是唯一真实来源，运行期读到的正文应与文件内容一致。
func TestBuiltinSkillsReadFromDisk(t *testing.T) {
	name := firstBuiltinName(t)
	dir := t.TempDir()
	writeSkillFile(t, dir, name, "---\nname: "+name+"\ndescription: custom\n---\n\nfrom disk\n")

	t.Cleanup(func() { SetBuiltinSkillDir("") })
	SetBuiltinSkillDir(dir)
	if !strings.Contains(readBuiltinSkillFileForTest(t, name), "from disk") {
		t.Fatalf("expected disk file to be preferred over embedded copy")
	}
}

func readBuiltinSkillFileForTest(t *testing.T, name string) string {
	t.Helper()
	data, err := readBuiltinSkillFile(name)
	if err != nil {
		t.Fatalf("read builtin skill %q: %v", name, err)
	}
	return string(data)
}

func TestEnsureBuiltinSkillsWritesAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	names := BuiltinNames()
	name := names[0]

	// 先写一份过期内容，确保启动时的写入是覆盖而不是仅在缺失时创建。
	writeSkillFile(t, dir, name, "stale")

	if err := EnsureBuiltinSkills(dir); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	// 列表中的每个内置技能都应写入并可被 Discover 加载。
	got := make(map[string]bool, len(names))
	for _, s := range Discover([]string{dir}) {
		got[s.Name] = true
	}
	for _, n := range names {
		if !got[n] {
			t.Fatalf("builtin skill %q not discoverable after EnsureBuiltinSkills", n)
		}
	}

	// 覆盖写入：内容应与源码 / 内嵌副本一致，而不是保留旧的 stale。
	content, err := os.ReadFile(filepath.Join(dir, name, SkillFileName))
	if err != nil {
		t.Fatalf("read written skill: %v", err)
	}
	if string(content) != readBuiltinSkillFileForTest(t, name) {
		t.Fatalf("written skill %q was not overwritten with builtin content", name)
	}

	// 空目录参数应为无操作。
	if err := EnsureBuiltinSkills(""); err != nil {
		t.Fatalf("empty dir should be a no-op: %v", err)
	}
	if !IsBuiltin(name) {
		t.Fatalf("IsBuiltin should report the builtin skill")
	}
}

// Overwrite=false（config.agent.overwrite_builtin_skills=false）时，
// 已存在的 SKILL.md 保留用户修改，只补齐缺失的内置技能。
func TestEnsureBuiltinSkillsKeepsExistingWhenOverwriteDisabled(t *testing.T) {
	dir := t.TempDir()
	names := BuiltinNames()
	name := names[0]

	const custom = "---\nname: local-edit\ndescription: local edits\n---\n\nlocal edits\n"
	writeSkillFile(t, dir, name, custom)

	if err := EnsureBuiltinSkillsWithOptions(dir, BuiltinWriteOptions{Overwrite: false}); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(dir, name, SkillFileName))
	if err != nil {
		t.Fatalf("read written skill: %v", err)
	}
	if string(content) != custom {
		t.Fatalf("existing skill %q should be preserved when overwrite is disabled, got %q", name, string(content))
	}

	// 其余缺失的内置技能仍会补齐。
	for _, n := range names {
		if n == name {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, n, SkillFileName)); err != nil {
			t.Fatalf("missing builtin skill %q should be written even when overwrite is disabled: %v", n, err)
		}
	}
}
