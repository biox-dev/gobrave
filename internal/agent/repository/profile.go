package repository

import (
	"context"
	"errors"

	"github.com/biox-dev/gobrave/internal/agent"
	"gorm.io/gorm"
)

// NewGormProfileRepository 创建基于数据库的用户自定义 Profile Repository。
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

func (r *gormProfileRepository) ListByUser(ctx context.Context, userID int64) ([]*agent.Profile, error) {
	var profiles []*agent.Profile
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("name ASC").
		Find(&profiles).Error; err != nil {
		return nil, err
	}
	return profiles, nil
}

func (r *gormProfileRepository) ListBuiltin(ctx context.Context) ([]*agent.Profile, error) {
	var profiles []*agent.Profile
	if err := r.db.WithContext(ctx).
		Where("is_builtin = ?", true).
		Order("name ASC").
		Find(&profiles).Error; err != nil {
		return nil, err
	}
	return profiles, nil
}

func (r *gormProfileRepository) GetDefault(ctx context.Context, userID int64) (*agent.Profile, error) {
	var p agent.Profile
	if err := r.db.WithContext(ctx).
		Where("user_id = ? AND is_default = ?", userID, true).
		First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

func (r *gormProfileRepository) ClearDefault(ctx context.Context, userID int64, exceptID int64) error {
	return r.db.WithContext(ctx).Model(&agent.Profile{}).
		Where("user_id = ? AND is_default = ? AND id <> ?", userID, true, exceptID).
		Update("is_default", false).Error
}
