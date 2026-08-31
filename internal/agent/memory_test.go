package agent

import (
	"context"
	"strings"
	"testing"
)

// TestGormMemoryRepositoryRoundTrip 校验 GORM 记忆仓库的往返（含 Metadata 序列化）。
func TestGormMemoryRepositoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newGormTestDB(t)
	repo := NewGormMemoryRepository(db)

	m := NewMemory("user-1", "sess-1", MemoryKindFact, "用户喜欢用 Go")
	m.Importance = 8
	m.Metadata = map[string]any{"source": "extractor", "n": 42}

	if err := repo.Create(ctx, m); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(ctx, m.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != m.ID || got.Content != m.Content || got.Importance != 8 {
		t.Fatalf("unexpected memory: %+v", got)
	}
	if got.Metadata == nil || got.Metadata["source"] != "extractor" {
		t.Fatalf("metadata not round-tripped: %+v", got.Metadata)
	}

	// 检索命中。
	hits, err := repo.Search(ctx, "user-1", "Go", 10)
	if err != nil || len(hits) != 1 || hits[0].ID != m.ID {
		t.Fatalf("Search = %d (err=%v), want 1", len(hits), err)
	}

	// 删除。
	if err := repo.Delete(ctx, m.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(ctx, m.ID); err != ErrMemoryNotFound {
		t.Fatalf("expected ErrMemoryNotFound after delete, got %v", err)
	}
}

// TestMemoryManagerSearch 校验内存实现的检索排序（相关度 + 重要度）。
func TestMemoryManagerSearch(t *testing.T) {
	ctx := context.Background()
	mgr := NewMemoryManager(MemoryConfig{})

	save := func(kind MemoryKind, content string, importance int) *Memory {
		m := NewMemory("user-1", "", kind, content)
		m.Importance = importance
		if err := mgr.Save(ctx, m); err != nil {
			t.Fatalf("Save: %v", err)
		}
		return m
	}

	a := save(MemoryKindFact, "用户是 Go 开发者，喜欢 Go", 5)
	_ = a
	b := save(MemoryKindNote, "项目使用 PostgreSQL", 9)
	_ = b
	save(MemoryKindEvent, "上周发布了 v1.0", 3)

	hits, err := mgr.Retrieve(ctx, "user-1", "Go", 10)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	// "Go" 命中两条（a 两次、b 一次未命中），按得分排序：a（2 次命中）> b（未命中）。
	if len(hits) != 1 {
		t.Fatalf("Retrieve = %d hits, want 1", len(hits))
	}
	if hits[0].ID != a.ID {
		t.Fatalf("top hit = %s, want %s", hits[0].ID, a.ID)
	}
}

// TestInjectMemory 校验调用前把相关记忆注入 SystemPrompt。
func TestInjectMemory(t *testing.T) {
	ctx := context.Background()
	mgr := NewMemoryManager(MemoryConfig{})
	m := NewMemory("user-1", "", MemoryKindFact, "用户偏爱简洁的 Go 答案")
	if err := mgr.Save(ctx, m); err != nil {
		t.Fatalf("Save: %v", err)
	}

	svc := NewService(ServiceConfig{Memory: mgr})

	req := Request{
		UserID:       "user-1",
		SystemPrompt: "你是助手",
		Messages:     []Message{{Role: RoleUser, Content: "请用 Go 简洁地介绍"}},
	}

	out := svc.injectMemory(ctx, req)
	if !strings.Contains(out.SystemPrompt, "相关记忆") || !strings.Contains(out.SystemPrompt, "偏爱简洁") {
		t.Fatalf("memory not injected into SystemPrompt:\n%s", out.SystemPrompt)
	}

	// 无 UserID 时不应注入。
	noUser := Request{Messages: []Message{{Role: RoleUser, Content: "hi"}}}
	if got := svc.injectMemory(ctx, noUser); got.SystemPrompt != "" {
		t.Fatalf("expected no injection without UserID, got %q", got.SystemPrompt)
	}
}

// TestExtractMemory 校验任务完成后记忆提取器被调用并写入。
func TestExtractMemory(t *testing.T) {
	ctx := context.Background()

	extractor := MemoryExtractorFunc(func(_ context.Context, turn MemoryTurn) ([]*Memory, error) {
		return []*Memory{
			NewMemory(turn.UserID, turn.SessionID, MemoryKindSummary, "本轮讨论了部署"),
		}, nil
	})

	mgr := NewMemoryManager(MemoryConfig{Extractor: extractor})
	svc := NewService(ServiceConfig{Memory: mgr})

	task := NewTask(Request{UserID: "user-1", SessionID: "sess-1"})
	req := task.Request
	svc.extractMemory(ctx, task, req, &Result{Content: "done"})

	mems, _, err := mgr.List(ctx, "user-1", 0, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(mems) != 1 || mems[0].Kind != MemoryKindSummary {
		t.Fatalf("expected 1 extracted summary memory, got %d", len(mems))
	}
}

// TestMockMemoryExtractor 校验模拟提取器能把一轮执行摘要为一条 summary 记忆。
func TestMockMemoryExtractor(t *testing.T) {
	ctx := context.Background()
	mgr := NewMemoryManager(MemoryConfig{Extractor: MockMemoryExtractor()})

	turn := MemoryTurn{
		UserID:    "user-1",
		SessionID: "sess-1",
		Request:   Request{Messages: []Message{{Role: RoleUser, Content: "介绍一下 Go"}}},
		Result:    &Result{Content: "Go 是一门编译型语言"},
	}

	mems, err := mgr.Extract(ctx, turn)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("Extract = %d memories, want 1", len(mems))
	}
	m := mems[0]
	if m.Kind != MemoryKindSummary || m.UserID != "user-1" || m.SessionID != "sess-1" {
		t.Fatalf("unexpected memory: %+v", m)
	}
	if !strings.Contains(m.Content, "介绍一下 Go") || !strings.Contains(m.Content, "编译型语言") {
		t.Fatalf("unexpected content: %q", m.Content)
	}

	// 空结果时不提取。
	if mems, err = mgr.Extract(ctx, MemoryTurn{Result: nil}); err != nil || len(mems) != 0 {
		t.Fatalf("expected no extraction for empty result, got %d (err=%v)", len(mems), err)
	}
}
