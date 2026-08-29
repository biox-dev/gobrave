package agent

import (
	"context"
	"sync"
	"time"
)

// PermissionManager 负责权限请求的创建、持久化与决策流转。
//
// 它是“数据库才是状态源”的核心实现：
//   - Create：把权限请求写入 Repository（pending），等待 UI 确认；
//   - Approve / Deny：更新 Repository 状态，并唤醒正在阻塞等待的 Agent；
//   - Wait：Agent 侧阻塞等待决策（内存 channel 只是运行时同步机制，真正状态在 Repository）。
//
// 注意：事件广播由上层 AgentService 负责（单一事件出口），PermissionManager 只维护状态。
// Approve / Deny 与 Wait 通过互斥锁 + waiter channel 协同，避免“决策先于等待注册”的竞态。
type PermissionManager struct {
	repo PermissionRepository

	mu      sync.Mutex
	waiters map[string]chan PermissionDecision // permissionID → 决策通道
}

// NewPermissionManager 创建权限管理器。
func NewPermissionManager(repo PermissionRepository) *PermissionManager {
	return &PermissionManager{
		repo:    repo,
		waiters: make(map[string]chan PermissionDecision),
	}
}

// Create 创建一个 pending 状态的权限请求并返回。
func (m *PermissionManager) Create(ctx context.Context, taskID, sessionID string, operation Operation) (*PermissionRequest, error) {
	p := NewPermissionRequest(taskID, sessionID, operation)
	if err := m.repo.Create(ctx, p); err != nil {
		return nil, err
	}
	return p, nil
}

// Get 按 ID 查询权限请求。
func (m *PermissionManager) Get(ctx context.Context, id string) (*PermissionRequest, error) {
	return m.repo.Get(ctx, id)
}

// GetPending 返回某任务当前全部待确认的权限请求。
func (m *PermissionManager) GetPending(ctx context.Context, taskID string) ([]*PermissionRequest, error) {
	return m.repo.ListPendingByTask(ctx, taskID)
}

// Page 分页查询权限请求；taskID 为空表示全部任务，statuses 为空表示全部状态。
func (m *PermissionManager) Page(ctx context.Context, offset, limit int, taskID string, statuses ...PermissionStatus) ([]*PermissionRequest, int64, error) {
	return m.repo.Page(ctx, offset, limit, taskID, statuses...)
}

// Approve 批准权限请求，返回更新后的权限请求；并唤醒正在等待的 Agent。
func (m *PermissionManager) Approve(ctx context.Context, id, by string) (*PermissionRequest, error) {
	return m.resolve(ctx, id, PermissionApproved, DecisionAllow, by)
}

// Deny 拒绝权限请求，返回更新后的权限请求；并唤醒正在等待的 Agent。
func (m *PermissionManager) Deny(ctx context.Context, id, by string) (*PermissionRequest, error) {
	return m.resolve(ctx, id, PermissionDenied, DecisionDeny, by)
}

// Wait 阻塞等待权限请求的决策。
//
// 返回：
//   - DecisionAllow：已批准（批准后立即把请求标记为 consumed）；
//   - DecisionDeny：已拒绝 / 过期 / 取消；
//   - error：ctx 取消等。
func (m *PermissionManager) Wait(ctx context.Context, id string) (PermissionDecision, error) {
	m.mu.Lock()
	p, err := m.repo.Get(ctx, id)
	if err != nil {
		m.mu.Unlock()
		return "", err
	}

	// 快路径：请求已经被决策（例如后端重启后恢复，或 approve 先于 wait 到达）。
	switch p.Status {
	case PermissionApproved:
		m.mu.Unlock()
		_ = m.consume(ctx, id)
		return DecisionAllow, nil
	case PermissionDenied:
		m.mu.Unlock()
		return DecisionDeny, nil
	case PermissionExpired:
		m.mu.Unlock()
		return DecisionDeny, ErrPermissionExpired
	case PermissionCanceled:
		m.mu.Unlock()
		return DecisionDeny, ErrPermissionCanceled
	}

	// 慢路径：pending，注册 waiter 后阻塞。
	ch := make(chan PermissionDecision, 1)
	m.waiters[id] = ch
	m.mu.Unlock()

	select {
	case decision := <-ch:
		if decision == DecisionAllow {
			_ = m.consume(ctx, id)
		}
		return decision, nil
	case <-ctx.Done():
		m.mu.Lock()
		delete(m.waiters, id)
		m.mu.Unlock()
		return "", ctx.Err()
	}
}

// resolve 是 Approve / Deny 的公共实现：校验状态 → 持久化 → 唤醒 waiter。
// 返回更新后的权限请求，供上层广播“权限已决策”事件。
func (m *PermissionManager) resolve(ctx context.Context, id string, status PermissionStatus, decision PermissionDecision, by string) (*PermissionRequest, error) {
	m.mu.Lock()
	p, err := m.repo.Get(ctx, id)
	if err != nil {
		m.mu.Unlock()
		return nil, err
	}
	if p.Status != PermissionPending {
		m.mu.Unlock()
		return nil, ErrPermissionNotPending
	}
	if !p.TransitionTo(status) {
		m.mu.Unlock()
		return nil, ErrInvalidPermissionTransition
	}
	now := time.Now()
	p.ResolvedAt = &now
	p.ResolvedBy = &by
	if err := m.repo.Update(ctx, p); err != nil {
		m.mu.Unlock()
		return nil, err
	}

	ch, ok := m.waiters[id]
	if ok {
		delete(m.waiters, id)
	}
	m.mu.Unlock()

	// 唤醒阻塞中的 Agent（非阻塞投递，避免无人等待时卡住）。
	if ok {
		ch <- decision
	}
	return p, nil
}

// consume 把已批准的权限请求标记为 consumed（Agent 已恢复执行）。
func (m *PermissionManager) consume(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	p, err := m.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if p.Status != PermissionApproved {
		return nil
	}
	if !p.TransitionTo(PermissionConsumed) {
		return ErrInvalidPermissionTransition
	}
	return m.repo.Update(ctx, p)
}
