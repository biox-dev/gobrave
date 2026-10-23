package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
)

// newGormTestDB 打开一个共享内存的 sqlite 数据库并迁移领域表，供仓库往返测试使用。
func newGormTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	_ = utils.InitSnowflake(1)
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(gormlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&agenttypes.Task{}, &agenttypes.PermissionRequest{}, &agenttypes.QuestionRequest{}, &agenttypes.Session{}, &agenttypes.Message{}, &agenttypes.Memory{}, &agenttypes.File{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

func TestGormTaskRepositoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := NewGormTaskRepository(newGormTestDB(t))

	task := agenttypes.NewTask(agenttypes.Request{
		Provider:   agenttypes.ProviderMock,
		Messages:   []agenttypes.Message{{Role: agenttypes.RoleUser, Content: "hi"}},
		Env:        map[string]string{"K": "V"},
		WorkingDir: "/tmp",
	}, agenttypes.TaskFacts{SessionID: 1001})
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != task.ID || got.Status != agenttypes.TaskCreated {
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
	got.TransitionTo(agenttypes.TaskRunning)
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	reloaded, _ := repo.Get(ctx, task.ID)
	if reloaded.Status != agenttypes.TaskRunning {
		t.Fatalf("status = %s, want running", reloaded.Status)
	}

	// ListByStatus / Page。
	list, err := repo.ListByStatus(ctx, agenttypes.TaskRunning)
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

	p := agenttypes.NewPermissionRequest(1, "sess-1", agenttypes.Operation{
		Type:    agenttypes.OperationWrite,
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
	if got.Operation.Type != agenttypes.OperationWrite || got.Operation.Path != "/a/b" {
		t.Fatalf("operation not preserved: %+v", got.Operation)
	}
	if got.Operation.Metadata["cmd"] != "echo hi" {
		t.Fatalf("metadata not preserved: %+v", got.Operation.Metadata)
	}

	pending, err := repo.ListPendingByTask(ctx, 1)
	if err != nil || len(pending) != 1 {
		t.Fatalf("ListPendingByTask: len=%d err=%v", len(pending), err)
	}

	// 批准并更新。
	got.TransitionTo(agenttypes.PermissionApproved)
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update: %v", err)
	}
	pending, _ = repo.ListPendingByTask(ctx, 1)
	if len(pending) != 0 {
		t.Fatalf("expected no pending after approve, got %d", len(pending))
	}
}

func TestGormSessionAndMessageStores(t *testing.T) {
	ctx := context.Background()
	db := newGormTestDB(t)
	sessions := agentservice.NewSessionService(NewGormSessionStore(db))
	messages := agentservice.NewMessageService(NewGormMessageStore(db))

	sess, err := sessions.Create(ctx, agenttypes.SessionCreateParams{UserID: 1, Provider: agenttypes.ProviderMock, Model: "model-x"})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	// 消息按会话自动分配 Seq。
	if _, err := messages.Create(ctx, agenttypes.MessageCreateParams{SessionID: sess.ID, Role: agenttypes.RoleUser, Kind: agenttypes.KindUser, Content: "你好"}); err != nil {
		t.Fatalf("Create user message: %v", err)
	}
	if _, err := messages.Create(ctx, agenttypes.MessageCreateParams{SessionID: sess.ID, Role: agenttypes.RoleAssistant, Kind: agenttypes.KindAssistantFinal, Content: "你好，有什么可以帮你？"}); err != nil {
		t.Fatalf("Create assistant message: %v", err)
	}

	msgs, err := messages.List(ctx, sess.ID)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("List messages: len=%d err=%v", len(msgs), err)
	}
	if msgs[0].Seq != 0 || msgs[1].Seq != 1 || msgs[0].Content != "你好" {
		t.Fatalf("messages not preserved: %+v", msgs)
	}

	// 子会话：task session 以 toolCallID 作为 ID，title session 关联父会话。
	taskSess, err := sessions.CreateTaskSession(ctx, 7777777, sess.ID, "task session")
	if err != nil {
		t.Fatalf("CreateTaskSession: %v", err)
	}
	if taskSess.ID != 7777777 || taskSess.ParentSessionID != sess.ID || taskSess.Title != "task session" {
		t.Fatalf("unexpected task session: %+v", taskSess)
	}
	titleSess, err := sessions.CreateTitleSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("CreateTitleSession: %v", err)
	}
	if titleSess.ParentSessionID != sess.ID || titleSess.Title != agenttypes.TitleSessionTitle || titleSess.ID == 0 {
		t.Fatalf("unexpected title session: %+v", titleSess)
	}
	// 子会话（带 parent_session_id）不进入会话列表。
	childSess, err := sessions.CreateChildSession(ctx, agenttypes.SessionChildParams{
		ParentSessionID: sess.ID, Title: "sub agent", UserID: 1, Provider: agenttypes.ProviderMock, Model: "model-x",
	})
	if err != nil || childSess.ParentSessionID != sess.ID || childSess.ID == 0 || childSess.Title != "sub agent" {
		t.Fatalf("CreateChildSession: %+v err=%v", childSess, err)
	}
	if _, err := sessions.CreateChildSession(ctx, agenttypes.SessionChildParams{Title: "orphan"}); !errors.Is(err, agenttypes.ErrInvalidParentSession) {
		t.Fatalf("CreateChildSession without parent: err=%v, want ErrInvalidParentSession", err)
	}

	// 更新会话当前轮次任务。
	sess.CurrentTaskID = 12345
	if _, err := sessions.Save(ctx, sess); err != nil {
		t.Fatalf("Save session: %v", err)
	}
	got, err := sessions.Get(ctx, sess.ID)
	if err != nil || got.CurrentTaskID != 12345 {
		t.Fatalf("session not updated: %+v err=%v", got, err)
	}

	// 不存在返回 agenttypes.ErrSessionNotFound。
	if _, err := sessions.Get(ctx, 0); !errors.Is(err, agenttypes.ErrSessionNotFound) {
		t.Fatalf("expected agenttypes.ErrSessionNotFound, got %v", err)
	}

	// List 按 user 过滤。
	if _, err := sessions.Create(ctx, agenttypes.SessionCreateParams{UserID: 2, Provider: agenttypes.ProviderMock, Model: "model-x"}); err != nil {
		t.Fatalf("Create session 2: %v", err)
	}
	page, total, err := sessions.List(ctx, agenttypes.SessionListParams{UserID: 1, Offset: 0, Limit: 10})
	if err != nil || total != 1 || len(page) != 1 {
		t.Fatalf("List user-1: total=%d len=%d err=%v", total, len(page), err)
	}

	// 删除会话应级联清理消息。
	if err := sessions.Delete(ctx, sess.ID); err != nil {
		t.Fatalf("Delete session: %v", err)
	}
	if msgs, err := messages.List(ctx, sess.ID); err != nil || len(msgs) != 0 {
		t.Fatalf("messages not cascaded: len=%d err=%v", len(msgs), err)
	}
}

// TestGormMemoryRepositoryRoundTrip 校验 GORM 记忆仓库的往返（含 Metadata 序列化）。
func TestGormMemoryRepositoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newGormTestDB(t)
	repo := NewGormMemoryRepository(db)

	m := agenttypes.NewMemory(1, 1002, 1001, agenttypes.MemoryKindFact, "用户喜欢用 Go")
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
	hits, err := repo.Search(ctx, 1, "Go", 10)
	if err != nil || len(hits) != 1 || hits[0].ID != m.ID {
		t.Fatalf("Search = %d (err=%v), want 1", len(hits), err)
	}

	// 删除。
	if err := repo.Delete(ctx, m.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.Get(ctx, m.ID); err != agenttypes.ErrMemoryNotFound {
		t.Fatalf("expected ErrMemoryNotFound after delete, got %v", err)
	}
}
