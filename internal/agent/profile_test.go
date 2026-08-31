package agent

import (
	"context"
	"testing"
)

func TestProfileManagerResolveBuiltin(t *testing.T) {
	m := NewProfileManager(nil)
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
	m := NewProfileManager(nil)
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

	// 其他用户不受影响。
	if _, err := m.Resolve(ctx, "u2", ""); err != nil {
		t.Fatalf("resolve other user default: %v", err)
	}
}

func TestProfileManagerDeleteBuiltin(t *testing.T) {
	m := NewProfileManager(nil)
	ctx := context.Background()

	if err := m.Delete(ctx, "", "builtin_default"); err == nil {
		t.Fatalf("delete builtin should fail")
	}
}
