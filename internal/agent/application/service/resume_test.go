package service_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/biox-dev/gobrave/internal/agent"
	agentrepository "github.com/biox-dev/gobrave/internal/agent/application/repository"
	"github.com/biox-dev/gobrave/internal/agent/message"
	agentproviders "github.com/biox-dev/gobrave/internal/agent/providers"
	"github.com/biox-dev/gobrave/internal/agent/session"
	"gorm.io/gorm"
)

// ---- 测试用可恢复 Provider ----

const resumeTestProviderName = "resume_test"

type resumeTestProvider struct {
	// requestPermission 为 true 时，执行期会为一个固定 tool call 请求写权限。
	requestPermission bool

	mu       sync.Mutex
	runs     int
	resumed  int
	lastSeen *agent.TaskResumeState
}

func (p *resumeTestProvider) Name() string { return resumeTestProviderName }

func (p *resumeTestProvider) New(agent.Options) (agent.Agent, error) {
	return &resumeTestAgent{provider: p}, nil
}

func (p *resumeTestProvider) stats() (runs, resumed int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.runs, p.resumed
}

type resumeTestAgent struct {
	provider *resumeTestProvider
}

func (a *resumeTestAgent) Name() string { return resumeTestProviderName }

func (a *resumeTestAgent) Invoke(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	return a.run(ctx, req, rt)
}

func (a *resumeTestAgent) Stream(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	return a.run(ctx, req, rt)
}

func (a *resumeTestAgent) run(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	cr, ok := rt.(agent.CheckpointRuntime)
	if !ok {
		return nil, errors.New("runtime does not support checkpoints")
	}

	state := cr.LoadCheckpoint(ctx)
	a.provider.mu.Lock()
	a.provider.runs++
	if state != nil {
		a.provider.resumed++
	}
	a.provider.lastSeen = state
	a.provider.mu.Unlock()

	if state == nil {
		// 模拟原生循环：在“模型已响应、工具待执行”安全点落检查点。
		if err := cr.SaveCheckpoint(ctx, &agent.TaskResumeState{
			Version: agent.ResumeStateVersion,
			History: []agent.ResumeMessage{{Role: "user", Content: "hello"}},
			Pending: &agent.ResumeTurn{Assistant: agent.ResumeMessage{
				Role: string(agent.RoleAssistant),
				ToolCalls: []agent.ResumeToolCall{{
					ID: "call-1", Name: "write", Input: `{"path":"/tmp/x"}`,
				}},
			}},
		}); err != nil {
			return nil, err
		}
	}

	if a.provider.requestPermission {
		ctx = agent.WithToolCallID(ctx, "call-1")
		decision, err := rt.RequestPermission(ctx, req.RequestUserID(), agent.Operation{
			Type: agent.OperationWrite,
			Path: "/tmp/x",
		})
		if err != nil {
			return nil, err
		}
		if decision == agent.DecisionDeny {
			return nil, agent.ErrPermissionDenied
		}
	}

	// 正常结束语义：清理检查点（与 native 循环一致）。
	_ = cr.ClearCheckpoint(ctx)
	return &agent.Result{Content: "resumed-ok"}, nil
}

// ---- 测试辅助 ----

// newServiceOnDB 复用同一个数据库构造 AgentService（模拟后端重启）。
func newServiceOnDB(t *testing.T, db *gorm.DB, extra ...agent.Provider) *agent.AgentService {
	t.Helper()
	registry := agent.NewRegistry(append(agentproviders.All(), extra...)...)
	client := agent.NewClient(registry, agent.Options{})
	return agent.NewService(agent.ServiceConfig{
		Client:    client,
		Tasks:     agentrepository.NewGormTaskRepository(db),
		Messages:  message.NewService(agentrepository.NewGormMessageStore(db)),
		Perms:     agent.NewPermissionManager(agentrepository.NewGormPermissionRepository(db)),
		Questions: agent.NewQuestionManager(agentrepository.NewGormQuestionRepository(db)),
		Profiles:  agent.NewProfileManager(agentrepository.NewGormProfileRepository(db)),
	})
}

// seedResumeProfile 创建一个指向测试 Provider 的 Profile（测试库不预置内置 Profile）。
func seedResumeProfile(t *testing.T, svc *agent.AgentService) *agent.Profile {
	t.Helper()
	p := &agent.Profile{Name: "resume-test", Provider: resumeTestProviderName}
	if err := svc.SaveProfile(context.Background(), p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	return p
}

// waitTaskStatus 轮询任务状态直到命中期望值或超时。
func waitTaskStatus(t *testing.T, svc *agent.AgentService, taskID int64, want agent.TaskStatus) *agent.Task {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := svc.GetTask(context.Background(), taskID)
		if err != nil {
			t.Fatalf("GetTask: %v", err)
		}
		if task.Status == want {
			return task
		}
		time.Sleep(10 * time.Millisecond)
	}
	task, _ := svc.GetTask(context.Background(), taskID)
	t.Fatalf("task status = %v, want %v (error=%q)", task.Status, want, task.Error)
	return nil
}

// ---- 用例 ----

// TestApprovePermissionResumesAfterRestart 验证：
// 后端重启后（无活跃 goroutine），用户点击允许会从持久化检查点真正续跑任务。
func TestApprovePermissionResumesAfterRestart(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	provider := &resumeTestProvider{requestPermission: true}

	// 构造“重启前”的数据库状态：waiting_permission + 检查点 + pending 权限。
	profileSvc := newServiceOnDB(t, db, provider)
	profile := seedResumeProfile(t, profileSvc)

	req := agent.Request{
		Provider:  resumeTestProviderName,
		ProfileID: profile.ID,
		Messages:  []agent.Message{{Role: agent.RoleUser, Content: "hello"}},
	}
	task := agent.NewTask(req)
	task.Status = agent.TaskWaitingPermission
	task.ResumeState = &agent.TaskResumeState{
		Version: agent.ResumeStateVersion,
		History: []agent.ResumeMessage{{Role: "user", Content: "hello"}},
		Pending: &agent.ResumeTurn{Assistant: agent.ResumeMessage{
			Role:      string(agent.RoleAssistant),
			ToolCalls: []agent.ResumeToolCall{{ID: "call-1", Name: "write", Input: `{}`}},
		}},
	}
	if err := agentrepository.NewGormTaskRepository(db).Create(ctx, task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	perm := agent.NewPermissionRequest(task.ID, "", agent.Operation{
		Type:     agent.OperationWrite,
		Path:     "/tmp/x",
		Metadata: map[string]any{agent.OperationMetadataToolCallID: "call-1"},
	})
	if err := agentrepository.NewGormPermissionRepository(db).Create(ctx, perm); err != nil {
		t.Fatalf("seed permission: %v", err)
	}

	// “重启”：同一数据库、全新的 Service（active / waiters 均为空）。
	svc := newServiceOnDB(t, db, provider)
	if err := svc.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := waitTaskStatus(t, svc, task.ID, agent.TaskWaitingPermission); got.Status != agent.TaskWaitingPermission {
		t.Fatalf("status after recover = %v", got.Status)
	}

	if err := svc.ApprovePermission(ctx, perm.ID, 1); err != nil {
		t.Fatalf("ApprovePermission: %v", err)
	}
	waitTaskStatus(t, svc, task.ID, agent.TaskCompleted)

	runs, resumed := provider.stats()
	if runs != 1 || resumed != 1 {
		t.Fatalf("provider runs=%d resumed=%d, want 1/1", runs, resumed)
	}

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.ResumeState != nil {
		t.Fatalf("checkpoint should be cleared after completion")
	}
	approved, err := agentrepository.NewGormPermissionRepository(db).Get(ctx, perm.ID)
	if err != nil {
		t.Fatalf("Get permission: %v", err)
	}
	if approved.Status != agent.PermissionConsumed {
		t.Fatalf("permission status = %s, want consumed", approved.Status)
	}
}

// TestManualResumeInterruptedTask 验证运行中任务被重启打断后，可手动触发继续。
func TestManualResumeInterruptedTask(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	provider := &resumeTestProvider{requestPermission: false}

	profileSvc := newServiceOnDB(t, db, provider)
	profile := seedResumeProfile(t, profileSvc)

	task := agent.NewTask(agent.Request{
		Provider:  resumeTestProviderName,
		ProfileID: profile.ID,
		Messages:  []agent.Message{{Role: agent.RoleUser, Content: "hello"}},
	})
	task.Status = agent.TaskRunning
	task.ResumeState = &agent.TaskResumeState{
		Version: agent.ResumeStateVersion,
		History: []agent.ResumeMessage{{Role: "user", Content: "hello"}},
	}
	if err := agentrepository.NewGormTaskRepository(db).Create(ctx, task); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	svc := newServiceOnDB(t, db, provider)
	// Recover 把没有活跃 goroutine 的 running 任务置为 failed，并保留检查点。
	if err := svc.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	failed := waitTaskStatus(t, svc, task.ID, agent.TaskFailed)
	if !failed.Resumable || !strings.Contains(failed.Error, "resumable") {
		t.Fatalf("task should be resumable: resumable=%v error=%q", failed.Resumable, failed.Error)
	}

	// 手动继续。
	if err := svc.ResumeTask(ctx, task.ID); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	waitTaskStatus(t, svc, task.ID, agent.TaskCompleted)

	if _, resumed := provider.stats(); resumed != 1 {
		t.Fatalf("provider resumed=%d, want 1", resumed)
	}

	// 已完成任务不可再次续跑。
	if err := svc.ResumeTask(ctx, task.ID); !errors.Is(err, agent.ErrTaskNotResumable) {
		t.Fatalf("ResumeTask on completed = %v, want ErrTaskNotResumable", err)
	}
}

// TestRecoverInvokesTaskFinishedHandler 验证重启后 onTaskFinished 仍然生效：
// 即使 ChatService 的内存 turns 为空，也会清理会话 CurrentTaskID 并补写错误消息。
func TestRecoverInvokesTaskFinishedHandler(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	sessions := session.NewService(agentrepository.NewGormSessionStore(db))
	messages := message.NewService(agentrepository.NewGormMessageStore(db))

	sess, err := sessions.Create(ctx, session.CreateParams{UserID: 1, Title: "t"})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	svc := newServiceOnDB(t, db)
	// 关键：ChatService 在 Recover 之前构造，注册任务终态回调。
	agent.NewChatService(svc, sessions, messages, nil)

	task := agent.NewTask(agent.Request{Provider: agent.ProviderMock, SessionID: sess.ID})
	task.SessionID = sess.ID
	task.Status = agent.TaskRunning
	task.ResumeState = &agent.TaskResumeState{Version: agent.ResumeStateVersion}
	if err := agentrepository.NewGormTaskRepository(db).Create(ctx, task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	sess.CurrentTaskID = task.ID
	if _, err := sessions.Save(ctx, sess); err != nil {
		t.Fatalf("save session: %v", err)
	}

	if err := svc.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	got, err := sessions.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	// 可续跑的中断任务必须保留会话指针，前端才能据此展示 Continue。
	if got.CurrentTaskID != task.ID {
		t.Fatalf("session CurrentTaskID = %d, want %d (resumable task)", got.CurrentTaskID, task.ID)
	}

	msgs, err := messages.List(ctx, sess.ID)
	if err != nil {
		t.Fatalf("List messages: %v", err)
	}
	var foundError bool
	for _, m := range msgs {
		if m.TaskID == task.ID && m.Kind == message.KindError {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Fatalf("expected an error message for interrupted task, messages=%+v", msgs)
	}
}
