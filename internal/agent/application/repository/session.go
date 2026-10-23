package repository

import (
	"context"
	"errors"

	agent "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"gorm.io/gorm"
)

// NewGormSessionStore 创建基于数据库的会话 Store。
func NewGormSessionStore(db *gorm.DB) agentinterfaces.SessionStore {
	return &gormSessionStore{db: db}
}

type gormSessionStore struct {
	db *gorm.DB
}

func (r *gormSessionStore) Create(ctx context.Context, s *agent.Session) error {
	return r.db.WithContext(ctx).Create(s).Error
}

func (r *gormSessionStore) Get(ctx context.Context, id int64) (*agent.Session, error) {
	var s agent.Session
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&s).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agent.ErrSessionNotFound
		}
		return nil, err
	}
	return &s, nil
}

func (r *gormSessionStore) Update(ctx context.Context, s *agent.Session) error {
	return r.db.WithContext(ctx).Save(s).Error
}

// Delete 删除会话，并级联清理其消息与文件历史。
func (r *gormSessionStore) Delete(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("session_id = ?", id).Delete(&agent.Message{}).Error; err != nil {
			return err
		}
		if err := tx.Where("session_id = ?", id).Delete(&agent.File{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&agent.Session{}).Error
	})
}

func (r *gormSessionStore) List(ctx context.Context, params agent.SessionListParams) ([]*agent.Session, int64, error) {
	// 会话列表只展示顶层会话：子 Agent / 标题生成等子会话通过 parent_session_id 关联，
	// 不属于会话列表（需要时按父会话单独查询）。
	q := r.db.WithContext(ctx).Model(&agent.Session{}).Where("parent_session_id = 0")
	if params.UserID != 0 {
		q = q.Where("user_id = ?", params.UserID)
	}
	// ProjectID 用于会话按项目隔离：0 表示不按项目过滤。
	if params.ProjectID != 0 {
		q = q.Where("project_id = ?", params.ProjectID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []*agent.Session
	if err := q.Order("updated_at DESC").Offset(params.Offset).Limit(params.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
