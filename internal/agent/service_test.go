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
	if err := db.AutoMigrate(&agent.Task{}, &agent.PermissionRequest{}, &agent.QuestionRequest{}, &agent.AgentEvent{}, &session.Session{}, &message.Message{}, &agent.Memory{}, &agent.Profile{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// newTestService 构建一个使用 mock Provider 与数据库仓库（内存 sqlite）的 AgentService。
func newTestService(t *testing.T) *agent.AgentService {
	t.Helper()
	registry := agent.NewRegistry(agentproviders.All()...)
	client := agent.NewClient(registry, agent.Options{})
	db := newTestDB(t)

	return agent.NewService(agent.ServiceConfig{
		Client:    client,
		Tasks:     agentrepository.NewGormTaskRepository(db),
		Events:    agentrepository.NewGormEventRepository(db),
		Perms:     agent.NewPermissionManager(agentrepository.NewGormPermissionRepository(db)),
		Questions: agent.NewQuestionManager(agentrepository.NewGormQuestionRepository(db)),
		Memory:    agent.NewMemoryManager(agent.MemoryConfig{Repo: agentrepository.NewGormMemoryRepository(db)}),
		Profiles:  agent.NewProfileManager(agentrepository.NewGormProfileRepository(db)),
	})
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

	task, err := svc.RunTask(ctx, req, nil)
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

	// 事件序列校验：sequence 单调递增，且包含权限创建 / 决策 / 流式 / 完成。
	evs, err := svc.GetEvents(ctx, task.ID, 0)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	seen := map[agent.AgentEventType]bool{}
	for i, e := range evs {
		if i > 0 && e.Sequence <= evs[i-1].Sequence {
			t.Fatalf("sequence not increasing: %d then %d", evs[i-1].Sequence, e.Sequence)
		}
		seen[e.Type] = true
	}
	for _, want := range []agent.AgentEventType{
		agent.EventTaskCreated,
		agent.EventTaskStarted,
		agent.EventTaskWaiting,
		agent.EventPermissionCreated,
		agent.EventPermissionResolved,
		agent.EventStream,
		agent.EventTaskCompleted,
	} {
		if !seen[want] {
			t.Errorf("missing event type %s (seen=%v)", want, seen)
		}
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

	task, err := svc.RunTask(ctx, req, nil)
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
