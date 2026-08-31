package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
)

func newGormTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(gormlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&Task{}, &PermissionRequest{}, &AgentEvent{}, &Conversation{}, &ConversationMessage{}, &Memory{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

func TestGormTaskRepositoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := NewGormTaskRepository(newGormTestDB(t))

	task := NewTask(Request{
		Provider:   ProviderMock,
		SessionID:  "sess-1",
		Messages:   []Message{{Role: RoleUser, Content: "hi"}},
		Env:        map[string]string{"K": "V"},
		WorkingDir: "/tmp",
	})
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != task.ID || got.Status != TaskCreated {
		t.Fatalf("unexpected task: %+v", got)
	}
	// 校验序列化字段往返：Request.Messages / Env 应被 JSON 序列化后正确还原。
	if len(got.Request.Messages) != 1 || got.Request.Messages[0].Content != "hi" {
		t.Fatalf("messages not preserved: %+v", got.Request.Messages)
	}
	if got.Request.Env["K"] != "V" {
		t.Fatalf("env not preserved: %+v", got.Request.Env)
	}

	// 状态迁移 + Update。
	got.TransitionTo(TaskRunning)
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	reloaded, _ := repo.Get(ctx, task.ID)
	if reloaded.Status != TaskRunning {
		t.Fatalf("status = %s, want running", reloaded.Status)
	}

	// ListByStatus / Page。
	list, err := repo.ListByStatus(ctx, TaskRunning)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListByStatus: len=%d err=%v", len(list), err)
	}
	page, total, err := repo.Page(ctx, 0, 10)
	if err != nil || total != 1 || len(page) != 1 {
		t.Fatalf("Page: total=%d len=%d err=%v", total, len(page), err)
	}
}

func TestGormPermissionRepositoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := NewGormPermissionRepository(newGormTestDB(t))

	p := NewPermissionRequest("task-1", "sess-1", Operation{
		Type:    OperationWrite,
		Path:    "/a/b",
		Content: "x",
		Metadata: map[string]any{
			"cmd": "echo hi",
		},
	})
	if err := repo.Create(ctx, p); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(ctx, p.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Operation.Type != OperationWrite || got.Operation.Path != "/a/b" {
		t.Fatalf("operation not preserved: %+v", got.Operation)
	}
	if got.Operation.Metadata["cmd"] != "echo hi" {
		t.Fatalf("metadata not preserved: %+v", got.Operation.Metadata)
	}

	pending, err := repo.ListPendingByTask(ctx, "task-1")
	if err != nil || len(pending) != 1 {
		t.Fatalf("ListPendingByTask: len=%d err=%v", len(pending), err)
	}

	// 批准并更新。
	got.TransitionTo(PermissionApproved)
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	pending, _ = repo.ListPendingByTask(ctx, "task-1")
	if len(pending) != 0 {
		t.Fatalf("expected no pending after approve, got %d", len(pending))
	}
}

func TestGormEventRepositorySequence(t *testing.T) {
	ctx := context.Background()
	repo := NewGormEventRepository(newGormTestDB(t))

	// 交错追加两个任务的事件，sequence 应按任务独立递增。
	for i := 0; i < 3; i++ {
		if err := repo.Append(ctx, &AgentEvent{ID: newID("evt"), TaskID: "task-A", Type: EventTaskStarted, CreatedAt: time.Now()}); err != nil {
			t.Fatalf("Append A%d: %v", i, err)
		}
		if err := repo.Append(ctx, &AgentEvent{ID: newID("evt"), TaskID: "task-B", Type: EventTaskStarted, CreatedAt: time.Now()}); err != nil {
			t.Fatalf("Append B%d: %v", i, err)
		}
	}

	evsA, err := repo.ListByTask(ctx, "task-A", 0)
	if err != nil {
		t.Fatalf("ListByTask A: %v", err)
	}
	evsB, err := repo.ListByTask(ctx, "task-B", 0)
	if err != nil {
		t.Fatalf("ListByTask B: %v", err)
	}
	if len(evsA) != 3 || len(evsB) != 3 {
		t.Fatalf("unexpected counts: A=%d B=%d", len(evsA), len(evsB))
	}
	for i, e := range evsA {
		if e.Sequence != int64(i+1) {
			t.Fatalf("task-A sequence[%d] = %d, want %d", i, e.Sequence, i+1)
		}
	}

	// after 语义：只返回 sequence > after。
	tail, err := repo.ListByTask(ctx, "task-A", 1)
	if err != nil || len(tail) != 2 || tail[0].Sequence != 2 {
		t.Fatalf("ListByTask after=1: len=%d err=%v firstSeq=%d", len(tail), err, tail[0].Sequence)
	}

	// Page 全量。
	page, total, err := repo.Page(ctx, "", 0, 100)
	if err != nil || total != 6 || len(page) != 6 {
		t.Fatalf("Page all: total=%d len=%d err=%v", total, len(page), err)
	}
}

func TestGormConversationRepositoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := NewGormConversationRepository(newGormTestDB(t))

	conv := NewConversation("user-1", ProviderMock, "model-x")
	conv.Messages = []Message{
		{Role: RoleUser, Content: "你好"},
		{Role: RoleAssistant, Content: "你好，有什么可以帮你？"},
	}
	if err := repo.Create(ctx, conv); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(ctx, conv.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Messages) != 2 || got.Messages[0].Role != RoleUser || got.Messages[0].Content != "你好" {
		t.Fatalf("messages not preserved: %+v", got.Messages)
	}
	if got.Messages[1].Role != RoleAssistant {
		t.Fatalf("assistant role not preserved: %+v", got.Messages[1])
	}

	// 追加消息后 Update，应正确重建消息列表。
	got.Messages = append(got.Messages, Message{Role: RoleUser, Content: "第二句"})
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	reloaded, err := repo.Get(ctx, conv.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if len(reloaded.Messages) != 3 || reloaded.Messages[2].Content != "第二句" {
		t.Fatalf("updated messages mismatch: %+v", reloaded.Messages)
	}

	// 不存在返回 ErrConversationNotFound。
	if _, err := repo.Get(ctx, "conv_missing"); !errors.Is(err, ErrConversationNotFound) {
		t.Fatalf("expected ErrConversationNotFound, got %v", err)
	}

	// Page 按 user 过滤 + 降序。
	conv2 := NewConversation("user-2", ProviderMock, "model-x")
	conv2.Messages = []Message{{Role: RoleUser, Content: "hi"}}
	if err := repo.Create(ctx, conv2); err != nil {
		t.Fatalf("Create conv2: %v", err)
	}
	page, total, err := repo.Page(ctx, "user-1", 0, 10)
	if err != nil || total != 1 || len(page) != 1 {
		t.Fatalf("Page user-1: total=%d len=%d err=%v", total, len(page), err)
	}
	if len(page[0].Messages) != 3 {
		t.Fatalf("page[0] messages not loaded: %+v", page[0].Messages)
	}
}
