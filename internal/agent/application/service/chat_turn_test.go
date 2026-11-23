package service_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	agentrepository "github.com/biox-dev/gobrave/internal/agent/application/repository"
	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"gorm.io/gorm"
)

// newTurnTestChat 构造一个带 mock Provider 的 ChatService，
// 并返回会话 / 消息服务与底层 AgentService 便于造数据与断言。
func newTurnTestChat(t *testing.T) (
	*agentservice.ChatService,
	*agentservice.AgentService,
	agenttypes.Session,
	agentinterfaces.SessionService,
	agentinterfaces.MessageService,
) {
	t.Helper()
	db := newTestDB(t)
	svc := newServiceOnDB(t, db)
	turnTestDBs.Store(svc, db)
	t.Cleanup(func() { turnTestDBs.Delete(svc) })
	sessions := agentservice.NewSessionService(agentrepository.NewGormSessionStore(db))
	messages := agentservice.NewMessageService(agentrepository.NewGormMessageStore(db))
	chat := agentservice.NewChatService(svc, sessions, messages, nil, t.TempDir())

	sess, err := sessions.Create(context.Background(), agenttypes.SessionCreateParams{UserID: 1, ProjectID: 1, Title: "t"})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	return chat, svc, sess, sessions, messages
}

// turnTestDBs 记录测试用 AgentService 对应的数据库，供造任务数据时复用同一份库。
var turnTestDBs sync.Map

func turnTestDB(t *testing.T, svc *agentservice.AgentService) *gorm.DB {
	t.Helper()
	db, ok := turnTestDBs.Load(svc)
	if !ok {
		t.Fatalf("test db not registered for service")
	}
	return db.(*gorm.DB)
}

func seedTurnTask(t *testing.T, svc *agentservice.AgentService, sessionID int64) *agenttypes.Task {
	t.Helper()
	task := agenttypes.NewTask(agenttypes.Request{Provider: agenttypes.ProviderMock},
		agenttypes.TaskFacts{SessionID: sessionID, UserID: 1})
	if err := agentrepository.NewGormTaskRepository(turnTestDB(t, svc)).Create(context.Background(), task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return task
}

func createTurnMessages(t *testing.T, messages agentinterfaces.MessageService, sessionID, taskID int64) {
	t.Helper()
	ctx := context.Background()
	for _, in := range []agenttypes.MessageCreateParams{
		{SessionID: sessionID, Role: agenttypes.RoleUser, Kind: agenttypes.KindUser, Content: "q", TaskID: taskID},
		{SessionID: sessionID, Role: agenttypes.RoleAssistant, Kind: agenttypes.KindAssistant, Content: "a", TaskID: taskID},
	} {
		if _, err := messages.Create(ctx, in); err != nil {
			t.Fatalf("Create message: %v", err)
		}
	}
}

// TestDeleteTaskMessagesRemovesUserAndAssistant 校验按任务删除会同时删掉 user 消息，
// 且不改动同会话其它轮次的消息。
func TestDeleteTaskMessagesRemovesUserAndAssistant(t *testing.T) {
	ctx := context.Background()
	chat, svc, sess, sessions, messages := newTurnTestChat(t)

	taskA := seedTurnTask(t, svc, sess.ID)
	taskB := seedTurnTask(t, svc, sess.ID)
	createTurnMessages(t, messages, sess.ID, taskA.ID)
	createTurnMessages(t, messages, sess.ID, taskB.ID)

	sess.CurrentTaskID = taskA.ID
	if _, err := sessions.Save(ctx, sess); err != nil {
		t.Fatalf("save session: %v", err)
	}

	if err := chat.DeleteTaskMessages(ctx, taskA.ID); err != nil {
		t.Fatalf("DeleteTaskMessages: %v", err)
	}

	rest, err := messages.List(ctx, sess.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rest) != 2 {
		t.Fatalf("remaining = %d, want 2 (other turn only)", len(rest))
	}
	for _, m := range rest {
		if m.TaskID == taskA.ID {
			t.Fatalf("task A message should be deleted: %+v", m)
		}
	}
	reloaded, err := sessions.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if reloaded.CurrentTaskID != 0 {
		t.Fatalf("CurrentTaskID = %d, want 0", reloaded.CurrentTaskID)
	}
}

// TestDeleteTaskMessagesRejectsTaskWithoutSession 校验不属于任何会话的任务不可按任务删除。
func TestDeleteTaskMessagesRejectsTaskWithoutSession(t *testing.T) {
	ctx := context.Background()
	chat, svc, _, _, _ := newTurnTestChat(t)

	// 会话无关的一次性任务（SessionID = 0）没有可展示的会话，不允许走会话接口。
	task := seedTurnTask(t, svc, 0)
	if err := chat.DeleteTaskMessages(ctx, task.ID); !errors.Is(err, agenttypes.ErrTaskNotFound) {
		t.Fatalf("err = %v, want ErrTaskNotFound", err)
	}
}

// TestRetryTurnReusesUserInput 校验重试会删掉旧轮次消息、复用原用户输入并创建新任务。
func TestRetryTurnReusesUserInput(t *testing.T) {
	ctx := context.Background()
	chat, svc, sess, _, messages := newTurnTestChat(t)
	task := seedTurnTask(t, svc, sess.ID)
	createTurnMessages(t, messages, sess.ID, task.ID)

	newTask, _, err := chat.RetryTurn(ctx, agentservice.TurnInput{
		UserID:   1,
		Provider: agenttypes.ProviderMock,
	}, task.ID)
	if err != nil {
		t.Fatalf("RetryTurn: %v", err)
	}
	if newTask.ID == task.ID || newTask.ID == 0 {
		t.Fatalf("retry should create a new task, got %d", newTask.ID)
	}

	// 旧任务的消息已清空，新任务只有本轮 user 消息（沿用原输入）。
	oldMsgs, err := messages.ListByTask(ctx, task.ID)
	if err != nil || len(oldMsgs) != 0 {
		t.Fatalf("old task messages = %d err=%v, want 0", len(oldMsgs), err)
	}
	newMsgs, err := messages.ListByTask(ctx, newTask.ID)
	if err != nil || len(newMsgs) != 1 {
		t.Fatalf("new task messages = %d err=%v, want 1 user message", len(newMsgs), err)
	}
	if newMsgs[0].Role != agenttypes.RoleUser || newMsgs[0].Content != "q" {
		t.Fatalf("new task message = %+v, want user 'q' carried over", newMsgs[0])
	}
}
