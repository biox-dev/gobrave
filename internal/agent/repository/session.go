package repository

import (
	"context"
	"errors"

	"github.com/biox-dev/gobrave/internal/agent/history"
	"github.com/biox-dev/gobrave/internal/agent/message"
	"github.com/biox-dev/gobrave/internal/agent/session"
	"gorm.io/gorm"
)

// NewGormSessionStore 创建基于数据库的会话 Store。
func NewGormSessionStore(db *gorm.DB) session.Store {
	return &gormSessionStore{db: db}
}

type gormSessionStore struct {
	db *gorm.DB
}

func (r *gormSessionStore) Create(ctx context.Context, s *session.Session) error {
	return r.db.WithContext(ctx).Create(s).Error
}

func (r *gormSessionStore) Get(ctx context.Context, id int64) (*session.Session, error) {
	var s session.Session
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&s).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, session.ErrNotFound
		}
		return nil, err
	}
	return &s, nil
}

func (r *gormSessionStore) Update(ctx context.Context, s *session.Session) error {
	return r.db.WithContext(ctx).Save(s).Error
}

// Delete 删除会话，并级联清理其消息与文件历史。
func (r *gormSessionStore) Delete(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("session_id = ?", id).Delete(&message.Message{}).Error; err != nil {
			return err
		}
		if err := tx.Where("session_id = ?", id).Delete(&history.File{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&session.Session{}).Error
	})
}

func (r *gormSessionStore) List(ctx context.Context, params session.ListParams) ([]*session.Session, int64, error) {
	q := r.db.WithContext(ctx).Model(&session.Session{})
	if params.UserID != 0 {
		q = q.Where("user_id = ?", params.UserID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []*session.Session
	if err := q.Order("updated_at DESC").Offset(params.Offset).Limit(params.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
