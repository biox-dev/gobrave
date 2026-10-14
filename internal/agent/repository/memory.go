package repository

import (
	"context"
	"errors"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent"
	"gorm.io/gorm"
)

// NewGormMemoryRepository 创建基于数据库的记忆 Repository。
func NewGormMemoryRepository(db *gorm.DB) agent.MemoryRepository {
	return &gormMemoryRepository{db: db}
}

type gormMemoryRepository struct {
	db *gorm.DB
}

func (r *gormMemoryRepository) Create(ctx context.Context, m *agent.Memory) error {
	if m == nil {
		return nil
	}
	return r.db.WithContext(ctx).Create(m).Error
}

func (r *gormMemoryRepository) Get(ctx context.Context, id string) (*agent.Memory, error) {
	var m agent.Memory
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agent.ErrMemoryNotFound
		}
		return nil, err
	}
	return &m, nil
}

func (r *gormMemoryRepository) Update(ctx context.Context, m *agent.Memory) error {
	if m == nil {
		return nil
	}
	return r.db.WithContext(ctx).Save(m).Error
}

func (r *gormMemoryRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&agent.Memory{}).Error
}

func (r *gormMemoryRepository) ListByUser(ctx context.Context, userID int64, offset, limit int, kinds ...agent.MemoryKind) ([]*agent.Memory, int64, error) {
	q := r.db.WithContext(ctx).Model(&agent.Memory{}).Where("user_id = ?", userID)
	if len(kinds) > 0 {
		q = q.Where("kind IN ?", kinds)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var mems []*agent.Memory
	if err := q.Order("updated_at DESC").Offset(offset).Limit(limit).Find(&mems).Error; err != nil {
		return nil, 0, err
	}
	return mems, total, nil
}

func (r *gormMemoryRepository) Search(ctx context.Context, userID int64, query string, limit int) ([]*agent.Memory, error) {
	q := r.db.WithContext(ctx).Model(&agent.Memory{}).Where("user_id = ?", userID)
	if strings.TrimSpace(query) != "" {
		q = q.Where("content LIKE ?", "%"+query+"%")
	}

	var mems []*agent.Memory
	if err := q.Order("importance DESC").Order("updated_at DESC").Limit(limit).Find(&mems).Error; err != nil {
		return nil, err
	}
	return mems, nil
}
