package types

import (
	"errors"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// ErrMessageNotFound 表示消息不存在。
var ErrMessageNotFound = errors.New("message: not found")

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
	// KindError 是一条可回放的错误消息（任务执行失败时由 ChatService 写入），
	// 前端据此用错误样式稳定展示，而不是只依赖临时流式事件。
	KindError = "error"
)

// MessageKind 常量是 Kind* 的兼容别名（旧 agent 包对外的命名）。
const (
	MessageKindUser           = KindUser
	MessageKindAssistant      = KindAssistant
	MessageKindAssistantFinal = KindAssistantFinal
	MessageKindReasoning      = KindReasoning
	MessageKindToolCall       = KindToolCall
	MessageKindToolResult     = KindToolResult
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

// MessageCreateParams 是写入一条消息的入参。
type MessageCreateParams struct {
	SessionID int64
	Role      string
	Kind      string
	TaskID    int64
	Content   string
	Data      any
}
