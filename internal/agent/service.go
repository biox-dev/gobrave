package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
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
	client   *Client
	tasks    TaskRepository
	perms    *PermissionManager
	events   EventRepository
	bus      EventBus
	policy   PermissionPolicy
	memory   *MemoryManager
	project  ProjectContextProvider
	profiles *ProfileManager

	mu     sync.Mutex
	active map[string]context.CancelFunc // taskID → 取消函数
}

// ServiceConfig 是 AgentService 的依赖配置。
type ServiceConfig struct {
	Client   *Client
	Tasks    TaskRepository
	Perms    *PermissionManager
	Events   EventRepository
	Bus      EventBus
	Policy   PermissionPolicy
	Memory   *MemoryManager
	Project  ProjectContextProvider
	Profiles *ProfileManager
}

// NewService 创建 AgentService，未提供的依赖使用安全的默认值。
func NewService(cfg ServiceConfig) *AgentService {
	if cfg.Tasks == nil {
		cfg.Tasks = NewMemoryTaskRepository()
	}
	if cfg.Events == nil {
		cfg.Events = NewMemoryEventRepository()
	}
	if cfg.Bus == nil {
		cfg.Bus = NewEventBus()
	}
	if cfg.Policy == nil {
		cfg.Policy = DefaultPermissionPolicy()
	}
	if cfg.Perms == nil {
		cfg.Perms = NewPermissionManager(NewMemoryPermissionRepository())
	}
	if cfg.Memory == nil {
		cfg.Memory = NewMemoryManager(MemoryConfig{})
	}
	if cfg.Profiles == nil {
		cfg.Profiles = NewProfileManager(NewMemoryProfileRepository())
	}
	return &AgentService{
		client:   cfg.Client,
		tasks:    cfg.Tasks,
		perms:    cfg.Perms,
		events:   cfg.Events,
		bus:      cfg.Bus,
		policy:   cfg.Policy,
		memory:   cfg.Memory,
		project:  cfg.Project,
		profiles: cfg.Profiles,
		active:   make(map[string]context.CancelFunc),
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
	s.emitEvent(ctx, task.ID, EventTaskCreated, task)
	return task, nil
}

// StartTask 启动已创建任务的异步执行。
func (s *AgentService) StartTask(ctx context.Context, taskID string, handler StreamHandler) error {
	task, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return err
	}

	rt := s.newTaskRuntime(task.ID, handler)
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
func (s *AgentService) RunTaskSync(ctx context.Context, req Request) (*Result, error) {
	task := NewTask(req)
	if err := s.tasks.Create(ctx, task); err != nil {
		return nil, err
	}
	s.emitEvent(ctx, task.ID, EventTaskCreated, task)

	return s.run(ctx, task, s.newTaskRuntime(task.ID, nil))
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
	if err := s.transitionTask(ctx, task.ID, TaskRunning, ""); err != nil {
		return nil, err
	}

	if s.client == nil {
		_ = s.transitionTask(ctx, task.ID, TaskFailed, "no agent client configured")
		return nil, fmt.Errorf("agent: no client configured")
	}

	// 应用 Agent Profile：解析 → 合并系统提示词 → 选择技能 → 按开关注入背景。
	req := s.applyProfile(ctx, task.Request)

	var result *Result
	var err error
	if req.Stream {
		result, err = s.client.StreamRuntime(ctx, req, rt)
	} else {
		result, err = s.client.InvokeRuntime(ctx, req, rt)
	}

	switch {
	case err != nil:
		_ = s.transitionTask(ctx, task.ID, TaskFailed, err.Error())
		return nil, err
	case result == nil:
		_ = s.transitionTask(ctx, task.ID, TaskFailed, "agent returned empty result")
		return nil, fmt.Errorf("agent: empty result")
	default:
		_ = s.transitionTask(ctx, task.ID, TaskCompleted, "")
		// 任务成功完成后，从本轮执行中提取值得长期记住的记忆。
		s.extractMemory(ctx, task, req, result)
		return result, nil
	}
}

// transitionTask 加载任务 → 校验状态迁移 → 保存 → 广播状态事件。
func (s *AgentService) transitionTask(ctx context.Context, taskID string, next TaskStatus, errMsg string) error {
	task, err := s.tasks.Get(ctx, taskID)
	if err != nil {
		return err
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

	s.emitEvent(ctx, taskID, taskEventType(next), task)
	return nil
}

// ApprovePermission 批准权限请求（供 UI / HTTP 层调用）。
func (s *AgentService) ApprovePermission(ctx context.Context, id, by string) error {
	p, err := s.perms.Approve(ctx, id, by)
	if err != nil {
		return err
	}
	s.emitEvent(ctx, p.TaskID, EventPermissionResolved, p)
	return nil
}

// DenyPermission 拒绝权限请求（供 UI / HTTP 层调用）。
func (s *AgentService) DenyPermission(ctx context.Context, id, by string) error {
	p, err := s.perms.Deny(ctx, id, by)
	if err != nil {
		return err
	}
	s.emitEvent(ctx, p.TaskID, EventPermissionResolved, p)
	return nil
}

// GetTask 查询任务。
func (s *AgentService) GetTask(ctx context.Context, id string) (*Task, error) {
	return s.tasks.Get(ctx, id)
}

// ProjectContext 返回当前用户激活项目下的上下文文本块（例如已完成的分析节点）。
//
// 未配置项目上下文提供者或用户为空时返回空串；查询失败时返回错误。
// 供 HTTP 层直接向用户展示注入 Agent 系统提示词的项目背景。
func (s *AgentService) ProjectContext(ctx context.Context, userID string) (string, error) {
	if s.project == nil || strings.TrimSpace(userID) == "" {
		return "", nil
	}
	return s.project.ProjectContext(ctx, userID)
}

// GetPendingPermissions 查询任务当前待确认的权限请求。
func (s *AgentService) GetPendingPermissions(ctx context.Context, taskID string) ([]*PermissionRequest, error) {
	return s.perms.GetPending(ctx, taskID)
}

// GetEvents 增量拉取任务事件（after 为上次收到的最大 sequence）。
func (s *AgentService) GetEvents(ctx context.Context, taskID string, after int64) ([]*AgentEvent, error) {
	return s.events.ListByTask(ctx, taskID, after)
}

// PageTasks 分页查询任务（offset/limit 由上层根据 types.Pagination 计算）。
func (s *AgentService) PageTasks(ctx context.Context, offset, limit int, statuses ...TaskStatus) ([]*Task, int64, error) {
	return s.tasks.Page(ctx, offset, limit, statuses...)
}

// PagePermissions 分页查询权限请求；taskID 为空表示全部任务，statuses 为空表示全部状态。
func (s *AgentService) PagePermissions(ctx context.Context, offset, limit int, taskID string, statuses ...PermissionStatus) ([]*PermissionRequest, int64, error) {
	return s.perms.Page(ctx, offset, limit, taskID, statuses...)
}

// PageEvents 分页查询事件；taskID 为空表示全部任务。
func (s *AgentService) PageEvents(ctx context.Context, offset, limit int, taskID string) ([]*AgentEvent, int64, error) {
	return s.events.Page(ctx, taskID, offset, limit)
}

// Subscribe 订阅任务事件（taskID 为空表示订阅全部任务），返回取消订阅函数。
// 供 WS / SSE 实时推送层使用；刷新恢复历史请使用 GetEvents。
func (s *AgentService) Subscribe(taskID string, handler EventHandler) func() {
	if s.bus == nil {
		return func() {}
	}
	return s.bus.Subscribe(taskID, handler)
}

// emitEvent 持久化（分配 sequence）并广播一个事件。
func (s *AgentService) emitEvent(ctx context.Context, taskID string, typ AgentEventType, payload any) {
	s.publish(ctx, taskID, typ, payload, true)
}

// broadcastEvent 仅广播一个事件，不持久化（用于 delta 等临时事件）。
func (s *AgentService) broadcastEvent(ctx context.Context, taskID string, typ AgentEventType, payload any) {
	s.publish(ctx, taskID, typ, payload, false)
}

// publish 是事件的单一出口：构造 AgentEvent，按需持久化，再广播给订阅者。
func (s *AgentService) publish(ctx context.Context, taskID string, typ AgentEventType, payload any, persist bool) {
	e := &AgentEvent{
		ID:        newID("evt"),
		TaskID:    taskID,
		Type:      typ,
		Payload:   payload,
		CreatedAt: time.Now(),
	}
	if persist && s.events != nil {
		_ = s.events.Append(ctx, e)
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
	s.broadcastEvent(ctx, "", EventMemorySaved, memory)
	return nil
}

// GetMemory 按 ID 查询单条记忆。
func (s *AgentService) GetMemory(ctx context.Context, id string) (*Memory, error) {
	if s.memory == nil {
		return nil, ErrMemoryNotConfigured
	}
	return s.memory.Get(ctx, id)
}

// DeleteMemory 删除记忆，并广播记忆事件。
func (s *AgentService) DeleteMemory(ctx context.Context, id string) error {
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
	s.broadcastEvent(ctx, "", EventMemoryDeleted, mem)
	return nil
}

// ListMemory 分页查询某用户的记忆；kinds 为空表示全部类别。
func (s *AgentService) ListMemory(ctx context.Context, userID string, offset, limit int, kinds ...MemoryKind) ([]*Memory, int64, error) {
	if s.memory == nil {
		return nil, 0, ErrMemoryNotConfigured
	}
	return s.memory.List(ctx, userID, offset, limit, kinds...)
}

// RetrieveMemory 检索与 query 相关的记忆。
func (s *AgentService) RetrieveMemory(ctx context.Context, userID, query string, limit int) ([]*Memory, error) {
	if s.memory == nil {
		return nil, ErrMemoryNotConfigured
	}
	return s.memory.Retrieve(ctx, userID, query, limit)
}

// ---- Profile 调用面（供 HTTP / 上层业务管理 Agent Profile） ----

// ListProfiles 返回内置与当前用户自定义的 Profile 列表（合并视图）。
func (s *AgentService) ListProfiles(ctx context.Context, userID string) ([]*Profile, error) {
	if s.profiles == nil {
		return nil, ErrProfileNotFound
	}
	return s.profiles.List(ctx, userID)
}

// GetProfile 按 ID 返回当前用户的自定义 Profile（内置 Profile 按 ID 不可取）。
func (s *AgentService) GetProfile(ctx context.Context, userID, id string) (*Profile, error) {
	if s.profiles == nil {
		return nil, ErrProfileNotFound
	}
	return s.profiles.Get(ctx, userID, id)
}

// SaveProfile 创建或更新用户自定义 Profile。
func (s *AgentService) SaveProfile(ctx context.Context, profile *Profile) error {
	if s.profiles == nil {
		return ErrProfileNotFound
	}
	return s.profiles.Save(ctx, profile)
}

// DeleteProfile 删除用户自定义 Profile（内置 Profile 不可删除）。
func (s *AgentService) DeleteProfile(ctx context.Context, userID, id string) error {
	if s.profiles == nil {
		return ErrProfileNotFound
	}
	return s.profiles.Delete(ctx, userID, id)
}

// applyProfile 在调用前应用 Agent Profile：
//
//  1. 解析 Profile（未指定名称时取默认；解析失败回退内置默认）；
//  2. 把 Profile 的系统提示词作为基础合并进请求；
//  3. Profile 指定了技能时，限制本次调用可见的技能集合；
//  4. 按 Profile 的上下文开关决定是否注入记忆 / 项目上下文。
func (s *AgentService) applyProfile(ctx context.Context, req Request) Request {
	var profile *Profile
	if s.profiles != nil {
		if p, err := s.profiles.Resolve(ctx, req.UserID, req.Profile); err == nil {
			profile = p
		}
	}
	if profile == nil {
		profile = DefaultBuiltinProfile()
	}

	// 1) 系统提示词：Profile 基础提示词在前，请求已有提示词（运行时上下文等）在后。
	if strings.TrimSpace(profile.SystemPrompt) != "" {
		if strings.TrimSpace(req.SystemPrompt) == "" {
			req.SystemPrompt = profile.SystemPrompt
		} else {
			req.SystemPrompt = profile.SystemPrompt + "\n\n" + req.SystemPrompt
		}
	}

	// 2) 技能选择：Profile.Skills 非空时限制本次调用可见的技能集合。
	if len(profile.Skills) > 0 {
		req.Skills = profile.Skills
	}

	// 3) 背景注入：按 Profile 声明的开关执行。
	if profile.Context.InjectMemory {
		req = s.injectMemory(ctx, req)
	}
	if profile.Context.InjectProject {
		req = s.injectProjectContext(ctx, req)
	}
	return req
}

// injectMemory 在调用前检索相关记忆并注入 SystemPrompt。
//
// 未配置记忆、请求未携带 UserID、或检索不到相关记忆时，原样返回请求。
func (s *AgentService) injectMemory(ctx context.Context, req Request) Request {
	if s.memory == nil || strings.TrimSpace(req.UserID) == "" {
		return req
	}
	query := lastUserMessage(req.Messages)
	mems, err := s.memory.Retrieve(ctx, req.UserID, query, defaultMemoryRetrieveLimit)
	if err != nil || len(mems) == 0 {
		return req
	}
	block := BuildMemoryContext(mems)
	if block == "" {
		return req
	}
	if req.SystemPrompt == "" {
		req.SystemPrompt = block
	} else {
		req.SystemPrompt = req.SystemPrompt + "\n\n" + block
	}
	return req
}

// injectProjectContext 在调用前把当前项目已完成的分析节点等背景注入 SystemPrompt。
//
// 未配置项目上下文提供者、请求未携带 UserID、或查询不到上下文时，原样返回请求。
// 与 injectMemory 不同，项目上下文是实时查询的领域状态（而非持久化记忆），因此不落库。
func (s *AgentService) injectProjectContext(ctx context.Context, req Request) Request {
	if s.project == nil || strings.TrimSpace(req.UserID) == "" {
		return req
	}
	block, err := s.project.ProjectContext(ctx, req.UserID)
	if err != nil || block == "" {
		return req
	}
	if req.SystemPrompt == "" {
		req.SystemPrompt = block
	} else {
		req.SystemPrompt = req.SystemPrompt + "\n\n" + block
	}
	return req
}

// extractMemory 在任务成功完成后调用记忆提取器，把提取出的记忆写入 Repository 并广播。
//
// 提取器未配置或提取失败时静默跳过（记忆提取是尽力而为的旁路，不应影响任务结果）。
func (s *AgentService) extractMemory(ctx context.Context, task *Task, req Request, result *Result) {
	if s.memory == nil || strings.TrimSpace(req.UserID) == "" {
		return
	}
	mems, err := s.memory.Extract(ctx, MemoryTurn{
		UserID:    req.UserID,
		SessionID: req.SessionID,
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
func (s *AgentService) CancelTask(ctx context.Context, taskID string) error {
	s.mu.Lock()
	cancel, ok := s.active[taskID]
	s.mu.Unlock()
	if ok {
		cancel()
	}
	return s.transitionTask(ctx, taskID, TaskCanceled, "canceled by user")
}

// Recover 在后端重启后重建运行态（见 design.md 第 6 / 19 节）。
//
// 当前内存实现下：
//   - 没有活跃 goroutine 的 running 任务：无法恢复执行，标记为 failed（interrupted）。
//   - 没有活跃 goroutine 的 waiting_permission 任务：pending 权限已持久化，
//     UI 仍可通过 GetPendingPermissions 拉取并批准 / 拒绝（更新 DB）；
//     但 Agent 进程已丢失，真正“恢复执行”依赖各 Provider 的 checkpoint / resume 能力。
func (s *AgentService) Recover(ctx context.Context) error {
	running, err := s.tasks.ListByStatus(ctx, TaskRunning)
	if err != nil {
		return err
	}
	for _, t := range running {
		if !s.isActive(t.ID) {
			_ = s.transitionTask(ctx, t.ID, TaskFailed, "backend restarted: task interrupted")
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

func (s *AgentService) isActive(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.active[taskID]
	return ok
}

// taskRuntime 是绑定到具体任务的 Runtime：事件持久化 + 广播，权限持久化 + 阻塞等待。
type taskRuntime struct {
	svc     *AgentService
	taskID  string
	handler StreamHandler
}

func (s *AgentService) newTaskRuntime(taskID string, handler StreamHandler) Runtime {
	return &taskRuntime{svc: s, taskID: taskID, handler: handler}
}

func (r *taskRuntime) Emit(ctx context.Context, event StreamEvent) error {
	// delta / block 分流：
	//   - 完整块与生命周期事件：持久化 + 广播，作为时间线数据源；
	//   - 纯增量（text / reasoning_delta）：仅广播，用于前端实时渲染，不落库。
	//
	// 判断留在 Emit（只有这里能拿到 StreamEvent.Type），但两条分支都走同一个 publish 出口。
	if isBlockEvent(event.Type) {
		r.svc.emitEvent(ctx, r.taskID, EventStream, event)
	} else {
		r.svc.broadcastEvent(ctx, r.taskID, EventStream, event)
	}
	if r.handler != nil {
		return r.handler(ctx, event)
	}
	return nil
}

func (r *taskRuntime) RequestPermission(ctx context.Context, operation Operation) (PermissionDecision, error) {
	// 1) 通知 UI：Agent 请求了对某个操作的权限确认。
	// _ = r.Emit(ctx, StreamEvent{Type: StreamEventPermission, Content: marshalOperation(operation)})

	// 2) 策略先行：直接放行 / 直接拒绝。
	if decision := r.svc.policy.Check(ctx, operation); decision != DecisionAsk {
		// r.emitPermissionResult(ctx, string(decision))
		if decision == DecisionDeny {
			return DecisionDeny, ErrPermissionDenied
		}
		return DecisionAllow, nil
	}

	// 3) 持久化 pending 权限请求。
	task, err := r.svc.tasks.Get(ctx, r.taskID)
	if err != nil {
		return "", err
	}
	perm, err := r.svc.perms.Create(ctx, r.taskID, task.SessionID, operation)
	if err != nil {
		return "", err
	}

	// 4) 任务进入等待权限状态，并广播事件。
	if err := r.svc.transitionTask(ctx, r.taskID, TaskWaitingPermission, ""); err != nil {
		return "", err
	}
	r.svc.emitEvent(ctx, r.taskID, EventPermissionCreated, perm)

	// 5) 阻塞等待 UI 决策。
	decision, err := r.svc.perms.Wait(ctx, perm.ID)
	if err != nil {
		r.emitPermissionResult(ctx, fmt.Sprintf("error: %v", err))
		return "", err
	}

	// 6) 恢复任务为 running（deny 时 Agent 会返回错误，由 execute 置为 failed）。
	_ = r.svc.transitionTask(ctx, r.taskID, TaskRunning, "")
	// r.emitPermissionResult(ctx, string(decision))
	return decision, nil
}

// emitPermissionResult 把权限决策结果透传给 UI（StreamEventPermissionResult）。
func (r *taskRuntime) emitPermissionResult(ctx context.Context, content string) {
	_ = r.Emit(ctx, StreamEvent{Type: StreamEventPermissionResult, Content: content})
}

// marshalOperation 把 Operation 序列化为 JSON 字符串，作为权限通知事件的 content。
func marshalOperation(op Operation) string {
	data, err := json.Marshal(op)
	if err != nil {
		return fmt.Sprintf("type=%s", op.Type)
	}
	return string(data)
}

func (r *taskRuntime) WaitPermission(ctx context.Context, permissionID string) (PermissionDecision, error) {
	return r.svc.perms.Wait(ctx, permissionID)
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
