package agent

import (
	"errors"
	"time"
)

// PermissionStatus 是权限请求的生命周期状态。
//
// 状态机（详见 design.md 第 8 节）：
//
//	pending → approved → consumed
//	pending → denied / expired / canceled
type PermissionStatus string

const (
	PermissionPending  PermissionStatus = "pending"  // 等待人工确认
	PermissionApproved PermissionStatus = "approved" // 已批准
	PermissionDenied   PermissionStatus = "denied"   // 已拒绝
	PermissionExpired  PermissionStatus = "expired"  // 已过期
	PermissionCanceled PermissionStatus = "canceled" // 已取消（任务被取消等）
	PermissionConsumed PermissionStatus = "consumed" // 已消费：Agent 已恢复并继续执行
)

// 权限相关错误。
var (
	// ErrPermissionDenied 表示权限请求被拒绝，或策略直接判定为 deny。
	ErrPermissionDenied = errors.New("agent: permission denied")
	// ErrPermissionNotFound 表示权限请求不存在。
	ErrPermissionNotFound = errors.New("agent: permission request not found")
	// ErrPermissionNotPending 表示尝试对非 pending 状态的权限请求做批准 / 拒绝。
	ErrPermissionNotPending = errors.New("agent: permission request is not pending")
	// ErrPermissionExpired 表示等待的权限请求已过期。
	ErrPermissionExpired = errors.New("agent: permission request expired")
	// ErrPermissionCanceled 表示等待的权限请求已取消。
	ErrPermissionCanceled = errors.New("agent: permission request canceled")
	// ErrInvalidPermissionTransition 表示非法的权限状态迁移。
	ErrInvalidPermissionTransition = errors.New("agent: invalid permission state transition")
)

// PermissionRequest 记录一次权限请求及其决策结果，是权限域的持久化对象。
//
// 它是“数据库才是状态源”这一原则的载体：Agent 阻塞等待的只是内存中的 channel，
// 真正的状态始终保存在 Repository 中，因此浏览器刷新 / 后端重启后仍可恢复。
type PermissionRequest struct {
	ID        string           `json:"id"`
	TaskID    string           `json:"task_id"`
	SessionID string           `json:"session_id,omitempty"`
	Operation Operation        `json:"operation"`
	Status    PermissionStatus `json:"status"`

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	ResolvedBy *string    `json:"resolved_by,omitempty"`
}

// NewPermissionRequest 构建一个 pending 状态的权限请求。
func NewPermissionRequest(taskID, sessionID string, operation Operation) *PermissionRequest {
	now := time.Now()
	return &PermissionRequest{
		ID:        newID("perm"),
		TaskID:    taskID,
		SessionID: sessionID,
		Operation: operation,
		Status:    PermissionPending,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// TransitionTo 校验并执行权限状态迁移；非法迁移返回 false 且不改变状态。
func (p *PermissionRequest) TransitionTo(next PermissionStatus) bool {
	if !canTransitionPermission(p.Status, next) {
		return false
	}
	p.Status = next
	p.UpdatedAt = time.Now()
	return true
}

func canTransitionPermission(from, to PermissionStatus) bool {
	switch from {
	case PermissionPending:
		return to == PermissionApproved ||
			to == PermissionDenied ||
			to == PermissionExpired ||
			to == PermissionCanceled
	case PermissionApproved:
		return to == PermissionConsumed
	default:
		// denied / expired / canceled / consumed 均为终态。
		return false
	}
}

// Decision 把当前状态映射为 Agent 可消费的决策结果。
func (p *PermissionRequest) Decision() PermissionDecision {
	switch p.Status {
	case PermissionApproved, PermissionConsumed:
		return DecisionAllow
	default:
		return DecisionDeny
	}
}
