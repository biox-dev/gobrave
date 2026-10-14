package repository

import (
	"context"
	"errors"

	"github.com/biox-dev/gobrave/internal/agent"
	"gorm.io/gorm"
)

// NewGormProfileRepository 创建基于数据库的 Profile Repository。
func NewGormProfileRepository(db *gorm.DB) agent.ProfileRepository {
	return &gormProfileRepository{db: db}
}

type gormProfileRepository struct {
	db *gorm.DB
}

func (r *gormProfileRepository) Create(ctx context.Context, p *agent.Profile) error {
	if p == nil {
		return nil
	}
	return r.db.WithContext(ctx).Create(p).Error
}

func (r *gormProfileRepository) Get(ctx context.Context, id int64) (*agent.Profile, error) {
	var p agent.Profile
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agent.ErrProfileNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (r *gormProfileRepository) Update(ctx context.Context, p *agent.Profile) error {
	if p == nil {
		return nil
	}
	return r.db.WithContext(ctx).Save(p).Error
}

func (r *gormProfileRepository) Delete(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&agent.Profile{}).Error
}

func (r *gormProfileRepository) List(ctx context.Context) ([]*agent.Profile, error) {
	var profiles []*agent.Profile
	if err := r.db.WithContext(ctx).
		Order("name ASC").
		Find(&profiles).Error; err != nil {
		return nil, err
	}
	return profiles, nil
}

func (r *gormProfileRepository) ClearDefault(ctx context.Context, exceptID int64) error {
	return r.db.WithContext(ctx).Model(&agent.Profile{}).
		Where("is_default = ? AND id <> ?", true, exceptID).
		Update("is_default", false).Error
}
