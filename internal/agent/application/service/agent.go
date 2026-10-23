package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	agentclient "github.com/biox-dev/gobrave/internal/agent/client"
	"github.com/biox-dev/gobrave/internal/agent/logging"
	agentpolicy "github.com/biox-dev/gobrave/internal/agent/policy"
	agentruntime "github.com/biox-dev/gobrave/internal/agent/runtime"
	"github.com/biox-dev/gobrave/internal/agent/skill"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/utils"
)

// AgentService 是 Agent 执行架构的编排层（即 design.md 中的 AgentTaskManager）。
//
// 它把 Client（Provider 门面）、TaskRepository、PermissionManager、
// EventBus 与 PermissionPolicy 组合成一条完整的调用链路：
//
//	RunTask 创建任务 → 启动 goroutine 执行 Agent
//	  → Agent 通过 Runtime.Emit 输出事件（持久化 + 广播）
//	  → Agent 通过 Runtime.RequestPermission 请求权限（持久化 pending + 任务置为 waiting）
//	  → UI 调用 ApprovePermission / DenyPermission（更新 DB + 唤醒 Agent）
//	  → Agent 恢复执行，直至 completed / failed
//
// Recover 用于后端重启后重建运行态。
type AgentService struct {
	client    *agentclient.Client
	tasks     TaskRepository
	perms     *PermissionManager
	questions *QuestionManager
	// timeline 把流式完整块事件即时投影为 agent_messages 记录。
	timeline *TimelineRecorder
	bus      EventBus
	policy   PermissionPolicy
	memory   *MemoryManager
	profiles *ProfileManager
	skills   *skill.Registry

	mu     sync.Mutex
	active map[int64]context.CancelFunc // taskID → 取消函数

	// onTaskFinished 是任务进入终态后的回调（可能为空）。由上层编排层通过
	// SetTaskFinishedHandler 注册，用于把「轮次收尾」绑定到任务状态机。
	onTaskFinished TaskFinishedHandler
}

// ServiceConfig 是 AgentService 的依赖配置。
type ServiceConfig struct {
	Client    *agentclient.Client
	Tasks     TaskRepository
	Perms     *PermissionManager
	Questions *QuestionManager
	// Messages 是时间线消息（agent_messages）的持久化服务。
	Messages MessageService
	Bus      EventBus
	Policy   PermissionPolicy
	Memory   *MemoryManager
	Profiles *ProfileManager
	Skills   *skill.Registry
}

// NewService 创建 AgentService，未提供的依赖使用安全的默认值。
//
// 注意：仓库 / 管理器（Tasks / Events / Perms / Questions / Memory / Profiles）由调用方
// 注入（见 container 的依赖注入配置）。数据库才是状态源，因此这里不再提供内存仓库兜底；
// 仅 EventBus / PermissionPolicy 这类无状态组件提供默认值。
func NewService(cfg ServiceConfig) *AgentService {
	if cfg.Bus == nil {
		cfg.Bus = NewEventBus()
	}
	if cfg.Policy == nil {
		cfg.Policy = agentpolicy.DefaultPermissionPolicy()
	}
	return &AgentService{
		client:    cfg.Client,
		tasks:     cfg.Tasks,
		perms:     cfg.Perms,
		questions: cfg.Questions,
		timeline:  NewTimelineRecorder(cfg.Messages),
		bus:       cfg.Bus,
		policy:    cfg.Policy,
		memory:    cfg.Memory,
		profiles:  cfg.Profiles,
		skills:    cfg.Skills,
		active:    make(map[int64]context.CancelFunc),
	}
}

// RunTask 创建任务并异步执行。
//
// 执行与调用方 ctx 解耦：任务在独立 goroutine 中运行，可跨多次 HTTP 请求存活，
// 通过权限确认 / 取消 / 恢复驱动其生命周期。返回的任务对象用于前端轮询 / 订阅。
//
// 事件消费只有两条通路：实时推送走 EventBus（Subscribe），历史落库走
// TimelineRecorder（agent_messages）；不再接受同步 handler。
func (s *AgentService) RunTask(ctx context.Context, req Request) (*Task, error) {
	task, err := s.CreateTask(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := s.StartTask(ctx, task.ID); err != nil {
		return nil, err
	}
	return task, nil
}

// CreateTask 创建任务（不启动执行）。
//
// 供 HTTP 层使用：先拿到 taskID 订阅事件，再调用 StartTask 启动，避免错过
// task.created 等早期事件。
func (s *AgentService) CreateTask(ctx context.Context, req Request) (*Task, error) {
	task := NewTask(req)
	if err := s.tasks.Create(ctx, task); err != nil {
		return nil, err
	}
	s.emitEvent(ctx, task.ID, EventTaskCreated, task)
	return task, nil
}

// StartTask 启动已创建任务的异步执行。
func (s *AgentService) StartTask(ctx context.Context, taskID int64) error {
	task, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return err
	}
	return s.launch(task, NewTaskRuntime(s, task))
}

// launch 注册活跃执行上下文并在独立 goroutine 中执行任务。
//
// 注册与运行解耦：任务执行不依赖调用方 ctx（HTTP 请求结束不应终止任务），
// 因此这里总是创建独立的 context.Background 派生上下文。
func (s *AgentService) launch(task *Task, rt Runtime) error {
	execCtx, _, ok := s.reserveActive(task.ID)
	if !ok {
		return ErrTaskAlreadyRunning
	}
	go s.execute(execCtx, task, rt)
	return nil
}

// reserveActive 原子地占用任务执行槽；已存在活跃 goroutine 时返回 false。
func (s *AgentService) reserveActive(taskID int64) (context.Context, context.CancelFunc, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.active[taskID]; exists {
		return nil, nil, false
	}
	execCtx, cancel := context.WithCancel(context.Background())
	s.active[taskID] = cancel
	return execCtx, cancel, true
}

// releaseActive 撤销尚未启动的占用（续跑前置校验失败时使用）。
func (s *AgentService) releaseActive(taskID int64, cancel context.CancelFunc) {
	if cancel != nil {
		cancel()
	}
	s.mu.Lock()
	delete(s.active, taskID)
	s.mu.Unlock()
}

// RunTaskSync 同步执行一次性任务：创建 → 运行 → 返回聚合结果。
//
// 供需要一次性拿到结果的业务（如 AISummaryWorker）使用，替代直接调用 agent.Client.Invoke。
// 与 RunTask / StartTask 不同，它不启动 goroutine，直接在调用方同步执行。
//
// 同步执行没有 UI 承接权限确认，因此默认无人值守：权限 ask 直接放行（见 RunTaskSyncByID）。
// 如需保留原有的人工确认语义，可传 WithAutoApprove(false)。
//
// 若调用方需要在任务创建后、执行前拿到 taskID（例如把摘要记录立即关联到任务），
// 请改用 CreateTask + RunTaskSyncByID 的两步形式。
func (s *AgentService) RunTaskSync(ctx context.Context, req Request, opts ...RuntimeOption) (*Result, error) {
	task, err := s.CreateTask(ctx, req)
	if err != nil {
		return nil, err
	}
	return s.RunTaskSyncByID(ctx, task.ID, opts...)
}

// RunTaskSyncByID 同步执行已创建的任务，返回聚合结果。
//
// 与 RunTaskSync 的区别在于任务创建被前置：调用方可以先 CreateTask 拿到 taskID 并完成
// 关联（例如 AISummaryWorker 在生成前把摘要与任务绑定），再调用本方法执行。
//
// 权限：同步执行没有 UI 承接权限确认，默认以「无人值守」方式执行——策略判定为 ask 的操作
// 直接放行，不会持久化 pending 权限请求、也不会把任务置为 waiting_permission 阻塞等待；
// 策略明确 deny 的操作仍会被拒绝。调用方可用 opts 覆盖（如 WithAutoApprove(false) 恢复人工确认）。
func (s *AgentService) RunTaskSyncByID(ctx context.Context, taskID int64, opts ...RuntimeOption) (*Result, error) {
	task, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	// 默认开启自动放行；显式传入的 opts 顺序在后，可覆盖该默认值。
	rt := NewTaskRuntime(s, task, append([]RuntimeOption{WithAutoApprove(true)}, opts...)...)
	result, err := s.run(ctx, task, rt)
	if result != nil {
		result.TaskID = taskID
	}
	return result, err
}

// execute 是异步任务的执行体：清理 active 注册后复用 run 执行任务主体。
//
// panic 必须被转成 failed 终态：否则任务会永远停在 running，且 onTaskFinished 不会触发。
// 检查点仍保留在任务行上，用户可随后通过 ResumeTask 继续。
func (s *AgentService) execute(ctx context.Context, task *Task, rt Runtime) {
	defer func() {
		s.mu.Lock()
		delete(s.active, task.ID)
		s.mu.Unlock()
	}()

	defer logging.RecoverPanic("agent.execute", func() {
		bg := context.WithoutCancel(ctx)
		current, err := s.tasks.Get(bg, task.ID)
		if err != nil || current == nil {
			return
		}
		if isTerminalTaskStatus(current.Status) {
			return
		}
		_ = s.transitionTask(bg, current, TaskFailed, "agent panicked: execution interrupted (resumable)")
	})

	_, _ = s.run(ctx, task, rt)
}

// run 执行任务主体：置为 running → 解析 Provider → 调用 Agent → 更新终态并返回结果。
//
// 异步（execute）与同步（RunTaskSync）共享这一执行路径。
func (s *AgentService) run(ctx context.Context, task *Task, rt Runtime) (*Result, error) {
	// 应用 Agent Profile：按 ID 解析 Profile → 覆盖模型 / Agent / 技能 → 渲染最终系统提示词。
	// 最终系统提示词由 applyProfile 依据 Profile.SystemPrompt 模板 + RuntimeContext.SystemPromptVars
	// 渲染，并按 Profile.Context 开关注入记忆。
	profile, err := s.profiles.Resolve(ctx, task.Request.ProfileID)
	if err != nil {
		return s.failTask(ctx, task, rt, fmt.Errorf("resolve profile %d: %w", task.Request.ProfileID, err))
	}

	// 有效请求在内存中构造；task.Request 保持创建时的原样（审计与恢复重建都依赖它），
	// 只把派生出的模型 / Provider 回写到任务列。
	req := applyProfileOverrides(task.Request, profile)
	req.SystemPrompt = s.GetSystemPromptByProfileWithContext(ctx, profile, req.RuntimeContext, req.Skills)
	task.Model = req.ModelID
	if provider := strings.TrimSpace(req.Provider); provider != "" {
		task.Provider = provider
	}

	if task.Status != TaskRunning {
		if err := s.transitionTask(ctx, task, TaskRunning, ""); err != nil {
			return s.failTask(ctx, task, rt, fmt.Errorf("start task %d: %w", task.ID, err))
		}
	}

	if s.client == nil {
		return s.failTask(ctx, task, rt, fmt.Errorf("agent: no client configured"))
	}

	var result *Result
	if req.Stream {
		result, err = s.client.StreamRuntime(ctx, req, rt)
	} else {
		result, err = s.client.InvokeRuntime(ctx, req, rt)
	}

	switch {
	case err != nil:
		return s.failTask(ctx, task, rt, err)
	case result == nil:
		return s.failTask(ctx, task, rt, fmt.Errorf("agent: empty result"))
	default:
		// Provider 只发增量（没有完整 message 块）时，用最终结果补一条 assistant_final。
		// 必须在 transitionTask 之前：终态会清理时间线缓存。
		s.timeline.RecordResult(ctx, task.SessionID, task.ID, result.Content)
		_ = s.transitionTask(ctx, task, TaskCompleted, "")
		// 任务成功完成后，从本轮执行中提取值得长期记住的记忆。
		s.extractMemory(ctx, task, req, result)
		return result, nil
	}
}

// failTask 统一处理任务失败：记录日志 → 补发可序列化的 error 流事件 → 置为 failed。
//
// 为什么所有失败路径都走这里：会话轮次（ChatService）依赖 done/error 流事件在
// finishTurn 中释放会话级锁并落库错误；若某条路径直接 return err，UI 会一直停留在
// 「运行中」且历史里看不到错误。StreamEvent.Err 带有 json:"-" 无法下发到前端，
// 因此这里同时写入 Content，事件落库 / WS 推送后前端可直接稳定展示。
func (s *AgentService) failTask(ctx context.Context, task *Task, rt Runtime, err error) (*Result, error) {
	if err == nil {
		err = fmt.Errorf("agent: unknown error")
	}
	logger.Errorf(ctx, "agent: task %d failed (provider=%s model=%s session=%d): %v",
		task.ID, task.Provider, task.Model, task.SessionID, err)
	if rt != nil {
		_ = rt.Emit(ctx, StreamEvent{Type: StreamEventError, Content: err.Error(), Err: err})
	}
	_ = s.transitionTask(ctx, task, TaskFailed, err.Error())
	return nil, err
}

// SetTaskFinishedHandler 注册任务终态回调（completed / failed / canceled）。
//
// 典型用法：ChatService 在构造时把自己的「轮次收尾」挂到这里，使会话锁释放、
// 最终回答落库不再依赖 Provider 是否补发 done / error 流事件。
// 回调在任务终态写库后、终态事件广播前同步执行；重复注册会覆盖前一个。
func (s *AgentService) SetTaskFinishedHandler(h TaskFinishedHandler) {
	s.mu.Lock()
	s.onTaskFinished = h
	s.mu.Unlock()
}

// finishedHandler 返回当前注册的终态回调（无锁读取副本，避免回调内反向调用死锁）。
func (s *AgentService) finishedHandler() TaskFinishedHandler {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.onTaskFinished
}

// transitionTask 加载任务 → 校验状态迁移 → 保存 → 广播状态事件。
func (s *AgentService) transitionTask(ctx context.Context, task *Task, next TaskStatus, errMsg string) error {
	if task == nil {
		return fmt.Errorf("task is nil")
	}

	switch next {
	case TaskRunning:
		if task.Status == TaskCreated || task.Status == TaskWaitingPermission {
			now := time.Now()
			task.StartedAt = &now
		}
	case TaskCompleted, TaskFailed, TaskCanceled:
		now := time.Now()
		task.FinishedAt = &now
		if errMsg != "" {
			task.Error = errMsg
		}
		// 正常完成 / 主动取消后不再需要检查点：清理，避免任务被误判为可续跑。
		// failed 保留检查点：无论是崩溃、panic 还是 Provider 临时错误，用户都可以手动续跑。
		if next == TaskCompleted || next == TaskCanceled {
			task.ResumeState = nil
			task.Resumable = false
		}
	}

	// failed 任务可能因为执行期刚写入检查点而具备续跑能力；内存中的 Task 快照
	// 未必包含最新检查点（Agent 循环通过 UpdateResumeState 单独写库），这里回查一次，
	// 使 onTaskFinished 能据此决定是否保留会话的 CurrentTaskID。
	if next == TaskFailed {
		if task.ResumeState == nil {
			if latest, err := s.tasks.Get(ctx, task.ID); err == nil {
				task.ResumeState = latest.ResumeState
			}
		}
		task.Resumable = task.ResumeState != nil
	}

	if !task.TransitionTo(next) {
		return ErrInvalidTaskTransition
	}
	if err := s.tasks.Update(ctx, task); err != nil {
		return err
	}

	// Update 会 Omit resume_state；终态 completed / canceled 需要显式清理检查点。
	// failed 保留检查点，供用户手动续跑。
	if next == TaskCompleted || next == TaskCanceled {
		if err := s.tasks.UpdateResumeState(ctx, task.ID, nil); err != nil {
			logger.Warnf(ctx, "[agent] clear resume state for task %d failed: %v", task.ID, err)
		}
	}

	// 终态后：清理时间线记录器的会话缓存（避免长期运行下无限增长），并通知上层
	// 编排层做轮次收尾。回调放在 emitEvent 之前，保证 UI 收到终态事件时
	// agent_messages 已经是完整的。
	switch next {
	case TaskCompleted, TaskFailed, TaskCanceled:
		s.timeline.Forget(task.ID)
		if h := s.finishedHandler(); h != nil {
			h(ctx, task)
		}
	}

	s.emitEvent(ctx, task.ID, taskEventType(next), task)
	return nil
}

// ApprovePermission 批准权限请求（供 UI / HTTP 层调用）。
//
// 两种情形：
//   - 任务仍在本进程执行（存在 waiter）：Approve 会唤醒 Wait，无需额外动作；
//   - 任务因进程重启而失去 goroutine：批准后立即从持久化检查点真正续跑（ResumeTask）。
func (s *AgentService) ApprovePermission(ctx context.Context, id int64, by int64) error {
	p, err := s.perms.Approve(ctx, id, by)
	if err != nil {
		return err
	}
	s.emitEvent(ctx, p.TaskID, EventPermissionResolved, p)

	if !s.isActive(p.TaskID) {
		s.resumeAfterUserAction(ctx, p.TaskID, "permission approved")
	}
	return nil
}

// DenyPermission 拒绝权限请求（供 UI / HTTP 层调用）。
//
// 若任务已无活跃 goroutine（后端重启），拒绝后直接把等待中的任务置为 failed，
// 保证会话轮次能正常收尾（onTaskFinished），而不是永远停在 waiting_permission。
func (s *AgentService) DenyPermission(ctx context.Context, id int64, by int64) error {
	p, err := s.perms.Deny(ctx, id, by)
	if err != nil {
		return err
	}
	s.emitEvent(ctx, p.TaskID, EventPermissionResolved, p)

	if !s.isActive(p.TaskID) {
		s.failWaitingTask(context.WithoutCancel(ctx), p.TaskID, "permission denied by user")
	}
	return nil
}

// GetTask 查询任务。
func (s *AgentService) GetTask(ctx context.Context, id int64) (*Task, error) {
	return s.tasks.Get(ctx, id)
}

// GetTaskRequest 按任务 ID 加载任务，返回任务持久化的 Request 内容。
//
// 供 UI 调试 / 审计使用。注意：这里返回的是任务创建时的原始请求，未应用 Agent Profile；
// 真正发送给 Provider 的系统提示词在任务执行时由 run 经 applyProfile 渲染
// （Profile 模板 + RuntimeContext.SystemPromptVars + 背景注入）。
func (s *AgentService) GetTaskRequest(ctx context.Context, taskID int64) (*Request, error) {
	task, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	return &task.Request, nil
}

// GetPendingPermissions 查询任务当前待确认的权限请求。
func (s *AgentService) GetPendingPermissions(ctx context.Context, taskID int64) ([]*PermissionRequest, error) {
	return s.perms.GetPending(ctx, taskID)
}

// AnswerQuestion 提交用户对一次提问的作答（供 UI / HTTP 层调用）。
func (s *AgentService) AnswerQuestion(ctx context.Context, id int64, answer AskUserAnswer, by int64) error {
	q, err := s.questions.Answer(ctx, id, answer, by)
	if err != nil {
		return err
	}
	s.emitEvent(ctx, q.TaskID, EventQuestionResolved, q)

	// 与权限同构：只有“本次作答已完成、且没有活跃 goroutine”时才需要真正续跑。
	if q.Status == QuestionAnswered && !s.isActive(q.TaskID) {
		s.resumeAfterUserAction(ctx, q.TaskID, "question answered")
	}
	return nil
}

// GetPendingQuestions 查询任务当前待作答的提问请求。
func (s *AgentService) GetPendingQuestions(ctx context.Context, taskID int64) ([]*QuestionRequest, error) {
	return s.questions.GetPending(ctx, taskID)
}

// PageQuestions 分页查询提问请求；taskID 为 0 表示全部任务，statuses 为空表示全部状态。
func (s *AgentService) PageQuestions(ctx context.Context, offset, limit int, taskID int64, statuses ...QuestionStatus) ([]*QuestionRequest, int64, error) {
	return s.questions.Page(ctx, offset, limit, taskID, statuses...)
}

// PageTasks 分页查询任务（offset/limit 由上层根据 types.Pagination 计算）。
func (s *AgentService) PageTasks(ctx context.Context, offset, limit int, statuses ...TaskStatus) ([]*Task, int64, error) {
	return s.tasks.Page(ctx, offset, limit, statuses...)
}

// PagePermissions 分页查询权限请求；taskID 为 0 表示全部任务，statuses 为空表示全部状态。
func (s *AgentService) PagePermissions(ctx context.Context, offset, limit int, taskID int64, statuses ...PermissionStatus) ([]*PermissionRequest, int64, error) {
	return s.perms.Page(ctx, offset, limit, taskID, statuses...)
}

// Subscribe 订阅任务事件（taskID 为 0 表示订阅全部任务），返回取消订阅函数。
// 供 WS / SSE 实时推送层使用；刷新后的历史请从 agent_messages 读取
// （事件流仅用于实时广播，不再持久化）。
func (s *AgentService) Subscribe(taskID int64, handler EventHandler) func() {
	if s.bus == nil {
		return func() {}
	}
	return s.bus.Subscribe(taskID, handler)
}

// emitEvent 广播一个事件（不落库）。时间线由 TimelineRecorder 在 EventStream 分支
// 即时投影为 agent_messages；sessionID 仅流式事件需要，其余事件传 0。
func (s *AgentService) emitEvent(ctx context.Context, taskID int64, typ AgentEventType, payload any) {
	s.publish(ctx, taskID, 0, typ, payload, true)
}

// broadcastEvent 仅广播一个事件，不持久化（用于 delta 等临时事件）。
func (s *AgentService) broadcastEvent(ctx context.Context, taskID int64, typ AgentEventType, payload any) {
	s.publish(ctx, taskID, 0, typ, payload, false)
}

// publish 是事件的单一出口：构造 AgentEvent，按需投影为时间线消息，再广播给订阅者。
func (s *AgentService) publish(ctx context.Context, taskID, sessionID int64, typ AgentEventType, payload any, persist bool) {
	// 每个事件都分配唯一 ID：持久化事件由 BeforeCreate 兜底，但广播（不落库）的
	// 流式增量事件不走 Append，若不在此分配，ID 恒为 0，前端按 id:sequence 去重
	// 会把同一任务的所有 stream 增量判为重复而丢弃，导致实时流只显示首个增量。
	e := &AgentEvent{
		ID:        utils.GenerateID(),
		TaskID:    taskID,
		Type:      typ,
		Payload:   payload,
		CreatedAt: time.Now(),
	}
	// 时间线持久化已从 AgentEvent 迁到 Message（agent_messages）：事件流只保留
	// 实时广播职责，AgentEvent 后续整体废除。流式事件由 TimelineRecorder 在轮内
	// 即时投影为 agent_messages 记录（reasoning / tool_call / tool_result）。
	// if persist && s.events != nil {
	// 	_ = s.events.Append(ctx, e)
	// }
	if persist && typ == EventStream {
		s.timeline.Record(ctx, sessionID, taskID, payload)
	}
	if s.bus != nil {
		s.bus.Publish(ctx, *e)
	}
}

// ---- Memory 调用面（供 HTTP / 上层业务操作记忆） ----

// SaveMemory 创建或更新一条记忆，并广播记忆事件（实时通知层）。
func (s *AgentService) SaveMemory(ctx context.Context, memory *Memory) error {
	if s.memory == nil {
		return ErrMemoryNotConfigured
	}
	if err := s.memory.Save(ctx, memory); err != nil {
		return err
	}
	// 记忆是用户级（跨任务）对象，不归属某个任务的时间线，因此仅实时广播、不落事件流。
	s.broadcastEvent(ctx, 0, EventMemorySaved, memory)
	return nil
}

// GetMemory 按 ID 查询单条记忆。
func (s *AgentService) GetMemory(ctx context.Context, id int64) (*Memory, error) {
	if s.memory == nil {
		return nil, ErrMemoryNotConfigured
	}
	return s.memory.Get(ctx, id)
}

// DeleteMemory 删除记忆，并广播记忆事件。
func (s *AgentService) DeleteMemory(ctx context.Context, id int64) error {
	if s.memory == nil {
		return ErrMemoryNotConfigured
	}
	mem, err := s.memory.Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.memory.Delete(ctx, id); err != nil {
		return err
	}
	s.broadcastEvent(ctx, 0, EventMemoryDeleted, mem)
	return nil
}

// ListMemory 分页查询某用户的记忆；kinds 为空表示全部类别。
func (s *AgentService) ListMemory(ctx context.Context, userID int64, offset, limit int, kinds ...MemoryKind) ([]*Memory, int64, error) {
	if s.memory == nil {
		return nil, 0, ErrMemoryNotConfigured
	}
	return s.memory.List(ctx, userID, offset, limit, kinds...)
}

// RetrieveMemory 检索与 query 相关的记忆。
func (s *AgentService) RetrieveMemory(ctx context.Context, userID int64, query string, limit int) ([]*Memory, error) {
	if s.memory == nil {
		return nil, ErrMemoryNotConfigured
	}
	return s.memory.Retrieve(ctx, MemoryQuery{UserID: userID, Text: query, Limit: limit})
}

// ---- Profile 调用面（供 HTTP / 上层业务管理 Agent Profile） ----

// ListProfiles 返回全部 Profile（内置 + 自定义，全局可见）。
func (s *AgentService) ListProfiles(ctx context.Context) ([]*Profile, error) {
	if s.profiles == nil {
		return nil, ErrProfileNotFound
	}
	return s.profiles.List(ctx)
}

// GetProfile 按 ID 返回 Profile（全局可见）。
func (s *AgentService) GetProfile(ctx context.Context, id int64) (*Profile, error) {
	if s.profiles == nil {
		return nil, ErrProfileNotFound
	}
	return s.profiles.Get(ctx, id)
}

// ResolveProfile 按 ID 解析 Profile，与 run 内应用 Profile 的解析路径保持一致
// （id 为 0 时回退到内置默认 Profile）。
func (s *AgentService) ResolveProfile(ctx context.Context, id int64) (*Profile, error) {
	if s.profiles == nil {
		return nil, ErrProfileNotFound
	}
	return s.profiles.Resolve(ctx, id)
}

// SaveProfile 创建或更新 Profile（内置 Profile 也允许更新）。
func (s *AgentService) SaveProfile(ctx context.Context, profile *Profile) error {
	if s.profiles == nil {
		return ErrProfileNotFound
	}
	return s.profiles.Save(ctx, profile)
}

// DeleteProfile 删除自定义 Profile（内置 Profile 不可删除）。
func (s *AgentService) DeleteProfile(ctx context.Context, id int64) error {
	if s.profiles == nil {
		return ErrProfileNotFound
	}
	return s.profiles.Delete(ctx, id)
}

// RenderSystemPrompt 按 Profile ID 解析 Profile，并用 vars 渲染其系统提示词模板，
// 返回真正的系统提示词（供 DescribeEnv 等前端展示 / 调试场景使用）。
//
// Profile 不存在或未配置系统提示词时返回空串、nil 错误。
func (s *AgentService) RenderSystemPrompt(ctx context.Context, userID int64, profileID int64, vars map[string]any) (*Profile, string, error) {
	if s.profiles == nil {
		return nil, "", nil
	}
	profile, err := s.profiles.Resolve(ctx, profileID)
	if err != nil || profile == nil {
		return nil, "", nil
	}
	// 只提供用户 ID（无业务上下文）：补一个仅含用户事实的 RuntimeContext，
	// 使记忆注入能按用户维度检索。
	var rc *tool.RuntimeContext
	if userID > 0 {
		rc = &tool.RuntimeContext{UserID: userID}
	}
	systemPrompt := s.GetSystemPromptByProfileWithContext(ctx, profile, rc, profile.Skills)

	// return RenderSystemPromptTemplate(profile.SystemPrompt, vars)
	return profile, systemPrompt, nil
}

// resolveProfile 解析本次调用应使用的 Profile：name 为空时回退到内置默认 Profile
// （保证系统提示词含的工作目录等模板变量始终可用）。
//
// 未配置 ProfileManager 或解析失败时返回 nil，调用方按「无 Profile」处理。
// func (s *AgentService) resolveProfile(ctx context.Context, name string) *Profile {
// 	if s.profiles == nil {
// 		return nil
// 	}
// if strings.TrimSpace(name) == "" {
// 	name = DefaultProfileName
// }

// 	return profile
// }

// applyProfileOverrides 用 Profile 覆盖请求级的模型 / Agent（Provider）/ 技能选择。
//
// 覆盖规则：Profile 中非空的字段优先，为空时保留 req 原值（作为兜底）。
func applyProfileOverrides(req Request, profile *Profile) Request {
	if profile == nil {
		return req
	}

	// 模型选择：Profile 指定了模型时，以 Profile 为准（req.ModelID 作为兜底）。
	if model := strings.TrimSpace(profile.Model); model != "" {
		req.ModelID = model
	}

	// Agent（Provider）选择：Profile 指定了 Agent 时，以 Profile 为准。
	if provider := strings.TrimSpace(profile.Provider); provider != "" {
		req.Provider = provider
	}

	// 技能选择：Profile.Skills 非空时限制本次调用可见的技能集合。
	if len(profile.Skills) > 0 {
		req.Skills = profile.Skills
	}

	return req
}

// applyProfile 依据 Profile 生成本轮最终的 SystemPrompt。
//
// 入参：
//   - profile：已解析的 Agent Profile（为 nil 时返回空提示词，表示不附加系统提示词）；
//   - vars：渲染 Profile.SystemPrompt（pongo2 模板）所用的变量，通常为 RuntimeContext.SystemPromptVars；
//   - userID：记忆的归属用户（为空时不注入背景）；
//   - selectedSkills：本轮启用的技能名（为空表示全部）；据此把技能元信息（name /
//     description）注入提示词，正文由模型调用 get_skill 工具按需加载。
//
// 处理顺序：渲染 Profile 模板 → 注入技能指令 → 按 Profile.Context 开关注入记忆。
// 模板渲染失败时退回原始模板，保证调用仍可进行。
// GetSystemSpromptByProfile 按 Profile 渲染系统提示词，并按需注入技能说明与记忆背景。
//
// vars 是模板变量（来自 RuntimeContext.SystemPromptVars）；记忆注入的用户 / 上下文维度
// 需要 RuntimeContext，因此没有业务场景的调用请使用
// GetSystemPromptByProfileWithContext（或 RenderSystemPrompt）。
func (s *AgentService) GetSystemSpromptByProfile(ctx context.Context, profile *Profile, vars map[string]any, selectedSkills []string) string {
	return s.buildSystemPrompt(ctx, profile, vars, nil, selectedSkills)
}

// GetSystemPromptByProfileWithContext 按 Profile 渲染系统提示词，并带上本次调用的
// 运行时上下文（模板变量 / 项目 / 会话 / 归属用户），使记忆注入优先命中所属上下文。
//
// rc 可为 nil（此时不做记忆注入，也不渲染模板变量之外的内容）。
func (s *AgentService) GetSystemPromptByProfileWithContext(ctx context.Context, profile *Profile, rc *tool.RuntimeContext, selectedSkills []string) string {
	var vars map[string]any
	if rc != nil {
		vars = rc.Vars()
	}
	return s.buildSystemPrompt(ctx, profile, vars, rc, selectedSkills)
}

// buildSystemPrompt 是系统提示词渲染的单一实现：模板渲染 → 技能说明 → 记忆背景。
func (s *AgentService) buildSystemPrompt(ctx context.Context, profile *Profile, vars map[string]any, rc *tool.RuntimeContext, selectedSkills []string) string {
	if profile == nil {
		return ""
	}

	// 0) 基础编码提示词：所有 Profile 共用，固定置于系统提示词最前面
	// （内容来自 builtin/prompts/base_coder.md）。
	prompt := strings.TrimSpace(mustReadBuiltinPrompt(BuiltinPromptBaseCoder))

	// 1) 系统提示词：来源于 Profile —— 用调用方传入的变量渲染 Profile 的模板，
	// 拼接在基础编码提示词之后。
	if tpl := strings.TrimSpace(profile.SystemPrompt); tpl != "" {
		rendered, err := agentruntime.RenderSystemPromptTemplate(profile.SystemPrompt, vars)
		if err != nil {
			prompt = appendPromptBlock(prompt, tpl)
		} else {
			prompt = appendPromptBlock(prompt, rendered)
		}
	}

	// 2) 技能元信息：把本轮可用技能的名称与描述注入提示词，使模型知道有哪些技能、
	// 何时该调用 get_skill；正文与附属资源由技能工具在执行期按需加载（见 providers）。
	if s.skills != nil && len(selectedSkills) > 0 {
		prompt = appendPromptBlock(prompt, s.skills.PromptBlock(selectedSkills...))
	}

	// 3) 背景注入：按 Profile 声明的开关执行。
	// 用户 / 项目 / 会话维度全部取自 RuntimeContext，rc 为 nil 时跳过注入。
	if profile.Context.InjectMemory {
		prompt = s.injectMemoryWithContext(ctx, prompt, rc)
	}

	return prompt
}

// injectMemoryWithContext 按「用户 + 业务上下文」检索记忆并注入系统提示词。
//
// 归属用户、项目、会话全部取自 RuntimeContext（它是请求级事实的唯一来源，见
// Request.RequestUserID）：当前项目 / 当前会话下写入的注意事项（如 save_memory 记录的
// 工具使用避坑点）优先被注入，其余记忆作为补充；仓库不支持上下文优先时自动退化为
// 普通检索。
//
// rc 为 nil 或 UserID 为空（后台任务等无用户场景）时不做注入，原样返回 prompt。
func (s *AgentService) injectMemoryWithContext(ctx context.Context, prompt string, rc *tool.RuntimeContext) string {
	if s.memory == nil || rc == nil || rc.UserID == 0 {
		return prompt
	}
	mems, err := s.memory.Retrieve(ctx, MemoryQuery{
		UserID:    rc.UserID,
		ProjectID: rc.ProjectID,
		SessionID: rc.SessionID,
		Limit:     DefaultMemoryRetrieveLimit,
	})
	if err != nil || len(mems) == 0 {
		return prompt
	}
	return appendPromptBlock(prompt, BuildMemoryContext(mems))
}

// appendPromptBlock 把上下文块追加到系统提示词尾部；块为空时原样返回。
func appendPromptBlock(prompt, block string) string {
	if block == "" {
		return prompt
	}
	if prompt == "" {
		return block
	}
	return prompt + "\n\n" + block
}

// extractMemory 在任务成功完成后调用记忆提取器，把提取出的记忆写入 Repository 并广播。
//
// 提取器未配置或提取失败时静默跳过（记忆提取是尽力而为的旁路，不应影响任务结果）。
func (s *AgentService) extractMemory(ctx context.Context, task *Task, req Request, result *Result) {
	userID := req.RequestUserID()
	if s.memory == nil || userID == 0 {
		return
	}
	mems, err := s.memory.Extract(ctx, MemoryTurn{
		UserID:    userID,
		ProjectID: req.RuntimeContext.ProjectID,
		SessionID: req.RequestSessionID(),
		Request:   req,
		Result:    result,
	})
	if err != nil {
		return
	}
	for _, mem := range mems {
		if err := s.memory.Save(ctx, mem); err != nil {
			continue
		}
		s.broadcastEvent(ctx, task.ID, EventMemorySaved, mem)
	}
}

// CancelTask 取消任务：取消执行上下文并置为 canceled。
func (s *AgentService) CancelTask(ctx context.Context, taskID int64) error {
	s.mu.Lock()
	cancel, ok := s.active[taskID]
	s.mu.Unlock()
	if ok {
		cancel()
	}
	task, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return err
	}
	return s.transitionTask(ctx, task, TaskCanceled, "canceled by user")
}

// ResumeTask 从持久化检查点续跑任务（供 UI 手动触发，或权限 / 提问决策后自动触发）。
//
// 前置条件：
//   - 任务存在检查点（Provider 在执行期写入）；
//   - 任务无活跃 goroutine（否则返回 ErrTaskAlreadyRunning）；
//   - 任务不是 completed / canceled（终态不可续跑）。
//
// 执行链：MarkResumed → 持久化 running → 启动 goroutine → Provider 读取检查点，
// 重放未完成的工具调用（已批准的权限会被 consume 后直接放行），随后继续 Agent 循环。
func (s *AgentService) ResumeTask(ctx context.Context, taskID int64) error {
	task, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return err
	}
	if task.ResumeState == nil {
		return ErrTaskNotResumable
	}
	if task.Status == TaskCompleted || task.Status == TaskCanceled || task.Status == TaskCreated {
		return ErrTaskNotResumable
	}

	// 先占用执行槽：两个并发恢复请求（如双击“继续”）只有一个能成功。
	execCtx, cancel, ok := s.reserveActive(taskID)
	if !ok {
		return ErrTaskAlreadyRunning
	}
	launched := false
	defer func() {
		if !launched {
			s.releaseActive(taskID, cancel)
		}
	}()

	if !task.MarkResumed() {
		return ErrTaskNotResumable
	}
	if err := s.tasks.Update(ctx, task); err != nil {
		return err
	}

	s.emitEvent(ctx, task.ID, EventTaskStarted, task)

	go s.execute(execCtx, task, NewTaskRuntime(s, task))
	launched = true
	return nil
}

// resumeAfterUserAction 在用户对权限 / 提问作出决策后尝试真正续跑任务。
//
// 只有“没有活跃 goroutine”的路径才会走到这里（后端重启 / 进程崩溃）。若任务没有
// 检查点（Provider 不支持续跑），把等待中的任务置为 failed，避免 UI 永久挂起。
func (s *AgentService) resumeAfterUserAction(ctx context.Context, taskID int64, reason string) {
	resumeCtx := context.WithoutCancel(ctx)
	if err := s.ResumeTask(resumeCtx, taskID); err != nil {
		if errors.Is(err, ErrTaskAlreadyRunning) {
			return
		}
		logger.Warnf(resumeCtx, "[agent] %s: resume task %d failed: %v", reason, taskID, err)
		s.failWaitingTask(resumeCtx, taskID, "task cannot be resumed: "+err.Error())
	}
}

// failWaitingTask 把仍在等待用户输入、但已经无法续跑的任务落到 failed 终态。
func (s *AgentService) failWaitingTask(ctx context.Context, taskID int64, reason string) {
	task, err := s.tasks.Get(ctx, taskID)
	if err != nil || task == nil {
		return
	}
	if task.Status == TaskWaitingPermission {
		_ = s.transitionTask(ctx, task, TaskFailed, reason)
	}
}

// Recover 在后端重启后重建运行态（见 design.md 第 6 / 19 节）。
//
// 行为：
//   - running 且无活跃 goroutine：执行循环已随进程消失，置为 failed（保留检查点，
//     用户可通过 ResumeTask 手动续跑；错误信息标注 resumable）；
//   - waiting_permission 且无活跃 goroutine：保留状态与 pending 权限，等待用户操作；
//     批准后 ApprovePermission 会自动 ResumeTask，拒绝则直接置为 failed。
//
// 注意：running → failed 会触发 onTaskFinished。ChatService 的收尾逻辑不依赖
// 内存 turns 表（重启后为空），而是用 Task.SessionID 从数据库恢复收尾，因此
// 会话的 CurrentTaskID / 错误消息仍能被正确清理。
func (s *AgentService) Recover(ctx context.Context) error {
	running, err := s.tasks.ListByStatus(ctx, TaskRunning)
	if err != nil {
		return err
	}
	for _, t := range running {
		if s.isActive(t.ID) {
			continue
		}
		reason := "backend restarted: task interrupted"
		if t.ResumeState != nil {
			reason += " (resumable)"
		}
		_ = s.transitionTask(ctx, t, TaskFailed, reason)
	}

	waiting, err := s.tasks.ListByStatus(ctx, TaskWaitingPermission)
	if err != nil {
		return err
	}
	for _, t := range waiting {
		if s.isActive(t.ID) {
			continue
		}

		// 若任务仍有待处理的权限 / 提问：保留 waiting_permission，等待 UI 操作。
		// 用户体验：批准 → 自动续跑；拒绝 → 直接失败收尾。
		pendingPerms, perr := s.pendingPermissionCount(ctx, t.ID)
		pendingQuestions, qerr := s.pendingQuestionCount(ctx, t.ID)
		if perr != nil || qerr != nil {
			continue
		}
		if pendingPerms > 0 || pendingQuestions > 0 {
			continue
		}

		// 没有任何待处理交互却仍在 waiting：说明用户在上次进程退出前已经作出决策，
		// 但执行循环没能恢复。有检查点时直接自动续跑；否则落到 failed（触发收尾回调），
		// 避免任务永久悬挂。
		if t.ResumeState != nil {
			go func(id int64) {
				if err := s.ResumeTask(context.Background(), id); err != nil {
					logger.Warnf(context.Background(), "[agent] recover resume task %d failed: %v", id, err)
				}
			}(t.ID)
			continue
		}
		_ = s.transitionTask(ctx, t, TaskFailed, "backend restarted: task was waiting but has no pending permission or question")
	}
	return nil
}

// pendingPermissionCount 统计任务当前的待确认权限数（未配置权限管理器时返回 0）。
func (s *AgentService) pendingPermissionCount(ctx context.Context, taskID int64) (int, error) {
	if s.perms == nil {
		return 0, nil
	}
	list, err := s.perms.GetPending(ctx, taskID)
	if err != nil {
		return 0, err
	}
	return len(list), nil
}

// pendingQuestionCount 统计任务当前的待作答提问数（未配置提问管理器时返回 0）。
func (s *AgentService) pendingQuestionCount(ctx context.Context, taskID int64) (int, error) {
	if s.questions == nil {
		return 0, nil
	}
	list, err := s.questions.GetPending(ctx, taskID)
	if err != nil {
		return 0, err
	}
	return len(list), nil
}

// isTerminalTaskStatus 判断任务是否已进入终态。
func isTerminalTaskStatus(status TaskStatus) bool {
	return status == TaskCompleted || status == TaskFailed || status == TaskCanceled
}

func (s *AgentService) isActive(taskID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.active[taskID]
	return ok
}

// taskEventType 把任务终态映射为事件类型。
func taskEventType(status TaskStatus) AgentEventType {
	switch status {
	case TaskRunning:
		return EventTaskStarted
	case TaskWaitingPermission:
		return EventTaskWaiting
	case TaskCompleted:
		return EventTaskCompleted
	case TaskFailed:
		return EventTaskFailed
	case TaskCanceled:
		return EventTaskCanceled
	default:
		return EventTaskCreated
	}
}
