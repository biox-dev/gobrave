// Package session 定义多轮对话的会话（Session）模型与增删改查服务。
//
// 参考 opencode 的分层方式：Session 只承载会话自身的数据库字段（不内嵌消息），
// 消息由 message 包独立维护（agent_messages 表按 session_id 关联）。这样：
//
//   - 会话列表 / 详情可以分别按需读取消息，避免每次加载会话都带上完整历史；
//   - 工具（如后续的 CoderAgentTools）可以只依赖 session.Service / message.Service
//     即可读写会话与消息，而不必感知上层的轮次编排。
package session

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// ErrNotFound 表示会话不存在。
var ErrNotFound = errors.New("session: not found")

// ErrInvalidParentSession 表示创建子会话时父会话 ID 无效。
var ErrInvalidParentSession = errors.New("session: parent session id is required")

// Session 是一次多轮对话的持久化对象。
//
// 它只包含会话自身的字段；历史消息保存在 agent_messages 表，由 message.Service
// 按 SessionID 查询，因此这里不再内嵌 Messages。
type Session struct {
	ID int64 `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	// ParentSessionID 指向父会话（如子任务会话 / 标题生成会话），0 表示顶层会话。
	ParentSessionID int64  `json:"parent_session_id,string" gorm:"column:parent_session_id;type:bigint;index"`
	Title           string `json:"title" gorm:"column:title;type:varchar(255)"`
	UserID          int64  `json:"user_id,string" gorm:"column:user_id;type:bigint;index"`
	Provider        string `json:"provider" gorm:"column:provider;type:varchar(64)"`
	Model           string `json:"model" gorm:"column:model;type:varchar(128)"`
	// CurrentTaskID 记录当前活跃轮次（running / waiting_permission）的任务 ID；
	// 轮次结束时置空。供前端刷新 / 切换会话时恢复实时流。
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

// CreateParams 是创建会话的入参。
type CreateParams struct {
	// ID 可选：指定会话主键（如 CreateTaskSession 以 toolCallID 作为会话 ID）；
	// 为 0 时由 BeforeCreate 分配雪花 ID。
	ID              int64
	ParentSessionID int64
	Title           string
	UserID          int64
	Provider        string
	Model           string
}

// ChildSessionParams 是创建子会话的入参（父会话必填，主键由雪花 ID 生成）。
type ChildSessionParams struct {
	// ParentSessionID 是父会话 ID，必须 > 0。
	ParentSessionID int64
	Title           string
	UserID          int64
	Provider        string
	Model           string
}

// ListParams 是会话分页查询的入参。
type ListParams struct {
	UserID int64 // 0 表示全部用户
	Offset int
	Limit  int
}

// Store 是 Session 的持久化端口，由 repository 层实现。
type Store interface {
	Create(ctx context.Context, s *Session) error
	Get(ctx context.Context, id int64) (*Session, error)
	Update(ctx context.Context, s *Session) error
	Delete(ctx context.Context, id int64) error
	List(ctx context.Context, params ListParams) ([]*Session, int64, error)
}

// Service 提供会话的增删改查（与 opencode session.Service 形式一致）。
type Service interface {
	Create(ctx context.Context, params CreateParams) (Session, error)
	// CreateTitleSession 为 parentSessionID 创建一个「生成标题」子会话。
	CreateTitleSession(ctx context.Context, parentSessionID int64) (Session, error)
	// CreateTaskSession 以 toolCallID 作为会话 ID 创建子任务会话。
	CreateTaskSession(ctx context.Context, toolCallID, parentSessionID int64, title string) (Session, error)
	// CreateChildSession 创建 ParentSessionID 指向父会话的子会话（主键由雪花 ID 生成）。
	//
	// 推荐用于子 Agent / 其它“附属会话”场景：不复用外部 ID 作为主键，避免厂商标识污染与会话主键冲突。
	CreateChildSession(ctx context.Context, params ChildSessionParams) (Session, error)
	Get(ctx context.Context, id int64) (Session, error)
	List(ctx context.Context, params ListParams) ([]Session, int64, error)
	Save(ctx context.Context, session Session) (Session, error)
	Delete(ctx context.Context, id int64) error
}

type service struct {
	store Store
}

// NewService 创建会话服务。
func NewService(store Store) Service {
	return &service{store: store}
}

func (s *service) Create(ctx context.Context, params CreateParams) (Session, error) {
	now := time.Now()
	session := &Session{
		ID:              params.ID,
		ParentSessionID: params.ParentSessionID,
		Title:           params.Title,
		UserID:          params.UserID,
		Provider:        params.Provider,
		Model:           params.Model,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.store.Create(ctx, session); err != nil {
		return Session{}, err
	}
	return *session, nil
}

func (s *service) CreateTaskSession(ctx context.Context, toolCallID, parentSessionID int64, title string) (Session, error) {
	return s.Create(ctx, CreateParams{
		ID:              toolCallID,
		ParentSessionID: parentSessionID,
		Title:           title,
	})
}

// CreateChildSession 创建 ParentSessionID 指向父会话的子会话。
func (s *service) CreateChildSession(ctx context.Context, params ChildSessionParams) (Session, error) {
	if params.ParentSessionID <= 0 {
		return Session{}, fmt.Errorf("%w: %d", ErrInvalidParentSession, params.ParentSessionID)
	}
	return s.Create(ctx, CreateParams{
		ParentSessionID: params.ParentSessionID,
		Title:           params.Title,
		UserID:          params.UserID,
		Provider:        params.Provider,
		Model:           params.Model,
	})
}

func (s *service) CreateTitleSession(ctx context.Context, parentSessionID int64) (Session, error) {
	return s.Create(ctx, CreateParams{
		ParentSessionID: parentSessionID,
		Title:           TitleSessionTitle,
	})
}

func (s *service) Get(ctx context.Context, id int64) (Session, error) {
	session, err := s.store.Get(ctx, id)
	if err != nil {
		return Session{}, err
	}
	return *session, nil
}

func (s *service) List(ctx context.Context, params ListParams) ([]Session, int64, error) {
	rows, total, err := s.store.List(ctx, params)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Session, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row)
	}
	return out, total, nil
}

func (s *service) Save(ctx context.Context, session Session) (Session, error) {
	session.UpdatedAt = time.Now()
	if err := s.store.Update(ctx, &session); err != nil {
		return Session{}, err
	}
	return session, nil
}

func (s *service) Delete(ctx context.Context, id int64) error {
	return s.store.Delete(ctx, id)
}
