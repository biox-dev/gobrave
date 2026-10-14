package agent_test

import (
	"context"
	"testing"

	// 使用点导入以复用 agent 包中大量领域标识符（与合并前的包内测试保持一致）。
	. "github.com/biox-dev/gobrave/internal/agent"
	agentrepository "github.com/biox-dev/gobrave/internal/agent/repository"
	"github.com/biox-dev/gobrave/internal/utils"
)

// newTestProfileManager 创建一个已同步内置 Profile 的 GORM Profile 管理器（内存 sqlite）。
func newTestProfileManager(t *testing.T) *ProfileManager {
	t.Helper()
	repo := agentrepository.NewGormProfileRepository(newTestDB(t))
	if err := EnsureBuiltinProfiles(context.Background(), repo); err != nil {
		t.Fatalf("seed builtin profiles: %v", err)
	}
	return NewProfileManager(repo)
}

func TestProfileManagerResolveBuiltin(t *testing.T) {
	m := newTestProfileManager(t)
	ctx := context.Background()

	// ID 为 0 → 内置默认。
	p, err := m.Resolve(ctx, 0)
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if p.ID != BuiltinDefaultProfileID {
		t.Fatalf("default profile id = %d, want %d", p.ID, BuiltinDefaultProfileID)
	}
	if !p.Context.InjectMemory {
		t.Fatalf("default context = %+v, want memory on", p.Context)
	}

	// 指定内置 ID → 命中内置 Profile。
	p, err = m.Resolve(ctx, BuiltinArticleWriterID)
	if err != nil {
		t.Fatalf("resolve article_writer: %v", err)
	}
	if p.Name != "科研文章撰写" {
		t.Fatalf("article_writer name = %q", p.Name)
	}
}

func TestProfileManagerUserDefault(t *testing.T) {
	_ = utils.InitSnowflake(1)
	m := newTestProfileManager(t)
	ctx := context.Background()

	custom := &Profile{
		Name:         "我的写作助手",
		UserID:       "u1",
		IsDefault:    true,
		SystemPrompt: "写作",
		Context:      ContextConfig{InjectMemory: true},
	}
	if err := m.Save(ctx, custom); err != nil {
		t.Fatalf("save custom: %v", err)
	}

	// 保存后按 ID 解析。
	p, err := m.Resolve(ctx, custom.ID)
	if err != nil {
		t.Fatalf("resolve custom: %v", err)
	}
	if p.Name != "我的写作助手" {
		t.Fatalf("custom name = %q, want 我的写作助手", p.Name)
	}

	// 未知 ID → ErrProfileNotFound。
	if _, err := m.Resolve(ctx, custom.ID+1); err == nil {
		t.Fatalf("resolve unknown id should fail")
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
	repo := agentrepository.NewGormProfileRepository(newTestDB(t))
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

	// 修改数据库中的一条内置记录，再同步：应保留改动（不再覆盖）。
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
