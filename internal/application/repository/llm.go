package repository

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

type llmRepository struct {
	db *gorm.DB
}

func NewLLMRepository(db *gorm.DB) interfaces.LLMRepository {
	return &llmRepository{db: db}
}

func (r *llmRepository) CreateLLMSession(ctx context.Context, session *types.LLMSession) error {
	return r.db.WithContext(ctx).Create(session).Error
}

func (r *llmRepository) GetLLMSessionByID(ctx context.Context, id int64) (*types.LLMSession, error) {
	item := &types.LLMSession{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *llmRepository) GetLLMSessionByIDAndProjectID(ctx context.Context, id int64, projectID int64) (*types.LLMSession, error) {
	item := &types.LLMSession{}
	if err := r.db.WithContext(ctx).Where("id = ? AND project_id = ?", id, projectID).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *llmRepository) UpdateLLMSession(ctx context.Context, session *types.LLMSession) error {
	return r.db.WithContext(ctx).Model(&types.LLMSession{}).
		Where("id = ?", session.ID).
		Updates(map[string]interface{}{
			"project_id": session.ProjectID,
			"title":      session.Title,
			"status":     session.Status,
		}).Error
}

func (r *llmRepository) DeleteLLMSession(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.LLMSession{}).Error
}

func (r *llmRepository) ListLLMSessionByProjectID(ctx context.Context, projectID int64) ([]*types.LLMSession, error) {
	items := make([]*types.LLMSession, 0)
	if err := r.db.WithContext(ctx).Where("project_id = ?", projectID).Order("id DESC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (r *llmRepository) DeleteLLMSessionWithRelations(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("llm_session_id = ?", id).Delete(&types.LLMConversation{}).Error; err != nil {
			return err
		}
		if err := tx.Where("id = ?", id).Delete(&types.LLMSession{}).Error; err != nil {
			return err
		}
		return nil
	})
}

func (r *llmRepository) CreateLLMConversation(ctx context.Context, conversation *types.LLMConversation) error {
	return r.db.WithContext(ctx).Create(conversation).Error
}

func (r *llmRepository) GetLLMConversationByID(ctx context.Context, id int64) (*types.LLMConversation, error) {
	item := &types.LLMConversation{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *llmRepository) GetLLMConversationByIDAndProjectID(ctx context.Context, id int64, projectID int64) (*types.LLMConversation, error) {
	item := &types.LLMConversation{}
	err := r.db.WithContext(ctx).
		Table("go_llm_conversation AS c").
		Select("c.id, c.conversation_id, c.llm_session_id, c.role, c.content, c.model, c.created_at, c.updated_at").
		Joins("JOIN go_llm_session AS s ON s.id = c.llm_session_id").
		Where("c.id = ? AND s.project_id = ?", id, projectID).
		Take(item).Error
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (r *llmRepository) UpdateLLMConversation(ctx context.Context, conversation *types.LLMConversation) error {
	return r.db.WithContext(ctx).Model(&types.LLMConversation{}).
		Where("id = ?", conversation.ID).
		Updates(map[string]interface{}{
			"conversation_id": conversation.ConversationID,
			"llm_session_id":  conversation.LLMSessionID,
			"role":            conversation.Role,
			"content":         conversation.Content,
			"model":           conversation.Model,
		}).Error
}

func (r *llmRepository) DeleteLLMConversation(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.LLMConversation{}).Error
}

func (r *llmRepository) ListLLMConversationBySessionID(ctx context.Context, llmSessionID int64) ([]*types.LLMConversation, error) {
	items := make([]*types.LLMConversation, 0)
	if err := r.db.WithContext(ctx).Where("llm_session_id = ?", llmSessionID).Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}
