package repository

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

type aiSummaryRepository struct {
	db *gorm.DB
}

func NewAISummaryRepository(db *gorm.DB) interfaces.AISummaryRepository {
	return &aiSummaryRepository{db: db}
}

func (r *aiSummaryRepository) CreateAISummary(ctx context.Context, item *types.AISummary) error {
	return r.db.WithContext(ctx).Create(item).Error
}

func (r *aiSummaryRepository) GetAISummaryByID(ctx context.Context, id int64) (*types.AISummary, error) {
	item := &types.AISummary{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *aiSummaryRepository) ListAISummariesByOwner(ctx context.Context, ownerType types.SummaryOwnerType, ownerID int64) ([]*types.AISummary, error) {
	items := make([]*types.AISummary, 0)
	if err := r.db.WithContext(ctx).
		Where("owner_type = ? AND owner_id = ?", ownerType, ownerID).
		Order("created_at ASC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (r *aiSummaryRepository) UpdateAISummary(ctx context.Context, item *types.AISummary) error {
	return r.db.WithContext(ctx).Model(&types.AISummary{}).Where("id = ?", item.ID).Updates(item).Error
}

func (r *aiSummaryRepository) DeleteAISummary(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.AISummary{}).Error
}
