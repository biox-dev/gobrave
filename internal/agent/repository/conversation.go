package repository

import (
	"context"
	"errors"
	"time"

	"github.com/biox-dev/gobrave/internal/agent"
	"gorm.io/gorm"
)

// NewGormConversationRepository 创建基于数据库的会话 Repository。
//
// 会话与其消息（agent_conversation_messages 表）的关系由本实现自行维护，
// 不使用 GORM 的 HasMany 关联。
func NewGormConversationRepository(db *gorm.DB) agent.ConversationRepository {
	return &gormConversationRepository{db: db}
}

type gormConversationRepository struct {
	db *gorm.DB
}

func (r *gormConversationRepository) Get(ctx context.Context, id int64) (*agent.Conversation, error) {
	var conv agent.Conversation
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&conv).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agent.ErrConversationNotFound
		}
		return nil, err
	}
	msgs, err := r.listMessages(ctx, id)
	if err != nil {
		return nil, err
	}
	conv.Messages = msgs
	return &conv, nil
}

func (r *gormConversationRepository) Create(ctx context.Context, conv *agent.Conversation) error {
	if conv == nil {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(conv).Error; err != nil {
			return err
		}
		return replaceMessages(tx, conv)
	})
}

func (r *gormConversationRepository) Update(ctx context.Context, conv *agent.Conversation) error {
	if conv == nil {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(conv).Error; err != nil {
			return err
		}
		return replaceMessages(tx, conv)
	})
}

func (r *gormConversationRepository) Page(ctx context.Context, userID int64, offset, limit int) ([]*agent.Conversation, int64, error) {
	q := r.db.WithContext(ctx).Model(&agent.Conversation{})
	if userID != 0 {
		q = q.Where("user_id = ?", userID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var convs []*agent.Conversation
	if err := q.Order("updated_at DESC").Offset(offset).Limit(limit).Find(&convs).Error; err != nil {
		return nil, 0, err
	}
	for _, c := range convs {
		msgs, err := r.listMessages(ctx, c.ID)
		if err != nil {
			return nil, 0, err
		}
		c.Messages = msgs
	}
	return convs, total, nil
}

// listMessages 按顺序加载会话的消息。
func (r *gormConversationRepository) listMessages(ctx context.Context, conversationID int64) ([]agent.Message, error) {
	var rows []*agent.ConversationMessage
	if err := r.db.WithContext(ctx).
		Where("conversation_id = ?", conversationID).
		Order("seq ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	msgs := make([]agent.Message, 0, len(rows))
	for _, row := range rows {
		msgs = append(msgs, agent.Message{
			Role:    row.Role,
			Kind:    row.Kind,
			TaskID:  row.TaskID,
			Content: row.Content,
			Data:    row.Data,
		})
	}
	return msgs, nil
}

// replaceMessages 重建会话的消息：先删除旧消息，再按当前顺序重新写入。
//
// 由于 ConversationService 每次 Update 都携带完整消息历史，这里采用「先删后插」的
// 幂等策略，避免区分增量 / 全量带来的状态不一致。
func replaceMessages(tx *gorm.DB, conv *agent.Conversation) error {
	if err := tx.Where("conversation_id = ?", conv.ID).Delete(&agent.ConversationMessage{}).Error; err != nil {
		return err
	}
	now := time.Now()
	for i, m := range conv.Messages {
		row := &agent.ConversationMessage{
			ConversationID: conv.ID,
			Seq:            i,
			Role:           m.Role,
			Kind:           m.Kind,
			TaskID:         m.TaskID,
			Content:        m.Content,
			Data:           m.Data,
			CreatedAt:      now,
		}
		if err := tx.Create(row).Error; err != nil {
			return err
		}
	}
	return nil
}
