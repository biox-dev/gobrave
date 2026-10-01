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

// aiSummaryPageColumns 是分页列表需要查询的列：显式排除 content（longtext），
// 避免列表接口把大字段读入内存并返回给前端。
var aiSummaryPageColumns = []string{
	"id", "owner_id", "owner_type", "project_id",
	"title", "status", "profile", "task_id", "created_at", "updated_at",
}

func (r *aiSummaryRepository) PageAISummariesByProjectID(ctx context.Context, pagination *types.Pagination, projectID int64) ([]*types.AISummary, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	buildQuery := func() *gorm.DB {
		return r.db.WithContext(ctx).
			Model(&types.AISummary{}).
			Where("project_id = ?", projectID)
	}

	var total int64
	if err := buildQuery().Count(&total).Error; err != nil {
		return nil, 0, err
	}

	items := make([]*types.AISummary, 0)
	if err := buildQuery().
		Select(aiSummaryPageColumns).
		Order("created_at DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}

	return items, total, nil
}

func (r *aiSummaryRepository) UpdateAISummary(ctx context.Context, item *types.AISummary) error {
	if item == nil || item.ID == 0 {
		return gorm.ErrRecordNotFound
	}

	// 用 map 整体替换：零值（如清空 content / profile）也需要写库。
	updates := map[string]any{
		"owner_id":   item.OwnerID,
		"owner_type": item.OwnerType,
		"project_id": item.ProjectID,
		"title":      item.Title,
		"content":    item.Content,
		"status":     item.Status,
		"profile":    item.Profile,
		"task_id":    item.TaskID,
	}
	return r.db.WithContext(ctx).Model(&types.AISummary{}).Where("id = ?", item.ID).Updates(updates).Error
}

func (r *aiSummaryRepository) DeleteAISummary(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.AISummary{}).Error
}
