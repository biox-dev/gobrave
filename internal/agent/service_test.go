package agent_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/agent/message"
	agentproviders "github.com/biox-dev/gobrave/internal/agent/providers"
	agentrepository "github.com/biox-dev/gobrave/internal/agent/repository"
	"github.com/biox-dev/gobrave/internal/agent/session"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
)

// newTestDB 打开一个内存 sqlite 数据库并迁移领域表（测试用）。
func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	_ = utils.InitSnowflake(1)
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(gormlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&agent.Task{}, &agent.PermissionRequest{}, &agent.QuestionRequest{}, &session.Session{}, &message.Message{}, &agent.Memory{}, &agent.Profile{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// newTestService 构建一个使用 mock Provider 与数据库仓库（内存 sqlite）的 AgentService。
func newTestService(t *testing.T) *agent.AgentService {
	t.Helper()
	svc, _ := newTestServiceWithDB(t)
	return svc
}

// newTestServiceWithDB 与 newTestService 相同，但同时返回底层 DB，便于断言落库结果。
// extra 用于注册测试专用 Provider。
func newTestServiceWithDB(t *testing.T, extra ...agent.Provider) (*agent.AgentService, *gorm.DB) {
	t.Helper()
	registry := agent.NewRegistry(append(agentproviders.All(), extra...)...)
	client := agent.NewClient(registry, agent.Options{})
	db := newTestDB(t)

	svc := agent.NewService(agent.ServiceConfig{
		Client:    client,
		Tasks:     agentrepository.NewGormTaskRepository(db),
		Messages:  message.NewService(agentrepository.NewGormMessageStore(db)),
		Perms:     agent.NewPermissionManager(agentrepository.NewGormPermissionRepository(db)),
		Questions: agent.NewQuestionManager(agentrepository.NewGormQuestionRepository(db)),
		Memory:    agent.NewMemoryManager(agent.MemoryConfig{Repo: agentrepository.NewGormMemoryRepository(db)}),
		Profiles:  agent.NewProfileManager(agentrepository.NewGormProfileRepository(db)),
	})
	return svc, db
}

// TestServiceFullPermissionFlow 演示完整链路：
// 创建任务 → 流式输出 → 请求权限（write → ask）→ 批准 → 恢复 → 完成。
func TestServiceFullPermissionFlow(t *testing.T) {
	svc := newTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events := make(chan agent.AgentEvent, 128)
	unsub := svc.Subscribe(0, func(_ context.Context, ev agent.AgentEvent) error {
		events <- ev
		return nil
	})
	defer unsub()

	req := agent.Request{
		Provider:   agent.ProviderMock,
		Stream:     true,
		WorkingDir: "/tmp",
		Env:        map[string]string{"MOCK_PERMISSION_DEMO": "true"},
		Messages:   []agent.Message{{Role: agent.RoleUser, Content: "hello"}},
	}

	task, err := svc.RunTask(ctx, req)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	// 等待 permission.created 事件。
	var permID int64
	for {
		ev := readEvent(t, ctx, events)
		if ev.Type == agent.EventPermissionCreated {
			perm, ok := ev.Payload.(*agent.PermissionRequest)
			if !ok {
				t.Fatalf("unexpected payload type %T", ev.Payload)
			}
			permID = perm.ID
			break
		}
		if ev.Type == agent.EventTaskFailed || ev.Type == agent.EventTaskCompleted {
			t.Fatalf("task finished before permission: %s", ev.Type)
		}
	}

	// 校验任务进入 waiting_permission，且权限可被查询。
	if got, _ := svc.GetTask(ctx, task.ID); got.Status != agent.TaskWaitingPermission {
		t.Fatalf("task status = %s, want waiting_permission", got.Status)
	}
	pending, err := svc.GetPendingPermissions(ctx, task.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending permissions = %d (err=%v), want 1", len(pending), err)
	}

	// 批准权限。
	if err := svc.ApprovePermission(ctx, permID, 1); err != nil {
		t.Fatalf("ApprovePermission: %v", err)
	}

	// 等待任务完成。
	for {
		ev := readEvent(t, ctx, events)
		switch ev.Type {
		case agent.EventTaskCompleted:
			goto done
		case agent.EventTaskFailed:
			t.Fatalf("task failed: %v", ev.Payload)
		}
	}
done:

	// 终态校验。
	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != agent.TaskCompleted {
		t.Fatalf("task status = %s, want completed", got.Status)
	}
}

// TestServiceDenyPermissionFlow 演示拒绝路径：拒绝后任务失败。
func TestServiceDenyPermissionFlow(t *testing.T) {
	svc := newTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events := make(chan agent.AgentEvent, 128)
	unsub := svc.Subscribe(0, func(_ context.Context, ev agent.AgentEvent) error {
		events <- ev
		return nil
	})
	defer unsub()

	req := agent.Request{
		Provider:   agent.ProviderMock,
		Stream:     true,
		WorkingDir: "/tmp",
		Env:        map[string]string{"MOCK_PERMISSION_DEMO": "true"},
		Messages:   []agent.Message{{Role: agent.RoleUser, Content: "hello"}},
	}

	task, err := svc.RunTask(ctx, req)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	var permID int64
	for {
		ev := readEvent(t, ctx, events)
		if ev.Type == agent.EventPermissionCreated {
			permID = ev.Payload.(*agent.PermissionRequest).ID
			break
		}
	}

	if err := svc.DenyPermission(ctx, permID, 1); err != nil {
		t.Fatalf("DenyPermission: %v", err)
	}

	for {
		ev := readEvent(t, ctx, events)
		if ev.Type == agent.EventTaskFailed {
			break
		}
	}

	got, _ := svc.GetTask(ctx, task.ID)
	if got.Status != agent.TaskFailed {
		t.Fatalf("task status = %s, want failed", got.Status)
	}
	if got.Error == "" {
		t.Fatalf("expected error recorded on task, got empty error")
	}
}

// TestStandaloneInvoke 验证 Client.Invoke 的一次性调用语义（AI 摘要等场景）不受影响。
// func TestStandaloneInvoke(t *testing.T) {
// 	registry := agent.NewRegistry(agentproviders.All()...)
// 	client := agent.NewClient(registry, agent.ProviderMock, agent.Options{})

// 	result, err := client.Invoke(context.Background(), agent.Request{
// 		Messages: []agent.Message{{Role: agent.RoleUser, Content: "hi"}},
// 	})
// 	if err != nil {
// 		t.Fatalf("Invoke: %v", err)
// 	}
// 	if result == nil || result.Content == "" {
// 		t.Fatalf("empty result: %+v", result)
// 	}
// }

// TestRecoverInterruptedTask 验证后端重启后，无活跃 goroutine 的 running 任务被标记为失败。
func TestRecoverInterruptedTask(t *testing.T) {
	ctx := context.Background()

	tasks := agentrepository.NewGormTaskRepository(newTestDB(t))
	task := agent.NewTask(agent.Request{Provider: agent.ProviderMock})
	task.Status = agent.TaskRunning
	if err := tasks.Update(ctx, task); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	svc := agent.NewService(agent.ServiceConfig{Tasks: tasks})

	if err := svc.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != agent.TaskFailed {
		t.Fatalf("task status = %s, want failed after recover", got.Status)
	}
	if got.Error == "" {
		t.Fatalf("expected error recorded on recovered task")
	}
}

// TestServiceSyncRunAutoApprovesPermission 演示同步执行（RunTaskSyncByID）的无人值守权限语义：
// 没有 UI 承接确认，因此 write（默认策略 ask）被自动放行——任务直接完成，
// 既不产生 pending 权限请求，也不进入 waiting_permission。
func TestServiceSyncRunAutoApprovesPermission(t *testing.T) {
	svc := newTestService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 测试库未播种内置 Profile，这里建一个自定义 Profile 固定 Provider=mock
	// （内置默认 Profile 的 Provider 是 copilot）。
	profile := &agent.Profile{Name: "sync-auto-approve-test", Provider: agent.ProviderMock}
	if err := svc.SaveProfile(ctx, profile); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	task, err := svc.CreateTask(ctx, agent.Request{
		Provider:   agent.ProviderMock,
		ProfileID:  profile.ID,
		WorkingDir: "/tmp",
		Env:        map[string]string{"MOCK_PERMISSION_DEMO": "true"},
		Messages:   []agent.Message{{Role: agent.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	result, err := svc.RunTaskSyncByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("RunTaskSyncByID: %v", err)
	}
	if result == nil || result.TaskID != task.ID {
		t.Fatalf("result = %+v, want TaskID %d", result, task.ID)
	}
	if !strings.Contains(result.Content, "[permission=allow]") {
		t.Fatalf("content = %q, want auto-approved permission marker", result.Content)
	}

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != agent.TaskCompleted {
		t.Fatalf("task status = %s, want completed", got.Status)
	}

	// 无人值守：不应留下任何待确认权限。
	pending, err := svc.GetPendingPermissions(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetPendingPermissions: %v", err)
	}
	if len(pending) != 0 {
		t.Fatalf("pending permissions = %d, want 0", len(pending))
	}
	_, total, err := svc.PagePermissions(ctx, 0, 10, task.ID)
	if err != nil {
		t.Fatalf("PagePermissions: %v", err)
	}
	if total != 0 {
		t.Fatalf("permission requests = %d, want 0", total)
	}
}

// TestTimelineMessagesPersistedDuringRun 验证 agent_messages 在轮内即时落库：
// 不再依赖「轮末从 agent_events 回放」，tool_call / tool_result 在任务执行过程中
// 就已经按顺序写入，为 Agent 循环中途退出后的恢复留下事实。
func TestTimelineMessagesPersistedDuringRun(t *testing.T) {
	svc, db := newTestServiceWithDB(t)
	ctx := context.Background()

	profile := &agent.Profile{Name: "timeline-persist-test", Provider: agent.ProviderMock}
	if err := svc.SaveProfile(ctx, profile); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	const sessionID int64 = 20241022
	task, err := svc.CreateTask(ctx, agent.Request{
		Provider:   agent.ProviderMock,
		ProfileID:  profile.ID,
		SessionID:  sessionID,
		WorkingDir: "/tmp",
		Env:        map[string]string{"MOCK_TOOL_DEMO": "true"},
		Messages:   []agent.Message{{Role: agent.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := svc.RunTaskSyncByID(ctx, task.ID); err != nil {
		t.Fatalf("RunTaskSyncByID: %v", err)
	}

	var rows []message.Message
	if err := db.Where("session_id = ? AND task_id = ?", sessionID, task.ID).
		Order("seq ASC").Find(&rows).Error; err != nil {
		t.Fatalf("query agent_messages: %v", err)
	}
	if len(rows) == 0 {
		t.Fatalf("no timeline messages persisted for task %d", task.ID)
	}

	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Kind] = true
		if row.Seq < 0 {
			t.Fatalf("message %d has invalid seq %d", row.ID, row.Seq)
		}
	}
	for _, want := range []string{message.KindToolCall, message.KindToolResult} {
		if !seen[want] {
			t.Fatalf("missing timeline message kind %q (got %v)", want, seen)
		}
	}
}

// TestAssistantMessageBlockPersistedInTurn 验证 assistant 文本块也在轮内即时落库
// （KindAssistant），不再依赖轮末从事件回放——这是 Agent 循环中途退出后恢复的基础。
func TestAssistantMessageBlockPersistedInTurn(t *testing.T) {
	svc, db := newTestServiceWithDB(t, messageBlockProvider{})
	ctx := context.Background()

	profile := &agent.Profile{Name: "assistant-block-test", Provider: messageBlockProviderName}
	if err := svc.SaveProfile(ctx, profile); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	const sessionID int64 = 20241023
	task, err := svc.CreateTask(ctx, agent.Request{
		Provider:   messageBlockProviderName,
		ProfileID:  profile.ID,
		SessionID:  sessionID,
		WorkingDir: "/tmp",
		Stream:     true,
		Messages:   []agent.Message{{Role: agent.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := svc.RunTaskSyncByID(ctx, task.ID); err != nil {
		t.Fatalf("RunTaskSyncByID: %v", err)
	}

	var rows []message.Message
	if err := db.Where("session_id = ? AND task_id = ?", sessionID, task.ID).
		Order("seq ASC").Find(&rows).Error; err != nil {
		t.Fatalf("query agent_messages: %v", err)
	}

	var assistant []message.Message
	for _, row := range rows {
		if row.Kind == message.KindAssistant {
			assistant = append(assistant, row)
		}
	}
	if len(assistant) != 1 || assistant[0].Content != "block answer" {
		t.Fatalf("assistant messages = %+v, want exactly one with content %q", assistant, "block answer")
	}
}

// TestAssistantFinalFallbackWhenNoMessageBlock 验证「只发增量、不发完整 message 块」的
// Provider 仍会用最终结果兜底写一条 assistant_final（TimelineRecorder.RecordResult），
// 刷新 / 恢复后的回答不会丢。
func TestAssistantFinalFallbackWhenNoMessageBlock(t *testing.T) {
	svc, db := newTestServiceWithDB(t, deltaOnlyProvider{})
	ctx := context.Background()

	profile := &agent.Profile{Name: "delta-only-test", Provider: deltaOnlyProviderName}
	if err := svc.SaveProfile(ctx, profile); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	const sessionID int64 = 20241024
	task, err := svc.CreateTask(ctx, agent.Request{
		Provider:   deltaOnlyProviderName,
		ProfileID:  profile.ID,
		SessionID:  sessionID,
		WorkingDir: "/tmp",
		Stream:     true,
		Messages:   []agent.Message{{Role: agent.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := svc.RunTaskSyncByID(ctx, task.ID); err != nil {
		t.Fatalf("RunTaskSyncByID: %v", err)
	}

	var rows []message.Message
	if err := db.Where("session_id = ? AND task_id = ?", sessionID, task.ID).
		Order("seq ASC").Find(&rows).Error; err != nil {
		t.Fatalf("query agent_messages: %v", err)
	}

	var assistant, final []message.Message
	for _, row := range rows {
		switch row.Kind {
		case message.KindAssistant:
			assistant = append(assistant, row)
		case message.KindAssistantFinal:
			final = append(final, row)
		}
	}
	if len(assistant) != 0 {
		t.Fatalf("assistant blocks = %d, want 0 (provider emits no message block)", len(assistant))
	}
	if len(final) != 1 || final[0].Content != "delta answer" {
		t.Fatalf("assistant_final = %+v, want exactly one with content %q", final, "delta answer")
	}
}

// ---- test providers ----

// messageBlockProviderName 是测试专用 Provider 名称。
const messageBlockProviderName = "test-message-block"

// messageBlockProvider 构造一个只发「完整 assistant 消息块 + done」的 Agent，
// 用于验证 assistant 文本块在轮内落库。
type messageBlockProvider struct{}

func (messageBlockProvider) Name() string { return messageBlockProviderName }

func (messageBlockProvider) New(agent.Options) (agent.Agent, error) {
	return messageBlockAgent{}, nil
}

type messageBlockAgent struct{}

func (messageBlockAgent) Name() string { return messageBlockProviderName }

func (messageBlockAgent) Invoke(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	return messageBlockAgent{}.Stream(ctx, req, rt)
}

func (messageBlockAgent) Stream(ctx context.Context, _ agent.Request, rt agent.Runtime) (*agent.Result, error) {
	if err := rt.Emit(ctx, agent.StreamEvent{
		Type: agent.StreamEventMessage,
		Data: agent.MessageBlock{ID: "m1", Content: "block answer"},
	}); err != nil {
		return nil, err
	}
	if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventDone}); err != nil {
		return nil, err
	}
	return &agent.Result{Content: "block answer"}, nil
}

// deltaOnlyProviderName 是「只发增量」测试 Provider 名称。
const deltaOnlyProviderName = "test-delta-only"

// deltaOnlyProvider 构造一个只发文本增量 + done 的 Agent，用于验证
// RecordResult 的最终结果兜底。
type deltaOnlyProvider struct{}

func (deltaOnlyProvider) Name() string { return deltaOnlyProviderName }

func (deltaOnlyProvider) New(agent.Options) (agent.Agent, error) {
	return deltaOnlyAgent{}, nil
}

type deltaOnlyAgent struct{}

func (deltaOnlyAgent) Name() string { return deltaOnlyProviderName }

func (deltaOnlyAgent) Invoke(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	return deltaOnlyAgent{}.Stream(ctx, req, rt)
}

func (deltaOnlyAgent) Stream(ctx context.Context, _ agent.Request, rt agent.Runtime) (*agent.Result, error) {
	if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventText, Content: "delta answer"}); err != nil {
		return nil, err
	}
	if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventDone}); err != nil {
		return nil, err
	}
	return &agent.Result{Content: "delta answer"}, nil
}

// ---- helpers ----

func readEvent(t *testing.T, ctx context.Context, ch <-chan agent.AgentEvent) agent.AgentEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-ctx.Done():
		t.Fatalf("timed out waiting for event")
		return agent.AgentEvent{}
	}
}
