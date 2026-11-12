package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/realtime"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

var (
	errWorkerConcurrencyLimit = errors.New("worker concurrency limit reached")
)

// defaultSyncCreateWait 是「同步创建容器」等待 running 的默认上限。
const defaultSyncCreateWait = 2 * time.Minute

// defaultSyncCreateCleanupTimeout 是「同步创建失败后回收运行时资源」的超时上限。
const defaultSyncCreateCleanupTimeout = 60 * time.Second

type ContainerManager struct {
	containerService interfaces.ContainerService
	containerRepo    interfaces.ContainerRepository
	projectRepo      interfaces.ProjectRepository
	analysisRepo     interfaces.AnalysisRepository
	workflowService  interfaces.WorkflowService
	reg              *containerruntime.Registry
	bus              event.Bus
	res              ContainerRuntimeResolver
	cfg              *config.Config
	monitorOnce      sync.Once

	// hub 是实时推送出口：容器创建 / 删除这类「与用户无关」的全局变化
	// 需要推给所有在线客户端（见 broadcastContainerChanged）。为空时静默跳过。
	//
	// 用 SetHub 在装配阶段回填，而不是放进构造函数，因为 *realtime.Hub 可能在
	// ContainerManager 之前还没被 Provide：dig 的 Invoke 是**立即**解析依赖的，
	// 而 Provide 只是注册。真实失败就是 container.go 里回填 Invoke 写在了
	// Provide(realtime.NewHub) 之前 → “missing type: *realtime.Hub”。
	// 回填写法让「创建 Manager」与「注册 Hub」的先后顺序不再互相约束。
	//
	// 注意：这里**不是**包级循环依赖 —— internal/realtime 不 import manager/handler，
	// NewHub 只依赖 *config.Config。真正需要避免的是装配顺序耦合。
	hub *realtime.Hub

	// lifecycle 收敛状态迁移 / 运行时解析 / spec 组装等与 ContainerCreateWorker
	// 共享的地基层能力，避免 Manager 与 Worker 相互持有指针（见 container_lifecycle.go）。
	lifecycle *ContainerLifecycle

	// createWorker 是同步创建容器的执行者：CreateByTemplateWithOptions(Sync=true) 时
	// 直接调用它的 create/start 逻辑，跳过 outbox（终端会话必须「返回即 running」）。
	// 由容器装配阶段回填（见 internal/container），避免 ContainerManager 与
	// ContainerCreateWorker 构造顺序互相依赖。
	createWorker *ContainerCreateWorker

	// statusMu/statusWaiters 保存「等待某个 ContainerInstance 进入指定状态」的订阅者。
	// OnEvent 应用完运行时事件后向订阅者广播最新状态，供创建方等待
	// ContainerStarted（running）而没有必要在 runtime 层阻塞等待。
	statusMu      sync.Mutex
	statusWaiters map[int64][]chan types.ContainerStatus
}

func NewContainerManager(
	containerRepo interfaces.ContainerRepository,
	analysisRepo interfaces.AnalysisRepository,
	projectRepo interfaces.ProjectRepository,
	workflowService interfaces.WorkflowService,
	reg *containerruntime.Registry,
	bus event.Bus,
	res ContainerRuntimeResolver,
	lifecycle *ContainerLifecycle,
	cfg *config.Config,
) *ContainerManager {
	return &ContainerManager{
		containerRepo: containerRepo, projectRepo: projectRepo, analysisRepo: analysisRepo, workflowService: workflowService, reg: reg, bus: bus, res: res, cfg: cfg,
		// lifecycle 由 DI 单例注入，与 ContainerCreateWorker 共享同一实例。
		lifecycle: lifecycle,
	}
}

// SetHub 回填实时推送出口（由容器装配阶段调用一次）。
//
// 不走构造函数的原因见 hub 字段的注释：这是装配顺序问题，不是包级循环依赖。
func (s *ContainerManager) SetHub(hub *realtime.Hub) {
	if s == nil {
		return
	}
	s.hub = hub
}

// Lifecycle 返回 Manager 持有的共享生命周期基础设施，供 ContainerCreateWorker 注入使用。
func (s *ContainerManager) Lifecycle() *ContainerLifecycle {
	if s == nil {
		return nil
	}
	return s.lifecycle
}

// SetCreateWorker 注入同步创建容器所需的 worker（容器装配阶段调用一次）。
//
// 之所以不在构造函数里注入：ContainerCreateWorker 自身依赖 ContainerManager，
// 直接双向构造会形成循环依赖；装配阶段回填一个指针即可。
//
// 同时把 Manager 自己的 lifecycle 共享给 worker（worker 构造期可能未拿到），
// 并回填 worker.owner，供 StartTerminalRuntime 复用 CreateByTemplateWithOptions。
func (s *ContainerManager) SetCreateWorker(worker *ContainerCreateWorker) {
	if s == nil || worker == nil {
		return
	}
	s.createWorker = worker
	if worker.lifecycle == nil {
		worker.lifecycle = s.lifecycle
	}
	worker.owner = s
}

func (s *ContainerManager) TransitionContainerAndEnqueueOutbox(ctx context.Context, inst *types.ContainerInstance, to types.ContainerStatus, eventType string) error {
	return s.lifecycle.TransitionAndEnqueue(ctx, inst, to, eventType)
}

// transitionContainer 是容器实例状态迁移的唯一入口（薄委托到 ContainerLifecycle）。
//
// enforceConcurrency 控制是否把本次迁移计入创建并发额度；终端容器这类长期占用
// running 的实例不参与该额度（false）。enqueueOutbox 控制是否写 outbox 事件；
// 由调用方同步驱动的内部迁移不需要 outbox（false）。
func (s *ContainerManager) transitionContainer(ctx context.Context, inst *types.ContainerInstance, to types.ContainerStatus, eventType string, enforceConcurrency bool, enqueueOutbox bool) error {
	return s.lifecycle.Transition(ctx, inst, to, eventType, enforceConcurrency, enqueueOutbox)
}

// WaitForInstanceStatus 阻塞等待实例进入 wanted 中的任一状态。
//
// 状态推进完全由运行时事件驱动（ContainerManager.OnEvent），因此等待方只需要
// 订阅状态广播：先注册订阅者，再读一次数据库，避免“注册前事件已到”的竞态。
// 实例进入 failed / stopped / exited 且不在 wanted 中时返回错误。
func (s *ContainerManager) WaitForInstanceStatus(ctx context.Context, instanceID int64, wanted ...types.ContainerStatus) (types.ContainerStatus, error) {
	if instanceID <= 0 {
		return "", errors.New("container instance id is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	ch := make(chan types.ContainerStatus, 8)
	s.statusMu.Lock()
	if s.statusWaiters == nil {
		s.statusWaiters = make(map[int64][]chan types.ContainerStatus)
	}
	s.statusWaiters[instanceID] = append(s.statusWaiters[instanceID], ch)
	s.statusMu.Unlock()
	defer s.removeStatusWaiter(instanceID, ch)

	// 先按数据库当前状态判断一次：事件可能在注册订阅者之前就已经应用。
	// 只有命中 wanted 或已经进入终态才立即返回，其它中间状态（creating 等）继续等
	// OnEvent 的后续广播。
	current := s.currentInstanceStatus(ctx, instanceID)
	if status, ok := matchInstanceStatus(current, wanted); ok {
		return status, nil
	}
	if isTerminalContainerStatus(current) {
		return current, fmt.Errorf("container instance %d reached %s before %v", instanceID, current, wanted)
	}

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case status := <-ch:
			if matched, ok := matchInstanceStatus(status, wanted); ok {
				return matched, nil
			}
			if isTerminalContainerStatus(status) {
				return status, fmt.Errorf("container instance %d reached %s before %v", instanceID, status, wanted)
			}
		}
	}
}

func (s *ContainerManager) currentInstanceStatus(ctx context.Context, instanceID int64) types.ContainerStatus {
	inst, err := s.containerRepo.GetContainerInstanceByID(ctx, instanceID)
	if err != nil || inst == nil {
		return ""
	}
	return inst.Status
}

func matchInstanceStatus(status types.ContainerStatus, wanted []types.ContainerStatus) (types.ContainerStatus, bool) {
	if status == "" {
		return "", false
	}
	for _, item := range wanted {
		if status == item {
			return status, true
		}
	}
	return status, false
}

func isTerminalContainerStatus(status types.ContainerStatus) bool {
	switch status {
	case types.ContainerFailed, types.ContainerStopped, types.ContainerExited, types.ContainerDeleted:
		return true
	default:
		return false
	}
}

func (s *ContainerManager) removeStatusWaiter(instanceID int64, ch chan types.ContainerStatus) {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	waiters := s.statusWaiters[instanceID]
	for i, item := range waiters {
		if item == ch {
			waiters = append(waiters[:i], waiters[i+1:]...)
			break
		}
	}
	if len(waiters) == 0 {
		delete(s.statusWaiters, instanceID)
		return
	}
	s.statusWaiters[instanceID] = waiters
}

// notifyInstanceStatus 把实例最新状态广播给等待方（非阻塞：等待方只关心最后一次命中）。
func (s *ContainerManager) notifyInstanceStatus(instanceID int64, status types.ContainerStatus) {
	if instanceID <= 0 || status == "" {
		return
	}
	s.statusMu.Lock()
	waiters := append([]chan types.ContainerStatus(nil), s.statusWaiters[instanceID]...)
	s.statusMu.Unlock()

	for _, ch := range waiters {
		select {
		case ch <- status:
		default:
		}
	}
}

// concurrencyOccupiedStatuses 返回占用创建并发额度的状态集合。
func concurrencyOccupiedStatuses() []types.ContainerStatus {
	return []types.ContainerStatus{
		types.ContainerCreating,
		types.ContainerRunning,
		types.ContainerStarting,
		types.ContainerStopping,
		types.ContainerStopPending,
	}
}

func (s *ContainerManager) GetMaxConcurrency() int {
	return s.lifecycle.MaxConcurrency()
}

// ContainerQueueRealtimeCategory 是「容器队列」实时通知在前端的组件类目。
//
// 前端 ContainerQueueMonitor 用 useComponentStore 注册 category=container-queue 的
// handler（method=containerChanged），后端每次推送都会触发它重新拉取
// /container/queue/status。
const ContainerQueueRealtimeCategory = "container-queue"

// broadcastContainerChanged 通知所有在线客户端「容器集合发生了变化」。
//
// 容器创建 / 删除会改变 /container/queue/status 的 active_count / pending_count，
// 而这份状态与具体用户无关，因此走 hub.Broadcast（而不是按 userID 推送）。
// 前端只需要收到一条 component.invoke 就会重新拉一次队列状态，不依赖具体参数。
func (m *ContainerManager) broadcastContainerChanged(ctx context.Context, evt string, inst *types.ContainerInstance) {
	if m.hub == nil || inst == nil {
		return
	}

	args := map[string]any{
		"event": evt,
		"id":    strconv.FormatInt(inst.ID, 10),
	}
	if status := strings.ToLower(strings.TrimSpace(string(inst.Status))); status != "" {
		args["status"] = status
	}

	m.hub.Broadcast(map[string]any{
		"action": "component.invoke",
		"payload": map[string]any{
			"category": ContainerQueueRealtimeCategory,
			"id":       "*",
			"method":   "containerChanged",
			"args":     args,
		},
	})
}

// func (m *ContainerManager) Create(ctx context.Context, spec Spec) error {

// 	// 1. FSM: pending -> creating
// 	inst := m.createInstance(spec)

// 	_ = m.transition(ctx, inst, Creating, "ContainerCreating")

// 	// 2. Runtime
// 	runtimeID, err := m.runtime.Create(ctx, spec)
// 	if err != nil {
// 		_ = m.transition(ctx, inst, Failed, "ContainerFailed")
// 		return err
// 	}

// 	inst.RuntimeID = runtimeID

// 	// 3. FSM: creating -> running
// 	_ = m.transition(ctx, inst, Running, "ContainerStarted")

//		return nil
//	}
//
// ContainerCreateOptions 是 CreateByTemplateWithOptions 的参数。
//
// 与仅按模板 + owner 创建相比，它额外支持：
//   - 显式指定容器运行形态（Kind）：决定 executeCreateInstance 如何组装运行时 spec；
//     未指定时按 OwnerType 推断（见 types.ContainerInstance.NormalizedKind），
//     并随实例一起落库；
//   - 同步创建：不写 outbox、由 ContainerCreateWorker 直接调用 runtime.Create +
//     runtime.Start，并在返回前等待容器进入 running（终端会话需要立刻 exec shell）。
//
// 启动命令、workload 类型、运行用户等只由 Kind 决定（见 executeCreateInstance），
// 这里不再接受 Command / Entrypoint 覆盖。
type ContainerCreateOptions struct {
	TemplateID int64
	OwnerType  types.ContainerOwnerType
	OwnerID    int64
	Name       string

	// Kind 是容器运行形态（terminal / web / dag_node）。
	// 为空时按 OwnerType 推断（见 types.ContainerInstance.NormalizedKind）。
	Kind types.ContainerInstanceKind

	// Sync 为 true 时同步创建并等待容器 running：
	//   - 不写 outbox，直接由 worker 调用 runtime.Create + runtime.Start；
	//   - 创建过程中的状态迁移不计入创建并发额度（长期占用 running 的实例）；
	//   - 返回前等待 ContainerManager.OnEvent 广播的 ContainerStarted（running）。
	//
	// Sync 为 false（默认）时保持旧行为：写 outbox，由 OutboxDispatcher 异步创建。
	Sync bool
	// WaitTimeout 是 Sync=true 时等待容器 running 的上限；<=0 使用默认 2 分钟。
	WaitTimeout time.Duration
}

// CreateByTemplateWithOptions 按模板创建容器实例（创建容器的唯一入口）。
//
// 默认走 outbox 异步链路，Sync=true 时改为同步创建并等待 running
// （见 ContainerCreateOptions）。
func (m *ContainerManager) CreateByTemplateWithOptions(ctx context.Context, opts ContainerCreateOptions) (*types.ContainerInstance, error) {
	templateID := opts.TemplateID
	if templateID <= 0 {
		return nil, fmt.Errorf("container template id is required")
	}

	tpl, err := m.containerRepo.GetContainerTemplateByID(ctx, templateID)
	if err != nil {
		return nil, err
	}

	// 镜像名与拉取策略直接存在容器模板绑定行上，这里只需确保已配置镜像名。
	if strings.TrimSpace(tpl.ImageName) == "" {
		return nil, fmt.Errorf("container template %d does not bind any image", templateID)
	}

	rt, err := m.getRuntime()
	if err != nil {
		return nil, err
	}

	inst := &types.ContainerInstance{
		RuntimeName: rt.Name(),
		TemplateID:  templateID,
		OwnerType:   opts.OwnerType,
		OwnerID:     opts.OwnerID,
		Name:        opts.Name,
		Status:      types.ContainerCreatePending,
	}
	// Kind 只在实例上落一次：异步链路的 worker 从实例读它来决定怎么组装运行时 spec，
	// 因此这里必须与 Sync 与否无关地写进数据库。
	inst.Kind = opts.Kind
	if inst.Kind == "" {
		inst.Kind = inst.NormalizedKind()
	}

	if opts.Sync {
		return m.createByTemplateSync(ctx, inst, opts)
	}

	// Always enqueue the create request through the worker.
	// userID := ""
	// if ctx != nil {
	// 	if uid, ok := ctx.Value(types.UserIDContextKey).(string); ok {
	// 		userID = uid
	// 	} else if uid, ok := ctx.Value(types.UserIDContextKey.String()).(string); ok {
	// 		userID = uid
	// 	}
	// }

	// req := containerCreatePayload{
	// 	// RuntimeName: runtimeName,
	// 	TemplateID: templateID,
	// 	OwnerType:  string(ownerType),
	// 	OwnerID:    ownerID,
	// 	Name:       name,
	// 	UserID:     userID,
	// }
	req := types.ContainerEvent{
		ContainerInstanceID: inst.ID,
		Event:               string(types.ContainerCreatePending),
		Message:             string(types.ContainerCreatePending),
	}
	maxPending := m.getCreateQueueMaxPending()

	err = m.containerRepo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
		if maxPending > 0 {
			count, err := m.countPendingCreateQueueRequests(ctx, tx)
			if err != nil {
				return fmt.Errorf("count pending create/start requests: %w", err)
			}
			if count >= int64(maxPending) {
				return fmt.Errorf("container create queue is full (%d/%d pending), please try again later",
					count, maxPending)
			}
		}

		if err := tx.CreateContainerInstance(ctx, inst); err != nil {
			return err
		}
		// if err := tx.CreateContainerEvent(ctx, &types.ContainerEvent{
		// 	ContainerInstanceID: inst.ID,
		// 	Event:               "ContainerPending",
		// 	Message:             "container instance created",
		// }); err != nil {
		// 	return err
		// }

		req.ContainerInstanceID = inst.ID
		payload, err := json.Marshal(req)
		if err != nil {
			return fmt.Errorf("marshal create payload: %w", err)
		}

		return tx.CreateOutboxEvent(ctx, &types.OutboxEvent{
			Type:    OutboxEventTypeCreateRequest,
			Payload: payload,
			Status:  "pending",
		})
	})
	if err != nil {
		return nil, err
	}

	logger.Infof(ctx, "[ContainerManager] enqueued create request, instance_id=%d name=%s", inst.ID, opts.Name)
	// 实例已落库：容器集合发生变化（active / pending 计数都会变），
	// 立即广播给所有在线客户端刷新队列状态。
	// m.broadcastContainerChanged(ctx, "ContainerCreated", inst)
	return inst, nil
}

// createByTemplateSync 同步创建容器：落实例 → worker 直接 create/start → 等待 running。
//
// 终端会话需要「返回时容器已经能 exec」的语义，因此不能走 outbox 异步链路。
func (m *ContainerManager) createByTemplateSync(ctx context.Context, inst *types.ContainerInstance, opts ContainerCreateOptions) (*types.ContainerInstance, error) {
	if m.createWorker == nil {
		return nil, errors.New("container create worker is not configured")
	}
	if err := m.containerRepo.CreateContainerInstance(ctx, inst); err != nil {
		return nil, fmt.Errorf("create container instance: %w", err)
	}

	if err := m.createWorker.executeCreateInstance(ctx, inst); err != nil {
		_ = m.containerRepo.DeleteContainerInstance(ctx, inst.ID)
		return nil, err
	}

	waitTimeout := opts.WaitTimeout
	if waitTimeout <= 0 {
		waitTimeout = defaultSyncCreateWait
	}
	waitCtx, cancel := context.WithTimeout(ctx, waitTimeout)
	defer cancel()
	if _, err := m.WaitForInstanceStatus(waitCtx, inst.ID, types.ContainerRunning); err != nil {
		// 启动失败：先尽力删除运行时资源，再删实例记录。
		//
		// 只删数据库记录会留下一个不停重启的 Deployment / Job（典型场景是启动命令
		// 拼错导致 CrashLoopBackOff）；资源名里带实例 ID，下次创建不会复用，
		// 只会越堆越多，而且永远在耗节点资源。
		m.deleteFailedSyncRuntime(context.Background(), inst)
		_ = m.containerRepo.DeleteContainerInstance(context.Background(), inst.ID)
		return nil, fmt.Errorf("wait container %d running: %w", inst.ID, err)
	}

	// 等待期间 OnEvent 可能已经更新过实例（status / runtime id / started_at）。
	if latest, err := m.containerRepo.GetContainerInstanceByID(ctx, inst.ID); err == nil && latest != nil {
		inst = latest
	}
	// 同步创建已完成：实例已经是 running，广播一次让队列状态 / 列表刷新。
	// m.broadcastContainerChanged(ctx, "ContainerCreated", inst)
	return inst, nil
}

// deleteFailedSyncRuntime 在同步创建失败（容器始终没进入 running）时尽力删除运行时资源。
//
// 删除数据库记录不会影响运行时：Deployment / Job 仍然存在并持续重启（CrashLoopBackOff），
// 资源名里带实例 ID，下次创建不会复用，失败重试只会不断堆积。
func (m *ContainerManager) deleteFailedSyncRuntime(ctx context.Context, inst *types.ContainerInstance) {
	if inst == nil || strings.TrimSpace(inst.RuntimeID) == "" {
		return
	}
	rt, err := m.getRuntimeByInstance(inst)
	if err != nil || rt == nil {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, defaultSyncCreateCleanupTimeout)
	defer cancel()
	if err := rt.Delete(cleanupCtx, inst.RuntimeID); err != nil {
		logger.Warnf(ctx, "[ContainerManager] delete failed sync container %s failed: %v", inst.RuntimeID, err)
	}
}

func (m *ContainerManager) getCreateQueueMaxPending() int {
	if m.cfg != nil && m.cfg.Container != nil && m.cfg.Container.CreateQueueMaxPending > 0 {
		return m.cfg.Container.CreateQueueMaxPending
	}
	return 50
}

// func (m *ContainerManager) getCreateQueueMaxConcurrency() int {
// 	if m.cfg != nil && m.cfg.Container != nil && m.cfg.Container.CreateQueueMaxConcurrency > 0 {
// 		return m.cfg.Container.CreateQueueMaxConcurrency
// 	}
// 	return 3
// }

// func (m *ContainerManager) CreateQueueMaxConcurrency() int {
// 	return m.getCreateQueueMaxConcurrency()
// }

func (m *ContainerManager) CreateQueueMaxPending() int {
	return m.getCreateQueueMaxPending()
}

func (m *ContainerManager) QueueStatus(ctx context.Context) (*CreateQueueStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	status := &CreateQueueStatus{
		MaxConcurrency: m.GetMaxConcurrency(),
		MaxPending:     m.getCreateQueueMaxPending(),
	}

	err := m.containerRepo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
		active, err := tx.CountContainerInstanceByStatuses(ctx, concurrencyOccupiedStatuses())
		if err != nil {
			return err
		}
		pending, err := m.countPendingCreateQueueRequests(ctx, tx)
		if err != nil {
			return err
		}
		status.ActiveCount = active
		status.PendingCount = pending
		return nil
	})
	if err != nil {
		return nil, err
	}

	return status, nil
}

func (m *ContainerManager) Start(ctx context.Context, id int64) error {
	maxPending := m.getCreateQueueMaxPending()
	if maxPending > 0 {
		count, err := m.countPendingCreateQueueRequests(ctx, m.containerRepo)
		if err != nil {
			return fmt.Errorf("count pending create/start requests: %w", err)
		}
		if count >= int64(maxPending) {
			return fmt.Errorf("container start queue is full (%d/%d pending), please try again later",
				count, maxPending)
		}
	}

	// userID := ""
	// if ctx != nil {
	// 	if uid, ok := ctx.Value(types.UserIDContextKey).(string); ok {
	// 		userID = uid
	// 	} else if uid, ok := ctx.Value(types.UserIDContextKey.String()).(string); ok {
	// 		userID = uid
	// 	}
	// }

	// req := containerStartPayload{
	// 	ContainerInstanceID: id,
	// 	UserID:              userID,
	// }
	instance, err := m.containerRepo.GetContainerInstanceByID(ctx, id)
	if err != nil {
		return err
	}
	if err := m.TransitionContainerAndEnqueueOutbox(ctx, instance, types.ContainerStartPending, OutboxEventTypeStartRequest); err != nil {
		logger.Errorf(ctx, "[ContainerManager] enqueue start failed, instance_id=%d err=%v", id, err)

		return err
	}

	// if err := m.enqueueLifecycleRequest(ctx, id, fsm.StartPending, OutboxEventTypeStartRequest, req); err != nil {
	// 	logger.Errorf(ctx, "[ContainerManager] enqueue start failed, instance_id=%d err=%v", id, err)
	// 	return err
	// }

	logger.Infof(ctx, "[ContainerManager] enqueued start request, instance_id=%d", id)
	return nil
}

func (m *ContainerManager) countPendingCreateQueueRequests(ctx context.Context, repo interfaces.ContainerRepository) (int64, error) {
	return repo.CountPendingOutboxEvents(ctx, OutboxEventTypeCreateRequest, OutboxEventTypeStartRequest)
}

func (m *ContainerManager) Stop(ctx context.Context, id int64) error {
	inst, err := m.containerRepo.GetContainerInstanceByID(ctx, id)
	if err != nil {
		return err
	}

	// If already in a terminal state, nothing to do.
	switch strings.TrimSpace(strings.ToLower(string(inst.Status))) {
	case string(types.ContainerStopped), string(types.ContainerFailed), string(types.ContainerExited):
		return types.ErrContainerAlreadyStopped
	}
	instaince, err := m.containerRepo.GetContainerInstanceByID(ctx, id)
	if err != nil {
		return err
	}
	if err := m.TransitionContainerAndEnqueueOutbox(ctx, instaince, types.ContainerStopPending, OutboxEventTypeStopRequest); err != nil {
		logger.Errorf(ctx, "[ContainerManager] enqueue stop failed, instance_id=%d err=%v", id, err)
		return err
	}

	// userID := ""
	// if ctx != nil {
	// 	if uid, ok := ctx.Value(types.UserIDContextKey).(string); ok {
	// 		userID = uid
	// 	} else if uid, ok := ctx.Value(types.UserIDContextKey.String()).(string); ok {
	// 		userID = uid
	// 	}
	// }

	// req := containerStopPayload{
	// 	ContainerInstanceID: inst.ID,
	// 	UserID:              userID,
	// }

	// if err := m.enqueueLifecycleRequest(ctx, inst.ID, fsm.StopPending, OutboxEventTypeStopRequest, req); err != nil {
	// 	logger.Errorf(ctx, "[ContainerManager] enqueue stop failed, instance_id=%d err=%v", inst.ID, err)
	// 	return err
	// }

	logger.Infof(ctx, "[ContainerManager] enqueued stop request, instance_id=%d", inst.ID)
	return nil
}

func (m *ContainerManager) StopByOwner(ctx context.Context, ownerType types.ContainerOwnerType, ownerID int64) error {
	inst, err := m.containerRepo.GetContainerInstanceByOwner(ctx, ownerType, ownerID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if inst == nil || inst.ID == 0 {
		return nil
	}
	switch strings.TrimSpace(strings.ToLower(string(inst.Status))) {
	case string(types.ContainerStopped), string(types.ContainerFailed), string(types.ContainerExited),
		string(types.ContainerStopPending), string(types.ContainerStopping),
		string(types.ContainerStartPending), string(types.ContainerStarting):
		return nil
	}
	return m.Stop(ctx, inst.ID)
}

func (m *ContainerManager) Delete(ctx context.Context, id int64) error {
	inst, err := m.containerRepo.GetContainerInstanceByID(ctx, id)
	if err != nil {
		return err
	}

	// If already deleted, nothing to do.
	if inst == nil || inst.ID == 0 {
		return nil
	}
	instance, err := m.containerRepo.GetContainerInstanceByID(ctx, id)
	if err != nil {
		return err
	}
	if err := m.TransitionContainerAndEnqueueOutbox(ctx, instance, types.ContainerDeletePending, OutboxEventTypeDeleteRequest); err != nil {
		logger.Errorf(ctx, "[ContainerManager] enqueue delete failed, instance_id=%d err=%v", id, err)
		return err
	}

	// userID := ""
	// if ctx != nil {
	// 	if uid, ok := ctx.Value(types.UserIDContextKey).(string); ok {
	// 		userID = uid
	// 	} else if uid, ok := ctx.Value(types.UserIDContextKey.String()).(string); ok {
	// 		userID = uid
	// 	}
	// }

	// req := containerDeletePayload{
	// 	ContainerInstanceID: inst.ID,
	// 	UserID:              userID,
	// }

	// if err := m.enqueueLifecycleRequest(ctx, inst.ID, fsm.DeletePending, OutboxEventTypeDeleteRequest, req); err != nil {
	// 	logger.Errorf(ctx, "[ContainerManager] enqueue delete failed, instance_id=%d err=%v", inst.ID, err)
	// 	return err
	// }

	logger.Infof(ctx, "[ContainerManager] enqueued delete request, instance_id=%d", inst.ID)
	return nil
}

// logFailureEvidence 在容器失败时把运行时日志一并写入服务端日志（Warn 级别）。
//
// 与 docker/kubernetes 运行时内部打印的证据行不同，这里以 ContainerInstance
// 为中心，带上 analysis / node 维度，便于把日志行直接对应到某个节点的失败。
func (m *ContainerManager) logFailureEvidence(ctx context.Context, inst *types.ContainerInstance, message string) {
	rt, err := m.getRuntimeByInstance(inst)
	if err != nil {
		logger.Warnf(ctx, "[ContainerManager] container failed, runtime_id=%s instance_id=%d message=%q, resolve runtime failed: %v", inst.RuntimeID, inst.ID, message, err)
		return
	}

	logs, logErr := rt.Logs(ctx, inst.RuntimeID, 200)
	if logErr != nil {
		logs = fmt.Sprintf("<failed to read runtime logs: %v>", logErr)
	}

	logger.Warnf(ctx, "[ContainerManager] container failed (runtime evidence), owner_type=%s owner_id=%d instance_id=%d runtime_id=%s runtime=%s status=%s message=%q started_at=%v finished_at=%v\n--- runtime logs (tail 200) ---\n%s\n--- end runtime logs ---",
		inst.OwnerType, inst.OwnerID, inst.ID, inst.RuntimeID, inst.RuntimeName, inst.Status, message, inst.StartedAt, inst.FinishedAt, strings.TrimRight(logs, "\n"))
}

func (m *ContainerManager) GetLogs(ctx context.Context, id int64, tail int) (string, error) {
	if tail <= 0 {
		tail = 200
	}
	inst, err := m.containerRepo.GetContainerInstanceByID(ctx, id)
	if err != nil {
		return "", err
	}
	rt, err := m.getRuntimeByInstance(inst)
	if err != nil {
		return "", err
	}

	return rt.Logs(ctx, inst.RuntimeID, tail)
}

// Describe 按 ContainerInstance ID 从运行时获取详情（docker inspect / kubectl describe）。
func (m *ContainerManager) Describe(ctx context.Context, id int64) (*containerruntime.RuntimeDescription, error) {
	inst, err := m.containerRepo.GetContainerInstanceByID(ctx, id)
	if err != nil {
		return nil, err
	}

	rt, err := m.getRuntimeByInstance(inst)
	if err != nil {
		return nil, err
	}

	describer, ok := rt.(containerruntime.RuntimeDescriber)
	if !ok {
		return nil, fmt.Errorf("runtime %s does not support describe", inst.RuntimeName)
	}

	return describer.Describe(ctx, inst.RuntimeID)
}

// OpenTerminal 为运行中的容器实例打开一条交互式终端（kubectl exec -it / docker exec -it）。
//
// 只有容器实例处于 running 状态时才允许连接：命令结束、容器退出后终端也随之不可用。
func (m *ContainerManager) OpenTerminal(ctx context.Context, id int64, opts containerruntime.TerminalOptions) (containerruntime.TerminalSession, error) {
	inst, err := m.containerRepo.GetContainerInstanceByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if inst.Status != types.ContainerRunning {
		return nil, fmt.Errorf("container instance %d is not running (status=%s)", id, inst.Status)
	}

	rt, err := m.getRuntimeByInstance(inst)
	if err != nil {
		return nil, err
	}

	terminal, ok := rt.(containerruntime.RuntimeTerminal)
	if !ok {
		return nil, fmt.Errorf("runtime %s does not support interactive terminals", inst.RuntimeName)
	}
	if len(opts.Command) == 0 {
		opts.Command = []string{"/bin/bash", "-l"}
	}
	opts.TTY = true

	return terminal.OpenTerminal(ctx, inst.RuntimeID, opts)
}

// func (m *ContainerManager) OnRuntimeEvent(e RuntimeEvent) {

// 	inst := m.repo.FindByRuntimeID(e.RuntimeID)

// 	switch e.Type {

// 	case "OOMKilled":

// 		_ = m.transition(
// 			context.Background(),
// 			inst,
// 			Failed,
// 			"ContainerOOM",
// 		)

// 	case "Exited":

//		_ = m.transition(
//			context.Background(),
//			inst,
//			Stopped,
//			"ContainerStopped",
//		)
//	}

// 如果容器运行太快，ContainerStarted与 ContainerExited可能会竞争
func (m *ContainerManager) OnEvent(e containerruntime.RuntimeEvent) {
	ctx := context.Background()
	inst, err := m.containerRepo.GetContainerInstanceByRuntimeID(ctx, e.RuntimeID)
	if err != nil {
		logger.Warnf(ctx, "[ContainerManager] OnEvent: GetContainerInstanceByRuntimeID failed, runtime_id=%s event=%s err=%v", e.RuntimeID, e.Type, err)
		return
	}

	// Every runtime event is logged with the instance state it is about to be
	// applied to. This is the only place that shows whether a terminal event
	// arrived for a container that had actually been running: an exit reported while
	// the instance is still creating (or with no started_at) cannot have come from
	// the container finishing its work, and is the fingerprint of a premature or
	// stale runtime event.
	logger.Debugf(ctx, "[ContainerManager] runtime event received, runtime_id=%s event=%s message=%q owner_type=%s owner_id=%d instance_id=%d status=%s started_at=%v finished_at=%v exit_code=%s created_at=%v",
		e.RuntimeID, e.Type, e.Message, inst.OwnerType, inst.OwnerID, inst.ID, inst.Status, inst.StartedAt, inst.FinishedAt, formatExitCode(inst.ExitCode), inst.CreatedAt)

	// 防御性检查：如果收到终端事件但容器状态仍为 Creating，
	// 说明 ContainerStarted 事件可能丢失，先补偿 started 时间戳再处理。
	currentStatus := strings.ToLower(strings.TrimSpace(string(inst.Status)))
	isCreating := currentStatus == string(types.ContainerCreating)
	isTerminalEvent := e.Type == "ContainerExited" || e.Type == "ContainerFailed"
	if isCreating && isTerminalEvent {
		age := "unknown"
		if !inst.CreatedAt.IsZero() {
			age = time.Since(inst.CreatedAt).Round(time.Millisecond).String()
		}
		logger.Warnf(ctx, "[ContainerManager] received terminal event while container still creating, runtime_id=%s event=%s status=%s message=%q instance_age=%s started_at=%v exit_code=%s, compensating started_at",
			e.RuntimeID, e.Type, inst.Status, e.Message, age, inst.StartedAt, formatExitCode(inst.ExitCode))
		now := time.Now()
		inst.StartedAt = &now
		inst.FinishedAt = nil
		if rt, rtErr := m.getRuntimeByInstance(inst); rtErr == nil {
			m.syncInstanceIPAddress(ctx, rt, inst)
		}
		// 先尝试转 Running，失败也无妨（FSM 现在允许 Creating→Stopped）
		// _ = m.transition(ctx, inst, fsm.Running, "ContainerStarted")
		_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerRunning, "ContainerStarted")
	}

	switch e.Type {

	case "ContainerStarted":
		now := time.Now()
		inst.StartedAt = &now
		inst.FinishedAt = nil
		if rt, rtErr := m.getRuntimeByInstance(inst); rtErr == nil {
			m.syncInstanceIPAddress(ctx, rt, inst)
		}
		// _ = m.transition(ctx, inst, fsm.Running, "ContainerStarted")
		_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerRunning, "ContainerStarted")
		// 容器进入 running：active_count 变化（creating → running 仍是占用态，
		// 但 started_at / IP 等展示字段已就绪），推送一次让队列状态与列表刷新。
		m.broadcastContainerChanged(ctx, "ContainerStarted", inst)

	case "ContainerPaused":
		// _ = m.transition(ctx, inst, fsm.Paused, "ContainerPaused")
		_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerPaused, "ContainerPaused")

	case "ContainerResumed":
		now := time.Now()
		inst.StartedAt = &now
		inst.FinishedAt = nil
		if rt, rtErr := m.getRuntimeByInstance(inst); rtErr == nil {
			m.syncInstanceIPAddress(ctx, rt, inst)
		}
		// _ = m.transition(ctx, inst, fsm.Running, "ContainerResumed")
		_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerRunning, "ContainerResumed")

	case "ContainerStopped":
		now := time.Now()
		inst.FinishedAt = &now
		if code, ok := parseRuntimeExitCode(e.Message); ok {
			inst.ExitCode = &code
		}
		// _ = m.transition(ctx, inst, fsm.Stopped, "ContainerStopped")
		_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerStopped, "ContainerStopped")

	case "ContainerExited":
		now := time.Now()
		inst.FinishedAt = &now
		if code, ok := parseRuntimeExitCode(e.Message); ok {
			inst.ExitCode = &code
		}
		// _ = m.transition(ctx, inst, fsm.Stopped, "ContainerStopped")
		_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerStopped, "ContainerStopped")

	case "ContainerFailed":
		now := time.Now()
		inst.FinishedAt = &now
		if code, ok := parseRuntimeExitCode(e.Message); ok {
			inst.ExitCode = &code
		}
		// _ = m.transition(ctx, inst, fsm.Failed, "ContainerFailed")
		if inst.Status == types.ContainerReCreating {
			_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerCreatePending, OutboxEventTypeCreateRequest)

		} else {
			_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerFailed, "ContainerFailed")
		}
	case "ContainerDeleted":
		now := time.Now()
		inst.FinishedAt = &now
		// _ = m.transition(ctx, inst, fsm.Stopped, "ContainerStopped")

		// Publish delete event immediately so current subscribers can react in this callback flow.
		// if m.bus != nil {
		// 	m.bus.Publish(types.ContainerEvent{
		// 		ContainerInstanceID: inst.ID,
		// 		Event:               "ContainerDeleted",
		// 		Message:             e.Message,
		// 	})
		// }
		if inst.Status == types.ContainerReCreating {
			_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerCreatePending, OutboxEventTypeCreateRequest)

		} else {
			_ = m.TransitionContainerAndEnqueueOutbox(ctx, inst, types.ContainerDeleted, "ContainerDeleted")

			if err := m.containerRepo.DeleteContainerInstance(ctx, inst.ID); err != nil {
				logger.Warnf(ctx, "[ContainerManager] delete container instance failed, instance_id=%d runtime_id=%s err=%v", inst.ID, e.RuntimeID, err)
			}
			// 实例记录已删除：容器集合发生变化，广播给所有在线客户端刷新队列状态。
			m.broadcastContainerChanged(ctx, "ContainerDeleted", inst)
		}

		// default:
		// _ = m.createContainerEvent(context.Background(), inst.ID, e.Type, e.Message)
	}

	// Summary of what the event did to the instance. The status written here is what
	// the DAG completion coordinator later reads, so it has to be traceable back to
	// the runtime event that produced it.
	logger.Debugf(ctx, "[ContainerManager] runtime event applied, runtime_id=%s event=%s instance_id=%d status=%s exit_code=%s finished_at=%v",
		e.RuntimeID, e.Type, inst.ID, inst.Status, formatExitCode(inst.ExitCode), inst.FinishedAt)

	// 容器异常结束时再打一条带容器日志的 Warn：若失败发生在启动阶段
	// （例如镜像平台不匹配导致 "exec format error"），LOG_PATH 里的日志文件是空的，
	// 只有运行时日志能说明原因，这条是用户排查时唯一能看到的信息。
	if e.Type == "ContainerFailed" {
		m.logFailureEvidence(ctx, inst, e.Message)
	}

	// 通知状态等待方（例如终端容器创建方等待 ContainerStarted）。
	m.notifyInstanceStatus(inst.ID, inst.Status)
}

func parseRuntimeExitCode(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	code, err := strconv.Atoi(raw)
	if err != nil {
		return 0, false
	}
	return code, true
}

// formatExitCode renders an instance exit code for logs. "none" means no runtime
// event ever carried one, which distinguishes a reported process exit from a
// container that was only stopped or deleted.
func formatExitCode(code *int) string {
	if code == nil {
		return "none"
	}
	return strconv.Itoa(*code)
}

// func (m *ContainerManager) transition(
// 	ctx context.Context,
// 	inst *types.ContainerInstance,
// 	to fsm.State,
// 	eventType string,
// ) error {
// 	if inst == nil {
// 		return errors.New("container instance is nil")
// 	}

// 	return m.repo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
// 		latest, err := tx.GetContainerInstanceByID(ctx, inst.ID)
// 		if err != nil {
// 			return err
// 		}

// 		if latest.Status == types.ContainerStatus(to) {
// 			inst.Status = latest.Status
// 			return nil
// 		}

// 		f := &fsm.FSM{}
// 		if err := f.Transition(fsm.State(latest.Status), to); err != nil {
// 			return err
// 		}

// 		inst.Status = types.ContainerStatus(to)
// 		if err := tx.UpdateContainerInstance(ctx, inst); err != nil {
// 			return err
// 		}

// 		domainEvent := &types.ContainerEvent{
// 			ContainerInstanceID: inst.ID,
// 			Event:               eventType,
// 			Message:             string(to),
// 		}
// 		// if err := tx.CreateContainerEvent(ctx, domainEvent); err != nil {
// 		// 	return err
// 		// }

// 		payload, err := json.Marshal(domainEvent)
// 		if err != nil {
// 			return err
// 		}

// 		return tx.CreateOutboxEvent(ctx, &types.OutboxEvent{
// 			Type:    eventType,
// 			Payload: payload,
// 			Status:  "pending",
// 		})
// 	})
// }

func (m *ContainerManager) getRuntime() (containerruntime.Runtime, error) {
	return m.lifecycle.GetRuntime()
}

func (m *ContainerManager) resolveRuntimeName() string {
	return m.lifecycle.ResolveRuntimeName()
}

// GetRuntime 按当前配置解析默认运行时，语义与容器实际创建时完全一致
// （同为 ContainerLifecycle.GetRuntime，解析顺序见 ResolveContainerRuntime）。
// 供配置检测接口复用，保证「检测结果」与「真实执行」不会出现口径差异。
func (m *ContainerManager) GetRuntime() (containerruntime.Runtime, error) {
	return m.lifecycle.GetRuntime()
}

// ResolveRuntimeName 解析当前生效的运行时名称，语义与容器实际执行时一致
// （ContainerLifecycle.ResolveRuntimeName）。
func (m *ContainerManager) ResolveRuntimeName() string {
	return m.lifecycle.ResolveRuntimeName()
}

// getRuntimeByInstance resolves a runtime from a container instance's RuntimeName.
func (m *ContainerManager) getRuntimeByInstance(inst *types.ContainerInstance) (containerruntime.Runtime, error) {
	return m.lifecycle.GetRuntimeByInstance(inst)
}

// func (m *ContainerManager) getRuntimeByInstance(inst *types.ContainerInstance) (containerruntime.Runtime, error) {
// 	if inst == nil {
// 		return nil, errors.New("container instance is nil")
// 	}

// 	for _, item := range m.reg.List() {
// 		if strings.HasPrefix(inst.RuntimeID, item.Name()+"-") {
// 			return item, nil
// 		}
// 	}

// 	items := m.reg.List()
// 	if len(items) == 1 {
// 		return items[0], nil
// 	}

// 	return nil, fmt.Errorf("failed to resolve runtime for instance %d", inst.ID)
// }

// func (m *ContainerManager) getInstanceAndRuntime(ctx context.Context, id int64) (*types.ContainerInstance, containerruntime.Runtime, error) {
// inst, err := m.repo.GetContainerInstanceByID(ctx, id)
// if err != nil {
// 	return nil, nil, err
// }
// rt, err := m.getRuntimeByInstance(inst)
// if err != nil {
// 	return nil, nil, err
// }
// 	return inst, rt, nil
// }

// func (m *ContainerManager) createContainerEvent(ctx context.Context, instanceID int64, evt string, msg string) error {
// 	return m.repo.CreateContainerEvent(ctx, &types.ContainerEvent{
// 		ContainerInstanceID: instanceID,
// 		Event:               evt,
// 		Message:             msg,
// 	})
// // // }

// func (m *ContainerManager) enqueueLifecycleRequest(
// 	ctx context.Context,
// 	instanceID int64,
// 	to fsm.State,
// 	requestType string,
// 	requestPayload interface{},
// ) error {
// 	return m.repo.WithTransaction(ctx, func(tx interfaces.ContainerRepository) error {
// 		latest, err := tx.GetContainerInstanceByID(ctx, instanceID)
// 		if err != nil {
// 			return err
// 		}
// 		// ctx, id, fsm.StartPending, OutboxEventTypeStartRequest, req

// 		if latest.Status != types.ContainerStatus(to) {
// 			f := &fsm.FSM{}
// 			if err := f.Transition(fsm.State(latest.Status), to); err != nil {
// 				return err
// 			}

// 			latest.Status = types.ContainerStatus(to)
// 			if err := tx.UpdateContainerInstance(ctx, latest); err != nil {
// 				return err
// 			}

// 			// domainEvent := &types.ContainerEvent{
// 			// 	ContainerInstanceID: latest.ID,
// 			// 	Event:               transitionEventType,
// 			// 	Message:             string(to),
// 			// }
// 			// if err := tx.CreateContainerEvent(ctx, domainEvent); err != nil {
// 			// 	return err
// 			// }
// 		}

// 		payload, err := json.Marshal(requestPayload)
// 		if err != nil {
// 			return err
// 		}

// 		return tx.CreateOutboxEvent(ctx, &types.OutboxEvent{
// 			Type:    requestType,
// 			Payload: payload,
// 			Status:  "pending",
// 		})
// 	})
// }

func setRuntimeVar(vars map[string]string, key string, value string) {
	if vars == nil {
		return
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" || value == "" {
		return
	}
	vars[key] = value
}

func ensureEmptyFileIfNotExists(ctx context.Context, paths *containerruntime.PathMapper, filePath string) {
	// 调用方传入的可能是容器内的 POSIX 路径（变量由 PathMapper.Join 生成），
	// 落盘前统一经 PathMapper 还原成宿主路径。
	filePath = paths.Host(filePath)
	if filePath == "" {
		return
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		logger.Warnf(ctx, "[ContainerManager] create profile directory failed, path=%s err=%v", filePath, err)
		return
	}

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return
		}
		logger.Warnf(ctx, "[ContainerManager] create empty profile failed, path=%s err=%v", filePath, err)
		return
	}
	_ = file.Close()
}

func parseCommand(raw string) []string {
	cmd := strings.TrimSpace(raw)
	if cmd == "" {
		return nil
	}
	return strings.Fields(cmd)
}

func applyDagNodeTaskSpec(spec *types.ContainerSpec, logPath string) {
	if spec == nil {
		return
	}

	quotedLogPath := shellSingleQuote(strings.TrimSpace(logPath))
	script := "bash ./run.sh 2>&1 | tee " + quotedLogPath + "; exit ${PIPESTATUS[0]}"

	spec.Entrypoint = []string{"bash"}
	spec.Command = []string{"-c", script}
}

// applyDagNodeRuntimeSpec 按 DAG 节点的约定覆盖容器启动命令（bash ./run.sh）与运行用户。
func (m *ContainerManager) applyDagNodeRuntimeSpec(spec *types.ContainerSpec, resolveVars map[string]string) {
	m.lifecycle.ApplyDagNodeRuntimeSpec(spec, resolveVars)
}

// applyTaskRuntimeSpec 是任务型容器（DAG 节点）的启动约定：
// 统一入口为 bash ./run.sh、输出 tee 到 LOG_PATH，并按运行时映射运行用户。
func (m *ContainerManager) applyTaskRuntimeSpec(spec *types.ContainerSpec, resolveVars map[string]string) {
	m.lifecycle.applyTaskRuntimeSpec(spec, resolveVars)
}

// defaultRuntimeHomeDir 是非 root 用户运行容器时可写的家目录（内存盘挂载点）。
const defaultRuntimeHomeDir = "/home/app"

// applyRuntimeUser maps USERID/GROUPID to the runtime's user field（薄委托到 ContainerLifecycle）。
func (m *ContainerManager) applyRuntimeUser(spec *types.ContainerSpec, resolveVars map[string]string) {
	m.lifecycle.ApplyRuntimeUser(spec, resolveVars)
}

// applyRuntimeHome 为非 root 运行的容器准备可写的 HOME（薄委托到 ContainerLifecycle）。
func (m *ContainerManager) applyRuntimeHome(spec *types.ContainerSpec, uid, gid string) {
	m.lifecycle.applyRuntimeHome(spec, uid, gid)
}

func shellSingleQuote(text string) string {
	value := strings.TrimSpace(text)
	if value == "" {
		value = "./run.log"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func sanitizeKubernetesResourceName(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return ""
	}

	b := strings.Builder{}
	b.Grow(len(raw))
	lastDash := false
	for _, r := range raw {
		isAlphaNum := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if isAlphaNum {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteRune('-')
			lastDash = true
		}
	}

	value := strings.Trim(b.String(), "-")
	if len(value) > 50 {
		value = strings.Trim(value[:50], "-")
	}
	return value
}

func (m *ContainerManager) syncInstanceIPAddress(ctx context.Context, rt containerruntime.Runtime, inst *types.ContainerInstance) {
	if inst == nil || rt == nil {
		return
	}

	inspector, ok := rt.(containerruntime.RuntimeInspector)
	if !ok {
		return
	}

	inspection, err := inspector.Inspect(ctx, inst.RuntimeID)
	if err != nil {
		logger.Warnf(ctx, "[ContainerManager] inspect runtime ip failed, instance_id=%d err=%v", inst.ID, err)
		return
	}
	if inspection == nil {
		return
	}

	ip := strings.TrimSpace(inspection.IPAddress)
	nodeName := strings.TrimSpace(inspection.NodeName)

	changed := false
	if ip != "" && ip != inst.IPAddress {
		inst.IPAddress = ip
		changed = true
	}
	if nodeName != "" && nodeName != inst.RuntimeNodeName {
		inst.RuntimeNodeName = nodeName
		changed = true
	}

	if !changed {
		return
	}

	if err := m.containerRepo.UpdateContainerInstance(ctx, inst); err != nil {
		logger.Warnf(ctx, "[ContainerManager] persist runtime inspect failed, instance_id=%d ip=%s node=%s err=%v", inst.ID, inst.IPAddress, inst.RuntimeNodeName, err)
	}
}

func parseEnv(raw []byte) map[string]string {
	if len(raw) == 0 {
		return map[string]string{}
	}

	obj := map[string]string{}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return obj
	}

	mixedObj := map[string]interface{}{}
	if err := json.Unmarshal(raw, &mixedObj); err == nil {
		out := map[string]string{}
		for rawKey, val := range mixedObj {
			k := strings.TrimSpace(rawKey)
			if k == "" {
				continue
			}
			if text, ok := normalizeScalarValue(val); ok {
				out[k] = text
			}
		}
		return out
	}

	pairs := []map[string]string{}
	if err := json.Unmarshal(raw, &pairs); err == nil {
		out := map[string]string{}
		for _, kv := range pairs {
			k := strings.TrimSpace(kv["key"])
			if k == "" {
				continue
			}
			out[k] = kv["value"]
		}
		return out
	}

	return map[string]string{}
}

func parseVolumes(raw []byte, ownerType types.ContainerOwnerType) []types.ContainerVolume {
	if len(raw) == 0 {
		return nil
	}

	// obj := map[string]map[string]interface{}{}
	// if err := json.Unmarshal(raw, &obj); err == nil {
	// 	out := make([]types.ContainerVolume, 0, len(obj))
	// 	for rawTarget, item := range obj {
	// 		target := strings.TrimSpace(rawTarget)
	// 		if target == "" {
	// 			continue
	// 		}

	// 		source := target
	// 		if bind, ok := item["bind"]; ok {
	// 			if text, ok := normalizeScalarValue(bind); ok {
	// 				source = strings.TrimSpace(text)
	// 			}
	// 		}
	// 		mode := ""
	// 		if rawMode, ok := item["mode"]; ok {
	// 			if text, ok := normalizeScalarValue(rawMode); ok {
	// 				mode = strings.TrimSpace(text)
	// 			}
	// 		}
	// 		volType := ""
	// 		if rawType, ok := item["type"]; ok {
	// 			if text, ok := normalizeScalarValue(rawType); ok {
	// 				volType = strings.TrimSpace(text)
	// 			}
	// 		}
	// 		if rawOwner, ok := item["owner"]; ok {
	// 			if text, ok := normalizeScalarValue(rawOwner); ok {
	// 				if text != string(ownerType) {
	// 					continue
	// 				}
	// 			}
	// 		}

	// 		if source == "" || target == "" {
	// 			continue
	// 		}
	// 		out = append(out, types.ContainerVolume{Source: source, Target: target, Mode: mode, Type: volType})
	// 	}
	// 	return out
	// }

	volumes := []types.ContainerVolume{}
	if err := json.Unmarshal(raw, &volumes); err != nil {
		return nil
	}

	out := make([]types.ContainerVolume, 0, len(volumes))
	for _, vol := range volumes {
		source := strings.TrimSpace(vol.Source)
		target := strings.TrimSpace(vol.Target)
		if source == "" || target == "" {
			continue
		}
		if vol.Owner != "" && strings.TrimSpace(vol.Owner) != string(ownerType) {
			continue
		}
		out = append(out, types.ContainerVolume{
			Source: source,
			Target: target,
			Mode:   strings.TrimSpace(vol.Mode),
			Type:   strings.TrimSpace(vol.Type),
		})
	}

	return out
}

func parseSchedulingConstraint(raw []byte) *types.ContainerSchedulingSelector {
	if len(raw) == 0 {
		return nil
	}

	parsed := &types.ContainerSchedulingSelector{}
	if err := json.Unmarshal(raw, parsed); err != nil {
		return nil
	}

	constraints := make([]types.ContainerSchedulingConstraint, 0, len(parsed.Constraints))
	for _, item := range parsed.Constraints {
		constraintType := strings.TrimSpace(item.Type)
		key := strings.TrimSpace(item.Key)
		operator := strings.TrimSpace(item.Operator)
		if constraintType == "" || key == "" || operator == "" {
			continue
		}

		values := make([]string, 0, len(item.Values))
		seen := make(map[string]struct{}, len(item.Values))
		for _, rawValue := range item.Values {
			value := strings.TrimSpace(rawValue)
			if value == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			values = append(values, value)
		}

		constraints = append(constraints, types.ContainerSchedulingConstraint{
			Type:     constraintType,
			Key:      key,
			Operator: operator,
			Values:   values,
		})
	}

	if len(constraints) == 0 {
		return nil
	}

	return &types.ContainerSchedulingSelector{Constraints: constraints}
}

func normalizeScalarValue(raw interface{}) (string, bool) {
	switch v := raw.(type) {
	case nil:
		return "", false
	case string:
		return v, true
	case bool:
		return strconv.FormatBool(v), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case json.Number:
		return v.String(), true
	case int:
		return strconv.Itoa(v), true
	case int8:
		return strconv.FormatInt(int64(v), 10), true
	case int16:
		return strconv.FormatInt(int64(v), 10), true
	case int32:
		return strconv.FormatInt(int64(v), 10), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case uint:
		return strconv.FormatUint(uint64(v), 10), true
	case uint8:
		return strconv.FormatUint(uint64(v), 10), true
	case uint16:
		return strconv.FormatUint(uint64(v), 10), true
	case uint32:
		return strconv.FormatUint(uint64(v), 10), true
	case uint64:
		return strconv.FormatUint(v, 10), true
	default:
		return "", false
	}
}

// func Init() *ContainerManager {

// 	bus := event.NewMemoryBus()

// 	reg := runtime.NewRegistry()

// 	docker := &docker.DockerRuntime{}

// 	reg.Register("docker", docker)

// 	manager := &ContainerManager{
// 		repo: repository.NewContainerRepo(),
// 		reg:  reg,
// 		bus:  bus,
// 	}

// 	// Runtime → Manager
// 	docker.SetEventHandler(manager)

// 	return manager
// }
