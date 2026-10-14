package agent_test

import (
	"context"
	"strings"
	"testing"

	// 使用点导入以复用 agent 包中大量领域标识符（与合并前的包内测试保持一致）。
	. "github.com/biox-dev/gobrave/internal/agent"
	agentrepository "github.com/biox-dev/gobrave/internal/agent/repository"
)

// newTestMemoryManager 构建一个使用 GORM 记忆仓库（内存 sqlite）的记忆管理器。
func newTestMemoryManager(t *testing.T, cfg MemoryConfig) *MemoryManager {
	t.Helper()
	if cfg.Repo == nil {
		cfg.Repo = agentrepository.NewGormMemoryRepository(newTestDB(t))
	}
	return NewMemoryManager(cfg)
}

// TestMemoryManagerSearch 校验检索器返回的记忆。
func TestMemoryManagerSearch(t *testing.T) {
	ctx := context.Background()
	mgr := newTestMemoryManager(t, MemoryConfig{})

	save := func(kind MemoryKind, content string, importance int) *Memory {
		m := NewMemory(1, "", kind, content)
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

	hits, err := mgr.Retrieve(ctx, 1, "Go", 10)
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
	mgr := newTestMemoryManager(t, MemoryConfig{})
	m := NewMemory(1, "", MemoryKindFact, "用户偏爱简洁的 Go 答案")
	if err := mgr.Save(ctx, m); err != nil {
		t.Fatalf("Save: %v", err)
	}

	svc := NewService(ServiceConfig{Memory: mgr})

	req := Request{
		UserID:       1,
		SystemPrompt: "你是助手",
		Messages:     []Message{{Role: RoleUser, Content: "请用 Go 简洁地介绍"}},
	}

	out := svc.InjectMemoryForTest(ctx, req.SystemPrompt, req.UserID)
	if !strings.Contains(out, "相关记忆") || !strings.Contains(out, "偏爱简洁") {
		t.Fatalf("memory not injected into SystemPrompt:\n%s", out)
	}

	// 无 UserID 时不应注入。
	if got := svc.InjectMemoryForTest(ctx, "你是助手", 0); got != "你是助手" {
		t.Fatalf("expected no injection without userID, got %q", got)
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

	mgr := newTestMemoryManager(t, MemoryConfig{Extractor: extractor})
	svc := NewService(ServiceConfig{Memory: mgr})

	task := NewTask(Request{UserID: 1, SessionID: "sess-1"})
	req := task.Request
	svc.ExtractMemoryForTest(ctx, task, req, &Result{Content: "done"})

	mems, _, err := mgr.List(ctx, 1, 0, 10)
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
	mgr := newTestMemoryManager(t, MemoryConfig{Extractor: MockMemoryExtractor()})

	turn := MemoryTurn{
		UserID:    1,
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
	if m.Kind != MemoryKindSummary || m.UserID != 1 || m.SessionID != "sess-1" {
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
