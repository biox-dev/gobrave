package repository

import (
	"context"
	"errors"

	agent "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"gorm.io/gorm"
)

// NewGormMessageStore 创建基于数据库的消息 Store。
func NewGormMessageStore(db *gorm.DB) agentinterfaces.MessageStore {
	return &gormMessageStore{db: db}
}

type gormMessageStore struct {
	db *gorm.DB
}

// Create 写入一条消息，Seq 由会话内当前最大序号 +1 自动分配。
func (r *gormMessageStore) Create(ctx context.Context, m *agent.Message) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int
		if err := tx.Model(&agent.Message{}).
			Where("session_id = ?", m.SessionID).
			Select("COALESCE(MAX(seq), -1)").
			Scan(&maxSeq).Error; err != nil {
			return err
		}
		m.Seq = maxSeq + 1
		return tx.Create(m).Error
	})
}

func (r *gormMessageStore) Update(ctx context.Context, m *agent.Message) error {
	return r.db.WithContext(ctx).Save(m).Error
}

func (r *gormMessageStore) Get(ctx context.Context, id int64) (*agent.Message, error) {
	var m agent.Message
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agent.ErrMessageNotFound
		}
		return nil, err
	}
	return &m, nil
}

func (r *gormMessageStore) List(ctx context.Context, sessionID int64) ([]*agent.Message, error) {
	var rows []*agent.Message
	if err := r.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("seq ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

func (r *gormMessageStore) Delete(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&agent.Message{}).Error
}

func (r *gormMessageStore) DeleteBySession(ctx context.Context, sessionID int64) error {
	return r.db.WithContext(ctx).Where("session_id = ?", sessionID).Delete(&agent.Message{}).Error
}
