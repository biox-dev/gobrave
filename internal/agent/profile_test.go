package agent

import (
	"context"
	"testing"

	"github.com/biox-dev/gobrave/internal/utils"
)

// newTestProfileManager 创建一个已同步内置 Profile 的内存 Profile 管理器。
func newTestProfileManager(t *testing.T) *ProfileManager {
	t.Helper()
	repo := NewMemoryProfileRepository()
	if err := EnsureBuiltinProfiles(context.Background(), repo); err != nil {
		t.Fatalf("seed builtin profiles: %v", err)
	}
	return NewProfileManager(repo)
}

func TestProfileManagerResolveBuiltin(t *testing.T) {
	m := newTestProfileManager(t)
	ctx := context.Background()

	// 未指定名称 → 内置默认。
	p, err := m.Resolve(ctx, "u1", "")
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if p.Name != DefaultProfileName {
		t.Fatalf("default profile name = %q, want %q", p.Name, DefaultProfileName)
	}
	if !p.Context.InjectMemory || p.Context.InjectProject {
		t.Fatalf("default context = %+v, want memory on / project off", p.Context)
	}

	// 指定内置名称 → 命中内置 Profile。
	p, err = m.Resolve(ctx, "u1", ProfileArticleWriter)
	if err != nil {
		t.Fatalf("resolve article_writer: %v", err)
	}
	if !p.Context.InjectProject {
		t.Fatalf("article_writer should inject project context")
	}
}

func TestProfileManagerUserDefault(t *testing.T) {
	_ = utils.InitSnowflake(1)
	m := newTestProfileManager(t)
	ctx := context.Background()

	custom := &Profile{
		Name:         "my_writer",
		DisplayName:  "我的写作助手",
		UserID:       "u1",
		IsDefault:    true,
		SystemPrompt: "写作",
		Context:      ContextConfig{InjectMemory: true, InjectProject: true},
	}
	if err := m.Save(ctx, custom); err != nil {
		t.Fatalf("save custom: %v", err)
	}

	// 用户默认优先于内置默认。
	p, err := m.Resolve(ctx, "u1", "")
	if err != nil {
		t.Fatalf("resolve user default: %v", err)
	}
	if p.Name != "my_writer" {
		t.Fatalf("user default name = %q, want my_writer", p.Name)
	}

	// 名称规整：空格转下划线、小写。
	p, err = m.Resolve(ctx, "u1", "MY WRITER")
	if err != nil {
		t.Fatalf("resolve by normalized name: %v", err)
	}
	if p.Name != "my_writer" {
		t.Fatalf("normalized name = %q, want my_writer", p.Name)
	}

	// 其他用户不受影响（回退到内置默认）。
	p, err = m.Resolve(ctx, "u2", "")
	if err != nil {
		t.Fatalf("resolve other user default: %v", err)
	}
	if p.Name != DefaultProfileName {
		t.Fatalf("other user default name = %q, want %q", p.Name, DefaultProfileName)
	}
}

func TestProfileManagerDeleteBuiltin(t *testing.T) {
	m := newTestProfileManager(t)
	ctx := context.Background()

	if err := m.Delete(ctx, "", BuiltinDefaultProfileID); err == nil {
		t.Fatalf("delete builtin should fail")
	}
}

func TestEnsureBuiltinProfilesCreateOnly(t *testing.T) {
	repo := NewMemoryProfileRepository()
	ctx := context.Background()

	// 首次同步：新增。
	if err := EnsureBuiltinProfiles(ctx, repo); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	builtins, err := repo.ListBuiltin(ctx)
	if err != nil {
		t.Fatalf("list builtin: %v", err)
	}
	if len(builtins) != len(BuiltinProfiles()) {
		t.Fatalf("builtin count = %d, want %d", len(builtins), len(BuiltinProfiles()))
	}

	// 修改内存中的一条内置记录，再同步：应保留改动（不再覆盖）。
	before, err := repo.Get(ctx, BuiltinDefaultProfileID)
	if err != nil {
		t.Fatalf("get default: %v", err)
	}
	before.SystemPrompt = "changed"
	if err := repo.Update(ctx, before); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := EnsureBuiltinProfiles(ctx, repo); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	after, err := repo.Get(ctx, BuiltinDefaultProfileID)
	if err != nil {
		t.Fatalf("get default after reseed: %v", err)
	}
	if after.SystemPrompt != "changed" {
		t.Fatalf("default system_prompt = %q, want preserved %q", after.SystemPrompt, "changed")
	}
}
