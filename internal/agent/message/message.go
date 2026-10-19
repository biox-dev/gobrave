// Package message 定义会话消息（Message）模型与增删改查服务。
//
// 参考 opencode 的分层方式：消息按 session_id 归属会话，独立持久化；Session 不内嵌
// 消息列表。Message 同时作为 Agent 请求中的对话消息（agent.Message 是其类型别名），
// 使「持久化历史」与「拼装 prompt」共用同一套字段。
package message

import (
	"context"
	"errors"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// ErrNotFound 表示消息不存在。
var ErrNotFound = errors.New("message: not found")

// 消息角色。
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// 消息类型（会话层可回放的消息种类）。
const (
	KindUser           = "user"
	KindAssistant      = "assistant"
	KindAssistantFinal = "assistant_final"
	KindReasoning      = "reasoning"
	KindToolCall       = "tool_call"
	KindToolResult     = "tool_result"
)

// Message 是一条会话消息，以独立表持久化。
//
// 它既用于落库（ID / SessionID / Seq / CreatedAt），也用于拼装 Agent 请求
// （Role / Content / Kind / TaskID / Data），因此 agent.Request.Messages 直接复用该类型。
type Message struct {
	ID        int64  `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	SessionID int64  `json:"session_id,string" gorm:"column:session_id;type:bigint;index"`
	Seq       int    `json:"seq" gorm:"column:seq;index"`
	Role      string `json:"role" gorm:"column:role;type:varchar(32)"`
	Kind      string `json:"kind,omitempty" gorm:"column:kind;type:varchar(32);index"`
	TaskID    int64  `json:"task_id,string,omitempty" gorm:"column:task_id;type:bigint;index"`
	// Content 可能包含完整工具输出 / 长回复，64KB 的 text 会触发 1406，这里用 longtext。
	Content   string    `json:"content,omitempty" gorm:"column:content;type:longtext"`
	Data      any       `json:"data,omitempty" gorm:"serializer:json"`
	CreatedAt time.Time `json:"created_at" gorm:"column:created_at"`
}

// TableName 返回消息表的表名。
func (Message) TableName() string { return "agent_messages" }

// BeforeCreate 在写入数据库前用雪花 ID 初始化主键。
func (m *Message) BeforeCreate(_ *gorm.DB) error {
	if m.ID == 0 {
		m.ID = utils.GenerateID()
	}
	return nil
}

// CreateParams 是写入一条消息的入参。
type CreateParams struct {
	SessionID int64
	Role      string
	Kind      string
	TaskID    int64
	Content   string
	Data      any
}

// Store 是 Message 的持久化端口，由 repository 层实现。
type Store interface {
	Create(ctx context.Context, m *Message) error
	Update(ctx context.Context, m *Message) error
	Get(ctx context.Context, id int64) (*Message, error)
	List(ctx context.Context, sessionID int64) ([]*Message, error)
	Delete(ctx context.Context, id int64) error
	DeleteBySession(ctx context.Context, sessionID int64) error
}

// Service 提供消息的增删改查（与 opencode message.Service 形式一致）。
type Service interface {
	Create(ctx context.Context, params CreateParams) (Message, error)
	Update(ctx context.Context, message Message) (Message, error)
	Get(ctx context.Context, id int64) (Message, error)
	List(ctx context.Context, sessionID int64) ([]Message, error)
	Delete(ctx context.Context, id int64) error
	DeleteSessionMessages(ctx context.Context, sessionID int64) error
}

type service struct {
	store Store
}

// NewService 创建消息服务。
func NewService(store Store) Service {
	return &service{store: store}
}

func (s *service) Create(ctx context.Context, params CreateParams) (Message, error) {
	message := &Message{
		SessionID: params.SessionID,
		Role:      params.Role,
		Kind:      params.Kind,
		TaskID:    params.TaskID,
		Content:   params.Content,
		Data:      params.Data,
		CreatedAt: time.Now(),
	}
	if err := s.store.Create(ctx, message); err != nil {
		return Message{}, err
	}
	return *message, nil
}

func (s *service) Update(ctx context.Context, message Message) (Message, error) {
	if err := s.store.Update(ctx, &message); err != nil {
		return Message{}, err
	}
	return message, nil
}

func (s *service) Get(ctx context.Context, id int64) (Message, error) {
	message, err := s.store.Get(ctx, id)
	if err != nil {
		return Message{}, err
	}
	return *message, nil
}

func (s *service) List(ctx context.Context, sessionID int64) ([]Message, error) {
	rows, err := s.store.List(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]Message, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row)
	}
	return out, nil
}

func (s *service) Delete(ctx context.Context, id int64) error {
	return s.store.Delete(ctx, id)
}

func (s *service) DeleteSessionMessages(ctx context.Context, sessionID int64) error {
	return s.store.DeleteBySession(ctx, sessionID)
}
