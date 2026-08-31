package agent

import (
	"errors"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
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
	ID        int64            `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	TaskID    int64            `json:"task_id,string" gorm:"column:task_id;type:bigint;index"`
	SessionID string           `json:"session_id,omitempty" gorm:"column:session_id;type:varchar(64)"`
	Operation Operation        `json:"operation" gorm:"serializer:json"`
	Status    PermissionStatus `json:"status" gorm:"column:status;type:varchar(32);index"`

	CreatedAt  time.Time  `json:"created_at" gorm:"column:created_at"`
	UpdatedAt  time.Time  `json:"updated_at" gorm:"column:updated_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty" gorm:"column:resolved_at"`
	ResolvedBy *string    `json:"resolved_by,omitempty" gorm:"column:resolved_by;type:varchar(64)"`
}

// TableName 返回权限请求表的表名。
func (PermissionRequest) TableName() string { return "agent_permission_requests" }

// BeforeCreate 在写入数据库前用雪花 ID 初始化主键。
func (p *PermissionRequest) BeforeCreate(_ *gorm.DB) error {
	if p.ID == 0 {
		p.ID = utils.GenerateID()
	}
	return nil
}

// NewPermissionRequest 构建一个 pending 状态的权限请求。
func NewPermissionRequest(taskID int64, sessionID string, operation Operation) *PermissionRequest {
	now := time.Now()
	return &PermissionRequest{
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
