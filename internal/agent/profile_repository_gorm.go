package agent

import (
	"context"
	"errors"
	"strings"

	"gorm.io/gorm"
)

// NewGormProfileRepository 创建基于数据库的用户自定义 Profile Repository。
func NewGormProfileRepository(db *gorm.DB) ProfileRepository {
	return &gormProfileRepository{db: db}
}

type gormProfileRepository struct {
	db *gorm.DB
}

func (r *gormProfileRepository) Create(ctx context.Context, p *Profile) error {
	if p == nil {
		return nil
	}
	return r.db.WithContext(ctx).Create(p).Error
}

func (r *gormProfileRepository) Get(ctx context.Context, id string) (*Profile, error) {
	var p Profile
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrProfileNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (r *gormProfileRepository) Update(ctx context.Context, p *Profile) error {
	if p == nil {
		return nil
	}
	return r.db.WithContext(ctx).Save(p).Error
}

func (r *gormProfileRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&Profile{}).Error
}

func (r *gormProfileRepository) ListByUser(ctx context.Context, userID string) ([]*Profile, error) {
	var profiles []*Profile
	if err := r.db.WithContext(ctx).
		Where("user_id = ?", strings.TrimSpace(userID)).
		Order("name ASC").
		Find(&profiles).Error; err != nil {
		return nil, err
	}
	return profiles, nil
}

func (r *gormProfileRepository) GetByName(ctx context.Context, userID, name string) (*Profile, error) {
	var p Profile
	if err := r.db.WithContext(ctx).
		Where("user_id = ? AND name = ?", strings.TrimSpace(userID), name).
		First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrProfileNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (r *gormProfileRepository) GetDefault(ctx context.Context, userID string) (*Profile, error) {
	var p Profile
	if err := r.db.WithContext(ctx).
		Where("user_id = ? AND is_default = ?", strings.TrimSpace(userID), true).
		First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &p, nil
}

func (r *gormProfileRepository) ClearDefault(ctx context.Context, userID, exceptID string) error {
	return r.db.WithContext(ctx).Model(&Profile{}).
		Where("user_id = ? AND is_default = ? AND id <> ?", strings.TrimSpace(userID), true, exceptID).
		Update("is_default", false).Error
}
