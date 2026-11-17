package skill

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

func TestWriterCreateSkill(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry()
	w := NewWriter(reg, NewStore(dir))

	info := createSkill(t, w, runtimecontext.SkillCreateInput{
		Name:         "demo-skill",
		Description:  "A demo skill.",
		Instructions: "# Demo\n\nStep 1.",
	})
	if info.Name != "demo-skill" || info.SkillFile == "" {
		t.Fatalf("info = %+v", info)
	}

	content, err := os.ReadFile(info.SkillFile)
	if err != nil {
		t.Fatalf("read skill file: %v", err)
	}
	for _, want := range []string{"name: demo-skill", "description: A demo skill.", "Step 1."} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("skill file missing %q:\n%s", want, content)
		}
	}
	if s, ok := reg.Get("demo-skill"); !ok || s.Description != "A demo skill." {
		t.Fatalf("registry entry = %+v, ok = %v", s, ok)
	}

	// 重名创建应失败。
	if _, err := w.PlanCreate(runtimecontext.SkillCreateInput{Name: "demo-skill", Description: "dup", Instructions: "x"}); err == nil {
		t.Fatalf("expected duplicate name error")
	}
	// 非法技能名（目录穿越 / 大写非法字符）应失败。
	if _, err := w.PlanCreate(runtimecontext.SkillCreateInput{Name: "../bad", Description: "d", Instructions: "x"}); err == nil {
		t.Fatalf("expected invalid name error")
	}
}

func TestWriterEditSkill(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry()
	w := NewWriter(reg, NewStore(dir))

	created := createSkill(t, w, runtimecontext.SkillCreateInput{
		Name:         "demo-skill",
		Description:  "A demo skill.",
		Instructions: "# Demo\n\nStep 1.",
	})

	updated := editSkill(t, w, "demo-skill", "Step 1.", "Step 2.")
	if !strings.Contains(updated.Instructions, "Step 2.") {
		t.Fatalf("instructions = %q", updated.Instructions)
	}

	content, err := os.ReadFile(created.SkillFile)
	if err != nil {
		t.Fatalf("read skill file: %v", err)
	}
	if !strings.Contains(string(content), "Step 2.") || strings.Contains(string(content), "Step 1.") {
		t.Fatalf("skill file not updated:\n%s", content)
	}
	// 注册表热更新：后续 get_skill 拿到的是新正文。
	if s, _ := reg.Get("demo-skill"); !strings.Contains(s.Instructions, "Step 2.") {
		t.Fatalf("registry not refreshed: %+v", s)
	}

	// 改 description（frontmatter）同样生效。
	editSkill(t, w, "demo-skill", "description: A demo skill.", "description: Updated.")
	if s, _ := reg.Get("demo-skill"); s.Description != "Updated." {
		t.Fatalf("description = %q", s.Description)
	}

	// 不存在的技能 / 未匹配的 old_string 应失败。
	if _, err := w.PlanEdit("missing", "a", "b"); err == nil {
		t.Fatalf("expected not found error")
	}
	if _, err := w.PlanEdit("demo-skill", "not-in-file", "b"); err == nil {
		t.Fatalf("expected old_string not found error")
	}
}

func TestWriterEditSkillRejectsMultipleMatchesAndInvalidResult(t *testing.T) {
	dir := t.TempDir()
	reg := NewRegistry()
	w := NewWriter(reg, NewStore(dir))

	created := createSkill(t, w, runtimecontext.SkillCreateInput{
		Name:         "demo-skill",
		Description:  "A demo skill.",
		Instructions: "# Demo\n\nStep A.\n\nStep A.",
	})

	if _, err := w.PlanEdit("demo-skill", "Step A.", "Step B."); err == nil {
		t.Fatalf("expected multiple match error")
	}
	// 破坏 frontmatter 的编辑必须被拒绝，且不改动磁盘内容。
	if _, err := w.PlanEdit("demo-skill", "name: demo-skill", "name: Bad_Name"); err == nil {
		t.Fatalf("expected invalid skill error")
	}
	content, err := os.ReadFile(created.SkillFile)
	if err != nil {
		t.Fatalf("read skill file: %v", err)
	}
	if !strings.Contains(string(content), "name: demo-skill") {
		t.Fatalf("file should stay unchanged:\n%s", content)
	}
}

func TestWriterEditSkillRejectsSkillOutsideStore(t *testing.T) {
	external := t.TempDir()
	path := writeSkill(t, external, "external-skill", sampleSKILL)

	reg := NewRegistryWith(&Skill{
		Name:          "external-skill",
		Description:   "A demo skill for tests.",
		Path:          external + "/external-skill",
		SkillFilePath: path,
	})
	w := NewWriter(reg, NewStore(t.TempDir()))

	if _, err := w.PlanEdit("external-skill", "Follow these steps.", "Do it."); err == nil {
		t.Fatalf("expected not-user-managed error")
	}
	if _, err := w.PlanUpdate("external-skill", runtimecontext.SkillUpdateInput{Description: "x"}); err == nil {
		t.Fatalf("expected not-user-managed error for field update")
	}
}

// TestWriterUpdateSkill 校验结构化字段覆盖：只改传入字段、保留其它 frontmatter 字段，
// 并在 Apply 后热更新注册表。
func TestWriterUpdateSkill(t *testing.T) {
	// license 是规范之外的字段，用于校验覆盖 description 时不会被丢掉。
	const raw = "---\nname: field-skill\ndescription: old desc\nlicense: MIT\n---\n\n# Demo\n\nStep 1.\n"
	dir := t.TempDir()
	path := writeSkill(t, dir, "field-skill", raw)

	s, err := Parse(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	reg := NewRegistryWith(s)
	w := NewWriter(reg, NewStore(dir))

	// 只改 description：正文与未知字段（license）都必须保留。
	info := updateSkill(t, w, "field-skill", runtimecontext.SkillUpdateInput{Description: "new desc"})
	if info.Description != "new desc" {
		t.Fatalf("description = %q", info.Description)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(content), "license: MIT") || !strings.Contains(string(content), "Step 1.") {
		t.Fatalf("field update should preserve body and unknown frontmatter fields:\n%s", content)
	}

	// 只改 instructions：frontmatter 文本保持原样。
	info = updateSkill(t, w, "field-skill", runtimecontext.SkillUpdateInput{Instructions: "# Demo\n\nStep 2."})
	if !strings.Contains(info.Instructions, "Step 2.") {
		t.Fatalf("instructions = %q", info.Instructions)
	}
	content, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(content), "description: new desc") || !strings.Contains(string(content), "license: MIT") {
		t.Fatalf("body update should keep frontmatter as-is:\n%s", content)
	}
	if strings.Contains(string(content), "Step 1.") {
		t.Fatalf("old body should be replaced:\n%s", content)
	}
	// 注册表热更新。
	if s, _ := reg.Get("field-skill"); s.Description != "new desc" || !strings.Contains(s.Instructions, "Step 2.") {
		t.Fatalf("registry not refreshed: %+v", s)
	}

	// 空字段 / 值无变化 / 技能不存在都应该报错。
	if _, err := w.PlanUpdate("field-skill", runtimecontext.SkillUpdateInput{}); err == nil {
		t.Fatalf("expected missing field error")
	}
	if _, err := w.PlanUpdate("field-skill", runtimecontext.SkillUpdateInput{Description: "new desc"}); err == nil {
		t.Fatalf("expected no-change error")
	}
	if _, err := w.PlanUpdate("missing", runtimecontext.SkillUpdateInput{Description: "d"}); err == nil {
		t.Fatalf("expected not found error")
	}
}

// TestEditSkillFieldOverwrite 校验 edit_skill 的结构化覆盖模式：一次带入 description 与
// instructions，两种编辑模式混用时必须拒绝。
func TestEditSkillFieldOverwrite(t *testing.T) {
	const raw = "---\nname: field-skill\ndescription: old desc\nlicense: MIT\n---\n\nStep 1.\n"
	dir := t.TempDir()
	path := writeSkill(t, dir, "field-skill", raw)
	s, err := Parse(path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	reg := NewRegistryWith(s)
	w := NewWriter(reg, NewStore(dir))
	ctx := tool.WithPermissionRequester(context.Background(), &allowPermissionRequester{})

	var out SkillWriteOutput
	res := runTool(t, ctx, EditSkill(w), `{"name":"field-skill","description":"tool desc","instructions":"Step 2."}`)
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Error != "" {
		t.Fatalf("unexpected error: %s", out.Error)
	}
	if s, _ := reg.Get("field-skill"); s.Description != "tool desc" || !strings.Contains(s.Instructions, "Step 2.") {
		t.Fatalf("skill not updated: %+v", s)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(content), "license: MIT") {
		t.Fatalf("unknown frontmatter field lost:\n%s", content)
	}

	// 两种模式混用必须拒绝，且不落盘。
	res = runTool(t, ctx, EditSkill(w), `{"name":"field-skill","description":"x","old_string":"Step 2.","new_string":"Step 3."}`)
	if err := json.Unmarshal(res, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(out.Error, "choose one edit mode") {
		t.Fatalf("err = %q, want mode conflict", out.Error)
	}
	if s, _ := reg.Get("field-skill"); s.Description != "tool desc" {
		t.Fatalf("conflicting edit should not change the skill: %+v", s)
	}
}

// TestWriterNoticesBuiltinOverwrite 验证：overwrite_builtin_skills=true（默认）时，
// 修改内置技能的写计划会带上「重启后被内置覆盖」的提醒；关闭后不再提醒。
func TestWriterNoticesBuiltinOverwrite(t *testing.T) {
	names := BuiltinNames()
	if len(names) == 0 {
		t.Skip("no builtin skills available in this build")
	}
	name := names[0]
	content := strings.Replace(sampleSKILL, "name: demo-skill", "name: "+name, 1)

	dir := t.TempDir()
	path := writeSkill(t, dir, name, content)
	reg := NewRegistryWith(&Skill{
		Name:          name,
		Description:   "A demo skill for tests.",
		Path:          filepath.Join(dir, name),
		SkillFilePath: path,
	})

	// 默认（覆盖）：必须提醒用户如何保留修改。
	plan, err := NewWriter(reg, NewStore(dir)).PlanEdit(name, "Follow these steps.", "Do it.")
	if err != nil {
		t.Fatalf("PlanEdit: %v", err)
	}
	if !strings.Contains(plan.Notice, "overwrite_builtin_skills") {
		t.Fatalf("notice = %q, want overwrite_builtin_skills hint", plan.Notice)
	}

	// overwrite_builtin_skills=false：修改会被保留，无需提醒。
	plan, err = NewWriter(reg, NewStore(dir), WithOverwriteBuiltinSkills(false)).PlanEdit(name, "Follow these steps.", "Do it.")
	if err != nil {
		t.Fatalf("PlanEdit: %v", err)
	}
	if plan.Notice != "" {
		t.Fatalf("notice = %q, want empty when overwrite is disabled", plan.Notice)
	}

	// 非内置技能不应出现提醒。
	plainPath := writeSkill(t, dir, "plain-skill", strings.Replace(sampleSKILL, "name: demo-skill", "name: plain-skill", 1))
	reg.Register(&Skill{
		Name:          "plain-skill",
		Description:   "A demo skill for tests.",
		Path:          filepath.Join(dir, "plain-skill"),
		SkillFilePath: plainPath,
	})
	plan, err = NewWriter(reg, NewStore(dir)).PlanEdit("plain-skill", "Follow these steps.", "Do it.")
	if err != nil {
		t.Fatalf("PlanEdit: %v", err)
	}
	if plan.Notice != "" {
		t.Fatalf("notice = %q, want empty for non-builtin skill", plan.Notice)
	}
}

// denyPermissionRequester 记录授权请求并一律拒绝。
type denyPermissionRequester struct {
	got tool.PermissionRequest
}

func (d *denyPermissionRequester) RequestPermission(_ context.Context, req tool.PermissionRequest) bool {
	d.got = req
	return false
}

// allowPermissionRequester 记录授权请求并一律放行（用于执行写入链路）。
type allowPermissionRequester struct {
	got tool.PermissionRequest
}

func (a *allowPermissionRequester) RequestPermission(_ context.Context, req tool.PermissionRequest) bool {
	a.got = req
	return true
}

func TestSkillWriteToolsRequireWritePermission(t *testing.T) {
	w := NewWriter(NewRegistry(), NewStore(t.TempDir()))
	createSkill(t, w, runtimecontext.SkillCreateInput{
		Name:         "demo-skill",
		Description:  "A demo skill.",
		Instructions: "# Demo\n\nStep 1.",
	})

	cases := []struct {
		name     string
		tool     tool.Tool
		args     string
		wantDesc string
	}{
		{
			name:     "create_skill",
			tool:     CreateSkill(w),
			args:     `{"name":"other-skill","description":"d","instructions":"x"}`,
			wantDesc: "Create skill other-skill",
		},
		{
			name:     "edit_skill",
			tool:     EditSkill(w),
			args:     `{"name":"demo-skill","old_string":"Step 1.","new_string":"Step 2."}`,
			wantDesc: "Edit skill demo-skill",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requester := &denyPermissionRequester{}
			ctx := tool.WithPermissionRequester(context.Background(), requester)

			_, err := tc.tool.Execute(ctx, json.RawMessage(tc.args))
			if !errors.Is(err, tool.ErrPermissionDenied) {
				t.Fatalf("err = %v, want ErrPermissionDenied", err)
			}
			if requester.got.Action != "write" || requester.got.Description != tc.wantDesc {
				t.Fatalf("permission request = %+v", requester.got)
			}
			// 授权请求必须带上与 coder edit / write 同键的 diff 预览。
			preview, ok := requester.got.Params.(tool.PermissionPreview)
			if !ok || preview.FilePath == "" || !strings.Contains(preview.Diff, "SKILL.md") {
				t.Fatalf("permission params = %#v, want file_path + diff preview", requester.got.Params)
			}
		})
	}
}

// createSkill 通过 PlanCreate + Apply 创建技能（测试内的便捷封装）。
func createSkill(t *testing.T, w *Writer, in runtimecontext.SkillCreateInput) runtimecontext.SkillInfo {
	t.Helper()
	plan, err := w.PlanCreate(in)
	if err != nil {
		t.Fatalf("PlanCreate: %v", err)
	}
	info, err := w.Apply(plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return info
}

// editSkill 通过 PlanEdit + Apply 修改技能（测试内的便捷封装）。
func editSkill(t *testing.T, w *Writer, name, oldString, newString string) runtimecontext.SkillInfo {
	t.Helper()
	plan, err := w.PlanEdit(name, oldString, newString)
	if err != nil {
		t.Fatalf("PlanEdit: %v", err)
	}
	info, err := w.Apply(plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return info
}

// updateSkill 通过 PlanUpdate + Apply 按字段覆盖技能（测试内的便捷封装）。
func updateSkill(t *testing.T, w *Writer, name string, in runtimecontext.SkillUpdateInput) runtimecontext.SkillInfo {
	t.Helper()
	plan, err := w.PlanUpdate(name, in)
	if err != nil {
		t.Fatalf("PlanUpdate: %v", err)
	}
	info, err := w.Apply(plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return info
}
