package types

import (
	"errors"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// ErrSessionNotFound 表示会话不存在。
var ErrSessionNotFound = errors.New("session: not found")

// ErrInvalidParentSession 表示创建子会话时父会话 ID 无效。
var ErrInvalidParentSession = errors.New("session: parent session id is required")

// Session 是一次多轮对话的持久化对象。
//
// 它只包含会话自身的字段；历史消息保存在 agent_messages 表，由 message.Service
// 按 SessionID 查询，因此这里不再内嵌 Messages。
type Session struct {
	ID int64 `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	// ProjectID 是会话所属项目（创建时取用户当前激活项目），会话列表按项目隔离。
	ProjectID int64 `json:"project_id,string" gorm:"column:project_id;type:bigint;index"`
	// ParentSessionID 指向父会话（如子任务会话 / 标题生成会话），0 表示顶层会话。
	ParentSessionID int64  `json:"parent_session_id,string" gorm:"column:parent_session_id;type:bigint;index"`
	Title           string `json:"title" gorm:"column:title;type:varchar(255)"`
	UserID          int64  `json:"user_id,string" gorm:"column:user_id;type:bigint;index"`
	Provider        string `json:"provider" gorm:"column:provider;type:varchar(64)"`
	Model           string `json:"model" gorm:"column:model;type:varchar(128)"`
	// CurrentTaskID 记录当前活跃轮次（running / waiting_permission）的任务 ID；
	// 轮次正常结束时置空。failed 但仍有检查点（可续跑）的任务会保留该指针，
	// 供前端刷新 / 切换会话时展示「Continue」并从 /agent/task/resume 恢复执行。
	CurrentTaskID int64     `json:"current_task_id,string" gorm:"column:current_task_id;type:bigint"`
	CreatedAt     time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt     time.Time `json:"updated_at" gorm:"column:updated_at"`
}

// TableName 返回会话表的表名。
func (Session) TableName() string { return "agent_sessions" }

// BeforeCreate 在写入数据库前用雪花 ID 初始化主键。
func (s *Session) BeforeCreate(_ *gorm.DB) error {
	if s.ID == 0 {
		s.ID = utils.GenerateID()
	}
	return nil
}

// TitleSessionTitle 是标题生成子会话的固定标题（与 opencode 保持一致）。
const TitleSessionTitle = "Generate a title"

// SessionCreateParams 是创建会话的入参。
type SessionCreateParams struct {
	// ID 可选：指定会话主键（如 CreateTaskSession 以 toolCallID 作为会话 ID）；
	// 为 0 时由 BeforeCreate 分配雪花 ID。
	ID int64
	// ProjectID 是会话所属项目；0 表示未归属项目。
	ProjectID       int64
	ParentSessionID int64
	Title           string
	UserID          int64
	Provider        string
	Model           string
}

// SessionChildParams 是创建子会话的入参（父会话必填，主键由雪花 ID 生成）。
type SessionChildParams struct {
	// ProjectID 是会话所属项目；通常与父会话保持一致。
	ProjectID int64
	// ParentSessionID 是父会话 ID，必须 > 0。
	ParentSessionID int64
	Title           string
	UserID          int64
	Provider        string
	Model           string
}

// SessionListParams 是会话分页查询的入参。
type SessionListParams struct {
	UserID int64 // 0 表示全部用户
	// ProjectID 限定只返回该项目的会话；0 表示不按项目过滤。
	ProjectID int64
	Offset    int
	Limit     int
}
