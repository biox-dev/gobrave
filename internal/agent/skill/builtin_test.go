package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	// 每个内置技能都要有可辨识的正文片段，避免 SKILL.md 被截断 / 写错。
	markers := map[string]string{
		BuiltinAnalysisScriptSkillName:     "create_script",
		BuiltinAnalysisNodeScriptSkillName: "run_analysis_node",
	}
	for _, sk := range skills {
		marker, ok := markers[sk.Name]
		if !ok {
			t.Fatalf("builtin skill %q has no marker registered in this test", sk.Name)
		}
		if !strings.Contains(sk.Instructions, marker) {
			t.Fatalf("builtin skill %q instructions look truncated (missing %q)", sk.Name, marker)
		}
	}
}

// 磁盘上的 builtin/skills 是唯一真实来源，运行期读到的正文应与文件内容一致。
func TestBuiltinSkillsReadFromDisk(t *testing.T) {
	dir := t.TempDir()
	name := BuiltinAnalysisScriptSkillName
	custom := "---\nname: " + name + "\ndescription: custom\n---\n\nfrom disk\n"
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name, SkillFileName), []byte(custom), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

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
	path := filepath.Join(dir, BuiltinAnalysisScriptSkillName, SkillFileName)

	// 先写一份过期内容，确保启动时的写入是覆盖而不是仅在缺失时创建。
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatalf("write stale: %v", err)
	}

	if err := EnsureBuiltinSkills(dir); err != nil {
		t.Fatalf("ensure: %v", err)
	}

	found := false
	for _, s := range Discover([]string{dir}) {
		if s.Name == BuiltinAnalysisScriptSkillName {
			found = true
			if !strings.Contains(s.Instructions, "create_script") {
				t.Fatalf("written skill content looks wrong")
			}
		}
	}
	if !found {
		t.Fatalf("builtin skill not discoverable after EnsureBuiltinSkills")
	}

	// 空目录参数应为无操作。
	if err := EnsureBuiltinSkills(""); err != nil {
		t.Fatalf("empty dir should be a no-op: %v", err)
	}
	if !IsBuiltin(BuiltinAnalysisScriptSkillName) {
		t.Fatalf("IsBuiltin should report the builtin skill")
	}
}
