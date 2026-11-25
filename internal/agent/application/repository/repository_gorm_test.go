package repository

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

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
	if err := db.AutoMigrate(&agenttypes.Task{}, &agenttypes.PermissionRequest{}, &agenttypes.QuestionRequest{}, &agenttypes.Session{}, &agenttypes.Message{}, &agenttypes.SessionSummary{}, &agenttypes.Memory{}, &agenttypes.File{}, &agenttypes.SessionResource{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// TestGormTaskRepositoryKeepsMessagesWithoutSession 验证无会话的一次性任务仍然
// 携带输入消息：它们没有 agent_messages 可回放，只能靠 Request 保存。
func TestGormTaskRepositoryKeepsMessagesWithoutSession(t *testing.T) {
	ctx := context.Background()
	repo := NewGormTaskRepository(newGormTestDB(t))

	task := agenttypes.NewTask(agenttypes.Request{
		Provider: agenttypes.ProviderMock,
		Messages: []agenttypes.Message{{Role: agenttypes.RoleUser, Content: "hi"}},
	}, agenttypes.TaskFacts{})
	if err := repo.Create(ctx, task); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(got.Request.Messages) != 1 || got.Request.Messages[0].Content != "hi" {
		t.Fatalf("messages not preserved: %+v", got.Request.Messages)
	}
}

func TestGormTaskRepositoryRoundTrip(t *testing.T) {
	ctx := context.Background()
	repo := NewGormTaskRepository(newGormTestDB(t))

	task := agenttypes.NewTask(agenttypes.Request{
		Provider: agenttypes.ProviderMock,
		Messages: []agenttypes.Message{{Role: agenttypes.RoleUser, Content: "hi"}},
		Env:      map[string]string{"K": "V"},
	}, agenttypes.TaskFacts{SessionID: 1001, WorkingDir: "/tmp"})
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
	// 会话轮次的历史只存在 agent_messages，不随 Request 持久化（否则任务行会随对话膨胀）。
	if len(got.Request.Messages) != 0 {
		t.Fatalf("session task must not persist messages: %+v", got.Request.Messages)
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

func TestGormMessageStoreListPage(t *testing.T) {
	ctx := context.Background()
	db := newGormTestDB(t)
	sessions := agentservice.NewSessionService(NewGormSessionStore(db))
	messages := agentservice.NewMessageService(NewGormMessageStore(db))

	sess, err := sessions.Create(ctx, agenttypes.SessionCreateParams{UserID: 1, Provider: agenttypes.ProviderMock})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := messages.Create(ctx, agenttypes.MessageCreateParams{
			SessionID: sess.ID,
			Role:      agenttypes.RoleUser,
			Kind:      agenttypes.KindUser,
			Content:   fmt.Sprintf("m%d", i),
		}); err != nil {
			t.Fatalf("Create message %d: %v", i, err)
		}
	}

	// 最新一页：按 Seq 升序返回，hasMore 表示其之前仍有消息。
	page, hasMore, err := messages.ListPage(ctx, sess.ID, 0, 2)
	if err != nil {
		t.Fatalf("ListPage: %v", err)
	}
	if !hasMore || len(page) != 2 || page[0].Seq != 3 || page[1].Seq != 4 {
		t.Fatalf("first page = %+v hasMore=%v, want seq [3 4] and hasMore", page, hasMore)
	}

	// 向上翻页：以本页最小 Seq 为游标继续取更早的消息。
	older, hasMore, err := messages.ListPage(ctx, sess.ID, page[0].Seq, 2)
	if err != nil {
		t.Fatalf("ListPage older: %v", err)
	}
	if !hasMore || len(older) != 2 || older[0].Seq != 1 || older[1].Seq != 2 {
		t.Fatalf("older page = %+v hasMore=%v, want seq [1 2] and hasMore", older, hasMore)
	}

	// 最后一页：hasMore 为 false。
	last, hasMore, err := messages.ListPage(ctx, sess.ID, older[0].Seq, 2)
	if err != nil {
		t.Fatalf("ListPage last: %v", err)
	}
	if hasMore || len(last) != 1 || last[0].Seq != 0 {
		t.Fatalf("last page = %+v hasMore=%v, want seq [0] and no more", last, hasMore)
	}
}

// TestGormSessionStoreFork 校验 fork 会话：消息复制、Seq 重排、错误消息不带入、
// 消息 TaskID 保留、文件历史只复制最新版本、归属校验，以及源会话不受影响。
func TestGormSessionStoreFork(t *testing.T) {
	ctx := context.Background()
	db := newGormTestDB(t)
	sessions := agentservice.NewSessionService(NewGormSessionStore(db))
	messages := agentservice.NewMessageService(NewGormMessageStore(db))
	files := agentservice.NewHistoryService(NewGormHistoryStore(db))

	src, err := sessions.Create(ctx, agenttypes.SessionCreateParams{
		UserID: 1, Title: "origin", Provider: agenttypes.ProviderMock, Model: "m",
	})
	if err != nil {
		t.Fatalf("create source session: %v", err)
	}
	for _, in := range []agenttypes.MessageCreateParams{
		{SessionID: src.ID, Role: agenttypes.RoleUser, Kind: agenttypes.KindUser, Content: "q1"},
		{SessionID: src.ID, Role: agenttypes.RoleAssistant, Kind: agenttypes.KindAssistant, Content: "a1", TaskID: 99},
		{SessionID: src.ID, Role: agenttypes.RoleAssistant, Kind: agenttypes.KindError, Content: "boom", TaskID: 99},
	} {
		if _, err := messages.Create(ctx, in); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}
	// 同一文件两个版本：fork 只应复制最新版本（v1 的内容）。
	// 直接用 Store 写入，避免 HistoryService 的版本推导干扰断言。
	fileStore := NewGormHistoryStore(db)
	for _, in := range []struct{ content, version string }{
		{"v0", agenttypes.InitialVersion},
		{"v1", "v1"},
	} {
		if err := fileStore.Create(ctx, &agenttypes.File{
			SessionID: src.ID, Path: "/a.txt", Content: in.content, Version: in.version,
		}); err != nil {
			t.Fatalf("create file history %s: %v", in.version, err)
		}
	}

	forked, err := sessions.Fork(ctx, agenttypes.SessionForkParams{
		SourceSessionID: src.ID, UserID: 1, CopyFiles: true,
	})
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if forked.ID == src.ID || forked.ID == 0 {
		t.Fatalf("forked session should have a new id, got %d", forked.ID)
	}
	if forked.ParentSessionID != 0 || forked.CurrentTaskID != 0 || forked.ProjectID != src.ProjectID {
		t.Fatalf("unexpected forked session: %+v", forked)
	}
	if forked.Title != "origin (fork)" {
		t.Fatalf("title = %q, want %q", forked.Title, "origin (fork)")
	}

	copied, err := messages.List(ctx, forked.ID)
	if err != nil {
		t.Fatalf("list forked messages: %v", err)
	}
	// error 消息被跳过，剩下 2 条且 Seq 从 0 重新排。
	if len(copied) != 2 {
		t.Fatalf("forked messages = %d, want 2", len(copied))
	}
	if copied[0].Seq != 0 || copied[1].Seq != 1 || copied[0].Content != "q1" {
		t.Fatalf("unexpected forked messages: %+v", copied)
	}
	if copied[1].TaskID != 99 {
		t.Fatalf("task id should be preserved on fork, got %d", copied[1].TaskID)
	}
	if copied[0].ID == 0 || copied[0].ID == copied[1].ID {
		t.Fatalf("copied messages should get new ids")
	}

	// 文件历史独立：新会话只有一条 initial 版本，内容为源会话最新版本。
	forkedFiles, err := files.ListLatestSessionFiles(ctx, forked.ID)
	if err != nil {
		t.Fatalf("list forked files: %v", err)
	}
	if len(forkedFiles) != 1 || forkedFiles[0].Content != "v1" || forkedFiles[0].Version != agenttypes.InitialVersion {
		t.Fatalf("unexpected forked files: %+v", forkedFiles)
	}

	// 源会话保持不变。
	srcMsgs, err := messages.List(ctx, src.ID)
	if err != nil || len(srcMsgs) != 3 {
		t.Fatalf("source messages changed: len=%d err=%v", len(srcMsgs), err)
	}

	// 归属校验：他人 fork 视为不存在。
	if _, err := sessions.Fork(ctx, agenttypes.SessionForkParams{SourceSessionID: src.ID, UserID: 2}); !errors.Is(err, agenttypes.ErrSessionNotFound) {
		t.Fatalf("fork by other user: err=%v, want ErrSessionNotFound", err)
	}
	// 不存在的源会话。
	if _, err := sessions.Fork(ctx, agenttypes.SessionForkParams{SourceSessionID: 123, UserID: 1}); !errors.Is(err, agenttypes.ErrSessionNotFound) {
		t.Fatalf("fork missing session: err=%v, want ErrSessionNotFound", err)
	}

	// UpToSeq：只复制截止到该 seq（含）的历史。源会话 seq 0=q1, 1=a1。
	upTo := 0
	partial, err := sessions.Fork(ctx, agenttypes.SessionForkParams{
		SourceSessionID: src.ID, UserID: 1, UpToSeq: &upTo,
	})
	if err != nil {
		t.Fatalf("fork up_to_seq: %v", err)
	}
	partialMsgs, err := messages.List(ctx, partial.ID)
	if err != nil {
		t.Fatalf("list partial messages: %v", err)
	}
	if len(partialMsgs) != 1 || partialMsgs[0].Content != "q1" || partialMsgs[0].Seq != 0 {
		t.Fatalf("up_to_seq=0 should copy only first message, got %+v", partialMsgs)
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
	if _, err := messages.Create(ctx, agenttypes.MessageCreateParams{SessionID: sess.ID, Role: agenttypes.RoleAssistant, Kind: agenttypes.KindAssistant, Content: "你好，有什么可以帮你？"}); err != nil {
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

	// 关联数据：任务 / 权限 / 提问 / 会话资源关联 / 记忆 / 文件历史（含子会话消息）。
	task := agenttypes.NewTask(agenttypes.Request{Provider: agenttypes.ProviderMock}, agenttypes.TaskFacts{SessionID: sess.ID})
	if err := db.Create(task).Error; err != nil {
		t.Fatalf("Create task: %v", err)
	}
	sessionRef := strconv.FormatInt(sess.ID, 10)
	perm := agenttypes.NewPermissionRequest(task.ID, sessionRef, agenttypes.Operation{Type: agenttypes.OperationWrite, Path: "/a/b"})
	if err := db.Create(perm).Error; err != nil {
		t.Fatalf("Create permission: %v", err)
	}
	question := agenttypes.NewQuestionRequest(task.ID, sessionRef, agenttypes.AskUserInput{
		Title:     "confirm",
		Questions: []agenttypes.Question{{ID: "q1", Type: agenttypes.QuestionTypeText, Title: "ok?"}},
	})
	if err := db.Create(question).Error; err != nil {
		t.Fatalf("Create question: %v", err)
	}
	link := &agenttypes.SessionResource{SessionID: sess.ID, ResourceType: agenttypes.SessionResourceTypeScript, ResourceID: 424242}
	if err := db.Create(link).Error; err != nil {
		t.Fatalf("Create session resource: %v", err)
	}
	memory := agenttypes.NewMemory(1, 0, sess.ID, agenttypes.MemoryKindSummary, "session summary")
	if err := db.Create(memory).Error; err != nil {
		t.Fatalf("Create memory: %v", err)
	}
	now := time.Now()
	file := &agenttypes.File{SessionID: sess.ID, Path: "/tmp/a.txt", Content: "x", Version: agenttypes.InitialVersion, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(file).Error; err != nil {
		t.Fatalf("Create history file: %v", err)
	}
	if _, err := messages.Create(ctx, agenttypes.MessageCreateParams{SessionID: childSess.ID, Role: agenttypes.RoleUser, Kind: agenttypes.KindUser, Content: "child"}); err != nil {
		t.Fatalf("Create child message: %v", err)
	}

	// 删除会话应级联清理消息、任务、权限、提问、资源关联、记忆、文件历史与子会话。
	if err := sessions.Delete(ctx, sess.ID); err != nil {
		t.Fatalf("Delete session: %v", err)
	}
	if msgs, err := messages.List(ctx, sess.ID); err != nil || len(msgs) != 0 {
		t.Fatalf("messages not cascaded: len=%d err=%v", len(msgs), err)
	}
	if msgs, err := messages.List(ctx, childSess.ID); err != nil || len(msgs) != 0 {
		t.Fatalf("child messages not cascaded: len=%d err=%v", len(msgs), err)
	}
	if _, err := sessions.Get(ctx, taskSess.ID); !errors.Is(err, agenttypes.ErrSessionNotFound) {
		t.Fatalf("child session not cascaded: err=%v", err)
	}

	assertCount := func(model any, where string, args ...any) {
		t.Helper()
		var n int64
		if err := db.Model(model).Where(where, args...).Count(&n).Error; err != nil {
			t.Fatalf("count %T: %v", model, err)
		}
		if n != 0 {
			t.Fatalf("%T still has %d row(s) after session delete", model, n)
		}
	}
	assertCount(&agenttypes.Task{}, "session_id = ?", sess.ID)
	assertCount(&agenttypes.PermissionRequest{}, "task_id = ?", task.ID)
	assertCount(&agenttypes.QuestionRequest{}, "task_id = ?", task.ID)
	assertCount(&agenttypes.SessionResource{}, "session_id = ?", sess.ID)
	assertCount(&agenttypes.Memory{}, "session_id = ?", sess.ID)
	assertCount(&agenttypes.File{}, "session_id = ?", sess.ID)
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

// TestGormMessageStoreTaskScoped 校验按任务读写消息：user 消息带 TaskID 后
// 可以被精确列出与删除，且不会影响同一会话中的其它轮次。
func TestGormMessageStoreTaskScoped(t *testing.T) {
	ctx := context.Background()
	db := newGormTestDB(t)
	messages := agentservice.NewMessageService(NewGormMessageStore(db))

	for _, in := range []agenttypes.MessageCreateParams{
		{SessionID: 10, Role: agenttypes.RoleUser, Kind: agenttypes.KindUser, Content: "q1", TaskID: 100},
		{SessionID: 10, Role: agenttypes.RoleAssistant, Kind: agenttypes.KindAssistant, Content: "a1", TaskID: 100},
		{SessionID: 10, Role: agenttypes.RoleUser, Kind: agenttypes.KindUser, Content: "q2", TaskID: 200},
		{SessionID: 10, Role: agenttypes.RoleAssistant, Kind: agenttypes.KindAssistant, Content: "a2", TaskID: 200},
		{SessionID: 10, Role: agenttypes.RoleAssistant, Kind: agenttypes.KindAssistant, Content: "no-task"},
	} {
		if _, err := messages.Create(ctx, in); err != nil {
			t.Fatalf("create message: %v", err)
		}
	}

	byTask, err := messages.ListByTask(ctx, 100)
	if err != nil {
		t.Fatalf("ListByTask: %v", err)
	}
	if len(byTask) != 2 || byTask[0].Content != "q1" || byTask[1].Content != "a1" {
		t.Fatalf("ListByTask = %+v, want [q1 a1]", byTask)
	}

	if err := messages.DeleteByTask(ctx, 100); err != nil {
		t.Fatalf("DeleteByTask: %v", err)
	}
	rest, err := messages.List(ctx, 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// 仅删除任务 100 的两条消息，任务 200 与无任务的消息保留。
	if len(rest) != 3 {
		t.Fatalf("remaining messages = %d, want 3", len(rest))
	}
	for _, m := range rest {
		if m.TaskID == 100 {
			t.Fatalf("task 100 message should be deleted: %+v", m)
		}
	}
}

// TestGormSessionStoreForkWithMessages 校验「从某一轮 fork」的原子写入：
// 新会话的历史由回调映射写入，Create 失败时整体回滚（不留下半成品会话）。
//
// 这里直接使用 db 句子写消息：内存 sqlite 的每一层事务各持自己的连接，
// 回调内若走 MessageService 会落到另一条连接而看不到测试库。
func TestGormSessionStoreForkWithMessages(t *testing.T) {
	ctx := context.Background()
	db := newGormTestDB(t)
	sessions := agentservice.NewSessionService(NewGormSessionStore(db))

	src, err := sessions.Create(ctx, agenttypes.SessionCreateParams{UserID: 1, Title: "src"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := db.Create(&agenttypes.Message{
		SessionID: src.ID, Role: agenttypes.RoleUser, Kind: agenttypes.KindUser, Content: "hello", TaskID: 1,
	}).Error; err != nil {
		t.Fatalf("create message: %v", err)
	}

	type msgs = []agenttypes.Message
	const newTaskID int64 = 999
	forked, err := sessions.ForkWithMessages(ctx, agenttypes.SessionForkParams{
		SourceSessionID: src.ID, UserID: 1, Title: "branch",
	}, func(tx *gorm.DB, target *agenttypes.Session) error {
		var rows msgs
		if err := tx.Where("session_id = ?", src.ID).Find(&rows).Error; err != nil {
			return err
		}
		for i, m := range rows {
			if err := tx.Create(&agenttypes.Message{
				SessionID: target.ID, Seq: i, Role: m.Role, Kind: m.Kind,
				TaskID: newTaskID, Content: m.Content,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ForkWithMessages: %v", err)
	}
	var copied msgs
	if err := db.Where("session_id = ?", forked.ID).Order("seq ASC").Find(&copied).Error; err != nil {
		t.Fatalf("list forked messages: %v", err)
	}
	if len(copied) != 1 || copied[0].TaskID != newTaskID || copied[0].Content != "hello" {
		t.Fatalf("unexpected forked messages: %+v", copied)
	}

	// 回调失败：会话行与消息行一起回滚，源会话不受影响。
	boom := errors.New("boom")
	if _, err := sessions.ForkWithMessages(ctx, agenttypes.SessionForkParams{
		SourceSessionID: src.ID, UserID: 1,
	}, func(tx *gorm.DB, target *agenttypes.Session) error {
		return boom
	}); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	var count int64
	if err := db.Model(&agenttypes.Session{}).Count(&count).Error; err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if count != 2 { // 源会话 + 成功的 fork
		t.Fatalf("sessions = %d, want 2 (failed fork must roll back)", count)
	}
}
