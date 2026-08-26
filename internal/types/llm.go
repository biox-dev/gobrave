package types

import (
	"time"

	"github.com/gobravedev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// LLMSession stores a chat session and owner scope.
// Relation with conversation is maintained manually via LLMConversation.LLMSessionID.
type LLMSession struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	ProjectID int64 `json:"project_id" gorm:"type:bigint;index;not null"`

	Title string `json:"title" gorm:"type:varchar(255)"`

	Status string `json:"status" gorm:"type:varchar(32);index;not null;default:ACTIVE"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (LLMSession) TableName() string {
	return "go_llm_session"
}

func (t *LLMSession) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}

// LLMConversation stores a single conversation entry under a session.
// We intentionally use LLMSessionID instead of GORM associations.
type LLMConversation struct {
	ID int64 `json:"id,string" gorm:"primaryKey;type:bigint;autoIncrement:false"`

	ConversationID string `json:"conversation_id" gorm:"type:varchar(64);uniqueIndex;not null"`

	LLMSessionID int64 `json:"llm_session_id,string" gorm:"index;not null"`

	Role string `json:"role" gorm:"type:varchar(32);index;not null"`

	Content string `json:"content" gorm:"type:longtext"`

	Model string `json:"model" gorm:"type:varchar(255)"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (LLMConversation) TableName() string {
	return "go_llm_conversation"
}

func (t *LLMConversation) BeforeCreate(_ *gorm.DB) error {
	if t.ID == 0 {
		t.ID = utils.GenerateID()
	}
	return nil
}
