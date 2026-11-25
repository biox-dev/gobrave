package service_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	agentrepository "github.com/biox-dev/gobrave/internal/agent/application/repository"
	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	agentclient "github.com/biox-dev/gobrave/internal/agent/client"
	agentproviders "github.com/biox-dev/gobrave/internal/agent/providers"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"gorm.io/gorm"
)

// ---- 测试用「可续跑 Provider」----
//
// 它模拟原生循环的恢复协议：本轮的事实先落 agent_messages（tool_call），执行完再把
// 结果落库（tool_result）。恢复时读取本任务的消息，只重放没有结果的调用。

const resumeTestProviderName = "resume_test"

// resumeTestTurnID 是测试里固定使用的轮次标识。
const resumeTestTurnID = "turn-1"

// resumeTestCall 是测试 Provider 每次执行都要完成的工具调用。
var resumeTestCall = agenttypes.ToolCall{ID: "call-1", Name: "write", Arguments: `{"path":"/tmp/x"}`}

type resumeTestProvider struct {
	// requestPermission 为 true 时，执行期会为第一个 tool call 请求写权限。
	requestPermission bool

	mu       sync.Mutex
	messages agentinterfaces.MessageService
	runs     int
	resumed  int
}

func (p *resumeTestProvider) Name() string { return resumeTestProviderName }

func (p *resumeTestProvider) New(agentclient.Options) (agentinterfaces.Agent, error) {
	return &resumeTestAgent{provider: p}, nil
}

// bindMessages 由测试装配注入，供 Provider 把工具事实写进 agent_messages。
func (p *resumeTestProvider) bindMessages(m agentinterfaces.MessageService) { p.messages = m }

func (p *resumeTestProvider) stats() (runs, resumed int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.runs, p.resumed
}

type resumeTestAgent struct {
	provider *resumeTestProvider
}

func (a *resumeTestAgent) Name() string { return resumeTestProviderName }

func (a *resumeTestAgent) Invoke(ctx context.Context, req agenttypes.Request, rt agentinterfaces.Runtime) (*agenttypes.Result, error) {
	return a.run(ctx, req, rt)
}

func (a *resumeTestAgent) Stream(ctx context.Context, req agenttypes.Request, rt agentinterfaces.Runtime) (*agenttypes.Result, error) {
	return a.run(ctx, req, rt)
}

func (a *resumeTestAgent) run(ctx context.Context, req agenttypes.Request, rt agentinterfaces.Runtime) (*agenttypes.Result, error) {
	mr, ok := rt.(agentinterfaces.MessageReplay)
	if !ok {
		return nil, errors.New("runtime does not support message replay")
	}
	msgs, err := mr.TaskMessages(ctx)
	if err != nil {
		return nil, err
	}

	// 有 tool_call 却没有对应 tool_result 的调用 = 上次中断时没走完的部分。
	done := make(map[string]bool, len(msgs))
	for _, m := range msgs {
		if m.Kind == agenttypes.KindToolResult && m.ToolCallID != "" {
			done[m.ToolCallID] = true
		}
	}

	a.provider.mu.Lock()
	a.provider.runs++
	if hasToolCall(msgs) {
		a.provider.resumed++
	}
	a.provider.mu.Unlock()

	call := resumeTestCall
	if !done[call.ID] {
		if !hasToolCall(msgs) {
			// 首次执行：模型响应先落成 tool_call 事件（与 native 循环顺序一致）。
			_ = rt.Emit(ctx, agenttypes.StreamEvent{
				Type: agenttypes.StreamEventToolCall,
				Data: agenttypes.ToolCall{ID: call.ID, TurnID: resumeTestTurnID, Name: call.Name, Arguments: call.Arguments},
			})
		}

		if a.provider.requestPermission {
			callCtx := agenttypes.WithToolCallID(ctx, call.ID)
			decision, err := rt.RequestPermission(callCtx, agenttypes.Operation{
				Type: agenttypes.OperationWrite,
				Path: "/tmp/x",
			})
			if err != nil {
				return nil, err
			}
			if decision == agenttypes.DecisionDeny {
				return nil, agenttypes.ErrPermissionDenied
			}
		}

		_ = rt.Emit(ctx, agenttypes.StreamEvent{
			Type: agenttypes.StreamEventToolResult,
			Data: agenttypes.ToolResultBlock{
				ToolCallID: call.ID,
				TurnID:     resumeTestTurnID,
				Name:       call.Name,
				Content:    "ok",
			},
		})
	}

	return &agenttypes.Result{Content: "resumed-ok"}, nil
}

// hasToolCall 判断本任务是否已经有落库的工具调用。
func hasToolCall(msgs []agenttypes.Message) bool {
	for _, m := range msgs {
		if m.Kind == agenttypes.KindToolCall {
			return true
		}
	}
	return false
}

// ---- 测试辅助 ----

// newServiceOnDB 复用同一个数据库构造 AgentService（模拟后端重启）。
func newServiceOnDB(t *testing.T, db *gorm.DB, extra ...agentclient.Provider) *agentservice.AgentService {
	t.Helper()
	messages := agentservice.NewMessageService(agentrepository.NewGormMessageStore(db))
	for _, p := range extra {
		if binder, ok := p.(interface {
			bindMessages(agentinterfaces.MessageService)
		}); ok {
			binder.bindMessages(messages)
		}
	}
	registry := agentclient.NewRegistry(append(agentproviders.All(), extra...)...)
	client := agentclient.NewClient(registry, agentclient.Options{})
	return agentservice.NewService(agentservice.ServiceConfig{
		Client:    client,
		Tasks:     agentrepository.NewGormTaskRepository(db),
		Messages:  messages,
		Perms:     agentservice.NewPermissionManager(agentrepository.NewGormPermissionRepository(db)),
		Questions: agentservice.NewQuestionManager(agentrepository.NewGormQuestionRepository(db)),
		Profiles:  agentservice.NewProfileManager(agentrepository.NewGormProfileRepository(db)),
	})
}

// seedResumeProfile 创建一个指向测试 Provider 的 Profile（测试库不预置内置 Profile）。
func seedResumeProfile(t *testing.T, svc *agentservice.AgentService) *agenttypes.Profile {
	t.Helper()
	p := &agenttypes.Profile{Name: "resume-test", Provider: resumeTestProviderName}
	if err := svc.SaveProfile(context.Background(), p); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}
	return p
}

// seedPendingToolCall 写入一条「已发起但未执行完」的工具调用消息。
//
// 这是恢复执行的唯一事实来源：没有 tool_result 就代表该调用尚未完成。
func seedPendingToolCall(t *testing.T, messages agentinterfaces.MessageService, sessionID, taskID int64, call agenttypes.ToolCall) {
	t.Helper()
	if _, err := messages.Create(context.Background(), agenttypes.MessageCreateParams{
		SessionID:  sessionID,
		Role:       agenttypes.RoleAssistant,
		Kind:       agenttypes.KindToolCall,
		TaskID:     taskID,
		TurnID:     resumeTestTurnID,
		ToolCallID: call.ID,
		Content:    "tool call",
		Data:       call,
	}); err != nil {
		t.Fatalf("seed tool call: %v", err)
	}
}

// waitTaskStatus 轮询任务状态直到命中期望值或超时。
func waitTaskStatus(t *testing.T, svc *agentservice.AgentService, taskID int64, want agenttypes.TaskStatus) *agenttypes.Task {
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

// newResumeSession 创建一个会话，作为消息事实的归属。
func newResumeSession(t *testing.T, sessions agentinterfaces.SessionService) agenttypes.Session {
	t.Helper()
	sess, err := sessions.Create(context.Background(), agenttypes.SessionCreateParams{UserID: 1, ProjectID: 1, Title: "t"})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	return sess
}

// ---- 用例 ----

// TestApprovePermissionResumesAfterRestart 验证：
// 后端重启后（无活跃 goroutine），用户点击允许会从 agent_messages 真正续跑任务。
func TestApprovePermissionResumesAfterRestart(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	provider := &resumeTestProvider{requestPermission: true}

	// 构造“重启前”的数据库状态：waiting_permission + 未完成的 tool_call + pending 权限。
	profileSvc := newServiceOnDB(t, db, provider)
	profile := seedResumeProfile(t, profileSvc)
	sessions := agentservice.NewSessionService(agentrepository.NewGormSessionStore(db))
	messages := agentservice.NewMessageService(agentrepository.NewGormMessageStore(db))
	sess := newResumeSession(t, sessions)

	req := agenttypes.Request{
		Provider:  resumeTestProviderName,
		ProfileID: profile.ID,
		Messages:  []agenttypes.Message{{Role: agenttypes.RoleUser, Content: "hello"}},
	}
	task := agenttypes.NewTask(req, agenttypes.TaskFacts{SessionID: sess.ID})
	task.Status = agenttypes.TaskWaitingPermission
	if err := agentrepository.NewGormTaskRepository(db).Create(ctx, task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	seedPendingToolCall(t, messages, sess.ID, task.ID, resumeTestCall)

	perm := agenttypes.NewPermissionRequest(task.ID, "", agenttypes.Operation{
		Type:     agenttypes.OperationWrite,
		Path:     "/tmp/x",
		Metadata: map[string]any{agenttypes.OperationMetadataToolCallID: "call-1"},
	})
	if err := agentrepository.NewGormPermissionRepository(db).Create(ctx, perm); err != nil {
		t.Fatalf("seed permission: %v", err)
	}

	// “重启”：同一数据库、全新的 Service（active / waiters 均为空）。
	svc := newServiceOnDB(t, db, provider)
	if err := svc.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got := waitTaskStatus(t, svc, task.ID, agenttypes.TaskWaitingPermission); got.Status != agenttypes.TaskWaitingPermission {
		t.Fatalf("status after recover = %v", got.Status)
	}

	if err := svc.ApprovePermission(ctx, perm.ID, 1); err != nil {
		t.Fatalf("ApprovePermission: %v", err)
	}
	waitTaskStatus(t, svc, task.ID, agenttypes.TaskCompleted)

	runs, resumed := provider.stats()
	if runs != 1 || resumed != 1 {
		t.Fatalf("provider runs=%d resumed=%d, want 1/1", runs, resumed)
	}

	approved, err := agentrepository.NewGormPermissionRepository(db).Get(ctx, perm.ID)
	if err != nil {
		t.Fatalf("Get permission: %v", err)
	}
	if approved.Status != agenttypes.PermissionConsumed {
		t.Fatalf("permission status = %s, want consumed", approved.Status)
	}
}

// TestManualResumeInterruptedTask 验证运行中任务被重启打断后，可手动触发继续。
func TestManualResumeInterruptedTask(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	provider := &resumeTestProvider{}

	profileSvc := newServiceOnDB(t, db, provider)
	profile := seedResumeProfile(t, profileSvc)
	sessions := agentservice.NewSessionService(agentrepository.NewGormSessionStore(db))
	messages := agentservice.NewMessageService(agentrepository.NewGormMessageStore(db))
	sess := newResumeSession(t, sessions)

	task := agenttypes.NewTask(agenttypes.Request{
		Provider:  resumeTestProviderName,
		ProfileID: profile.ID,
		Messages:  []agenttypes.Message{{Role: agenttypes.RoleUser, Content: "hello"}},
	}, agenttypes.TaskFacts{SessionID: sess.ID})
	task.Status = agenttypes.TaskRunning
	if err := agentrepository.NewGormTaskRepository(db).Create(ctx, task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	seedPendingToolCall(t, messages, sess.ID, task.ID, resumeTestCall)

	svc := newServiceOnDB(t, db, provider)
	// Recover 把没有活跃 goroutine 的 running 任务置为 failed，但仍可续跑。
	if err := svc.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}
	failed := waitTaskStatus(t, svc, task.ID, agenttypes.TaskFailed)
	if !failed.Resumable {
		t.Fatalf("failed task should be resumable")
	}
	if !strings.Contains(failed.Error, "resumable") {
		t.Fatalf("error = %q, want mention of resumable", failed.Error)
	}

	// 手动继续。
	if err := svc.ResumeTask(ctx, task.ID); err != nil {
		t.Fatalf("ResumeTask: %v", err)
	}
	waitTaskStatus(t, svc, task.ID, agenttypes.TaskCompleted)

	if _, resumed := provider.stats(); resumed != 1 {
		t.Fatalf("provider resumed=%d, want 1", resumed)
	}

	// 已完成任务不可再次续跑。
	if err := svc.ResumeTask(ctx, task.ID); !errors.Is(err, agenttypes.ErrTaskNotResumable) {
		t.Fatalf("ResumeTask on completed = %v, want ErrTaskNotResumable", err)
	}
}

// TestRecoverInvokesTaskFinishedHandler 验证重启后 onTaskFinished 仍然生效：
// 即使 ChatService 的内存 turns 为空，也会清理会话 CurrentTaskID 并补写错误消息。
func TestRecoverInvokesTaskFinishedHandler(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	sessions := agentservice.NewSessionService(agentrepository.NewGormSessionStore(db))
	messages := agentservice.NewMessageService(agentrepository.NewGormMessageStore(db))

	sess, err := sessions.Create(ctx, agenttypes.SessionCreateParams{UserID: 1, ProjectID: 1, Title: "t"})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	svc := newServiceOnDB(t, db)
	// 关键：ChatService 在 Recover 之前构造，注册任务终态回调。
	agentservice.NewChatService(svc, sessions, messages, nil, "")

	task := agenttypes.NewTask(agenttypes.Request{Provider: agenttypes.ProviderMock}, agenttypes.TaskFacts{SessionID: sess.ID})
	task.SessionID = sess.ID
	task.Status = agenttypes.TaskRunning
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
		if m.TaskID == task.ID && m.Kind == agenttypes.KindError {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Fatalf("expected an error message for interrupted task, messages=%+v", msgs)
	}
}

// TestCancelKeepsTaskResumable 验证用户手动终止（canceled）后：
//   - 任务仍处于可续跑的中断态；
//   - 会话的 CurrentTaskID 仍指向该任务，前端据此在该轮末尾展示「Continue」；
//   - 点击 Continue 能从 agent_messages 续跑。
func TestCancelKeepsTaskResumable(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	provider := &resumeTestProvider{}

	profileSvc := newServiceOnDB(t, db, provider)
	profile := seedResumeProfile(t, profileSvc)
	sessions := agentservice.NewSessionService(agentrepository.NewGormSessionStore(db))
	messages := agentservice.NewMessageService(agentrepository.NewGormMessageStore(db))
	svc := newServiceOnDB(t, db, provider)
	agentservice.NewChatService(svc, sessions, messages, nil, "")

	sess := newResumeSession(t, sessions)
	task := agenttypes.NewTask(agenttypes.Request{
		Provider:  resumeTestProviderName,
		ProfileID: profile.ID,
		Messages:  []agenttypes.Message{{Role: agenttypes.RoleUser, Content: "hello"}},
	}, agenttypes.TaskFacts{SessionID: sess.ID})
	task.Status = agenttypes.TaskRunning
	if err := agentrepository.NewGormTaskRepository(db).Create(ctx, task); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	seedPendingToolCall(t, messages, sess.ID, task.ID, resumeTestCall)
	sess.CurrentTaskID = task.ID
	if _, err := sessions.Save(ctx, sess); err != nil {
		t.Fatalf("save session: %v", err)
	}

	if err := svc.CancelTask(ctx, task.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	canceled, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if canceled.Status != agenttypes.TaskCanceled {
		t.Fatalf("status = %v, want canceled", canceled.Status)
	}
	if !canceled.Resumable {
		t.Fatalf("canceled task must stay resumable")
	}

	got, err := sessions.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("Get session: %v", err)
	}
	if got.CurrentTaskID != task.ID {
		t.Fatalf("session CurrentTaskID = %d, want %d (resumable canceled task)", got.CurrentTaskID, task.ID)
	}

	// 用户点击 Continue：续跑应该被允许，并从已落库的事实继续。
	if err := svc.ResumeTask(ctx, task.ID); err != nil {
		t.Fatalf("ResumeTask after cancel: %v", err)
	}
	waitTaskStatus(t, svc, task.ID, agenttypes.TaskCompleted)
}
