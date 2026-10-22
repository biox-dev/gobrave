package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/message"
	"github.com/biox-dev/gobrave/internal/agent/skill"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/utils"
)

// AgentService 是 Agent 执行架构的编排层（即 design.md 中的 AgentTaskManager）。
//
// 它把 Client（Provider 门面）、TaskRepository、PermissionManager、EventRepository、
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
	client    *Client
	tasks     TaskRepository
	perms     *PermissionManager
	questions *QuestionManager
	events    EventRepository
	// timeline 把流式完整块事件即时投影为 agent_messages 记录（替代 AgentEvent 落库）。
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
	Client    *Client
	Tasks     TaskRepository
	Perms     *PermissionManager
	Questions *QuestionManager
	Events    EventRepository
	// Messages 是时间线消息（agent_messages）的持久化服务。注入后事件流不再写入
	// AgentEvent，改由 TimelineRecorder 在轮内即时投影为 Message（AgentEvent 后续废除）。
	Messages message.Service
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
		cfg.Policy = DefaultPermissionPolicy()
	}
	return &AgentService{
		client:    cfg.Client,
		tasks:     cfg.Tasks,
		perms:     cfg.Perms,
		questions: cfg.Questions,
		events:    cfg.Events,
		timeline:  NewTimelineRecorder(cfg.Tasks, cfg.Messages),
		bus:       cfg.Bus,
		policy:    cfg.Policy,
		memory:    cfg.Memory,
		profiles:  cfg.Profiles,
		skills:    cfg.Skills,
		active:    make(map[int64]context.CancelFunc),
	}
}

// RunTask 创建任务并异步执行（流式语义）。
//
// 执行与调用方 ctx 解耦：任务在独立 goroutine 中运行，可跨多次 HTTP 请求存活，
// 通过权限确认 / 取消 / 恢复驱动其生命周期。返回的任务对象用于前端轮询 / 订阅。
func (s *AgentService) RunTask(ctx context.Context, req Request, handler StreamHandler) (*Task, error) {
	task, err := s.CreateTask(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := s.StartTask(ctx, task.ID, handler); err != nil {
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
	// 预先记住会话归属，使 Emit 热点路径不需要回查任务。
	s.timeline.Remember(task.ID, task.SessionID)
	s.emitEvent(ctx, task.ID, EventTaskCreated, task)
	return task, nil
}

// StartTask 启动已创建任务的异步执行。
func (s *AgentService) StartTask(ctx context.Context, taskID int64, handler StreamHandler) error {
	task, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return err
	}
	s.timeline.Remember(task.ID, task.SessionID)

	rt := NewTaskRuntime(s, task.ID, handler)
	execCtx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	if _, exists := s.active[taskID]; exists {
		s.mu.Unlock()
		cancel()
		return ErrTaskAlreadyRunning
	}
	s.active[taskID] = cancel
	s.mu.Unlock()

	go s.execute(execCtx, task, rt)
	return nil
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
	s.timeline.Remember(task.ID, task.SessionID)
	// 默认开启自动放行；显式传入的 opts 顺序在后，可覆盖该默认值。
	rt := NewTaskRuntime(s, taskID, nil, append([]RuntimeOption{WithAutoApprove(true)}, opts...)...)
	result, err := s.run(ctx, task, rt)
	if result != nil {
		result.TaskID = taskID
	}
	return result, err
}

// execute 是异步任务的执行体：清理 active 注册后复用 run 执行任务主体。
func (s *AgentService) execute(ctx context.Context, task *Task, rt Runtime) {
	defer func() {
		s.mu.Lock()
		delete(s.active, task.ID)
		s.mu.Unlock()
	}()

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

	task.Request = applyProfileOverrides(task.Request, profile)
	// 系统提示词模板变量取自 Request.RuntimeContext（随 Request 一起持久化进
	// agent_tasks.request，任务从 DB 恢复后依然可用）；同时把运行时上下文传给记忆注入，
	// 使当前项目 / 会话下的记忆优先出现。
	task.Request.SystemPrompt = s.GetSystemPromptByProfileWithContext(ctx, profile, task.Request.RuntimeContext, task.Request.Skills)
	// Profile 可能依据 Profile.Model 覆盖请求级模型，同步回任务持久化字段。
	task.Model = task.Request.ModelID
	if err := s.transitionTask(ctx, task, TaskRunning, ""); err != nil {
		return s.failTask(ctx, task, rt, fmt.Errorf("start task %d: %w", task.ID, err))
	}

	if s.client == nil {
		return s.failTask(ctx, task, rt, fmt.Errorf("agent: no client configured"))
	}

	var result *Result
	// var err error
	if task.Request.Stream {
		result, err = s.client.StreamRuntime(ctx, task.Request, rt)
	} else {
		result, err = s.client.InvokeRuntime(ctx, task.Request, rt)
	}

	switch {
	case err != nil:
		return s.failTask(ctx, task, rt, err)
	case result == nil:
		return s.failTask(ctx, task, rt, fmt.Errorf("agent: empty result"))
	default:
		_ = s.transitionTask(ctx, task, TaskCompleted, "")
		// 任务成功完成后，从本轮执行中提取值得长期记住的记忆。
		s.extractMemory(ctx, task, task.Request, result)
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
		task.ID, task.Request.Provider, task.Request.ModelID, task.Request.RequestSessionID(), err)
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
	}

	if !task.TransitionTo(next) {
		return ErrInvalidTaskTransition
	}
	if err := s.tasks.Update(ctx, task); err != nil {
		return err
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
func (s *AgentService) ApprovePermission(ctx context.Context, id int64, by int64) error {
	p, err := s.perms.Approve(ctx, id, by)
	if err != nil {
		return err
	}
	s.emitEvent(ctx, p.TaskID, EventPermissionResolved, p)
	return nil
}

// DenyPermission 拒绝权限请求（供 UI / HTTP 层调用）。
func (s *AgentService) DenyPermission(ctx context.Context, id int64, by int64) error {
	p, err := s.perms.Deny(ctx, id, by)
	if err != nil {
		return err
	}
	s.emitEvent(ctx, p.TaskID, EventPermissionResolved, p)
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

// GetEvents 增量拉取任务事件（after 为上次收到的最大 sequence）。
func (s *AgentService) GetEvents(ctx context.Context, taskID int64, after int64) ([]*AgentEvent, error) {
	return s.events.ListByTask(ctx, taskID, after)
}

// PageTasks 分页查询任务（offset/limit 由上层根据 types.Pagination 计算）。
func (s *AgentService) PageTasks(ctx context.Context, offset, limit int, statuses ...TaskStatus) ([]*Task, int64, error) {
	return s.tasks.Page(ctx, offset, limit, statuses...)
}

// PagePermissions 分页查询权限请求；taskID 为 0 表示全部任务，statuses 为空表示全部状态。
func (s *AgentService) PagePermissions(ctx context.Context, offset, limit int, taskID int64, statuses ...PermissionStatus) ([]*PermissionRequest, int64, error) {
	return s.perms.Page(ctx, offset, limit, taskID, statuses...)
}

// PageEvents 分页查询事件；taskID 为 0 表示全部任务。
func (s *AgentService) PageEvents(ctx context.Context, offset, limit int, taskID int64) ([]*AgentEvent, int64, error) {
	return s.events.Page(ctx, taskID, offset, limit)
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

// emitEvent 广播一个事件。持久化不再走 EventRepository：时间线改由
// TimelineRecorder 在 EventStream 分支即时投影为 agent_messages。
func (s *AgentService) emitEvent(ctx context.Context, taskID int64, typ AgentEventType, payload any) {
	s.publish(ctx, taskID, typ, payload, true)
}

// broadcastEvent 仅广播一个事件，不持久化（用于 delta 等临时事件）。
func (s *AgentService) broadcastEvent(ctx context.Context, taskID int64, typ AgentEventType, payload any) {
	s.publish(ctx, taskID, typ, payload, false)
}

// publish 是事件的单一出口：构造 AgentEvent，按需投影为时间线消息，再广播给订阅者。
func (s *AgentService) publish(ctx context.Context, taskID int64, typ AgentEventType, payload any, persist bool) {
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
		s.timeline.Record(ctx, taskID, payload)
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
		rendered, err := RenderSystemPromptTemplate(profile.SystemPrompt, vars)
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
		Limit:     defaultMemoryRetrieveLimit,
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

// Recover 在后端重启后重建运行态（见 design.md 第 6 / 19 节）。
//
// 当前内存实现下：
//   - 没有活跃 goroutine 的 running 任务：无法恢复执行，标记为 failed（interrupted）。
//   - 没有活跃 goroutine 的 waiting_permission 任务：pending 权限已持久化，
//     UI 仍可通过 GetPendingPermissions 拉取并批准 / 拒绝（更新 DB）；
//     但 Agent 进程已丢失，真正“恢复执行”依赖各 Provider 的 checkpoint / resume 能力。
//
// 轮内时间线（reasoning / tool_call / tool_result）现在由 TimelineRecorder 即时写入
// agent_messages，恢复时可以据此重建「已经执行到哪一步」；真正的续跑仍待实现
// （需要基于消息表重建 Request + 对已批准权限做一次性重放）。
func (s *AgentService) Recover(ctx context.Context) error {
	running, err := s.tasks.ListByStatus(ctx, TaskRunning)
	if err != nil {
		return err
	}
	for _, t := range running {
		if !s.isActive(t.ID) {
			_ = s.transitionTask(ctx, t, TaskFailed, "backend restarted: task interrupted")
		}
	}

	waiting, err := s.tasks.ListByStatus(ctx, TaskWaitingPermission)
	if err != nil {
		return err
	}
	for _, t := range waiting {
		if s.isActive(t.ID) {
			continue
		}
		// 保留 waiting_permission 状态：pending 权限已持久化，等待 UI 重新处理。
		// 真实恢复执行需 Provider 支持 resume，此处仅保证状态与权限不丢。
		_ = t
	}
	return nil
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
