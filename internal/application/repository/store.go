package repository

import (
	"context"
	"fmt"

	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

type storeRepository struct {
	db *gorm.DB
}

func NewStoreRepository(db *gorm.DB) interfaces.StoreRepository {
	return &storeRepository{db: db}
}

func (r *storeRepository) CreateStore(ctx context.Context, item *types.Store) error {
	return r.db.WithContext(ctx).Create(item).Error
}

func (r *storeRepository) GetStoreByID(ctx context.Context, id int64) (*types.Store, error) {
	item := &types.Store{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *storeRepository) GetStoreByStoreID(ctx context.Context, storeID string) (*types.Store, error) {
	item := &types.Store{}
	if err := r.db.WithContext(ctx).Where("store_id = ?", storeID).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *storeRepository) GetStoreByURL(ctx context.Context, rawURL string) (*types.Store, error) {
	item := &types.Store{}
	if err := r.db.WithContext(ctx).Where("url = ?", rawURL).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *storeRepository) UpdateStore(ctx context.Context, item *types.Store) error {
	return r.db.WithContext(ctx).Model(&types.Store{}).Where("id = ?", item.ID).Updates(map[string]interface{}{
		// "store_id":     item.StoreID,
		"store_type":   item.StoreType,
		"name":         item.Name,
		"origin":       item.Origin,
		"url":          item.URL,
		"status":       item.Status,
		"path":         item.Path,
		"path_name":    item.PathName,
		"category":     item.Category,
		"tags":         item.Tags,
		"img":          item.Img,
		"publish_urls": item.PublishURLs,
		"log":          item.Log,
		"version":      item.Version,
		"message":      item.Message,
	}).Error
}

func (r *storeRepository) DeleteStore(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&types.Script{}).Where("store_id = ?", id).Update("store_id", 0).Error; err != nil {
			return err
		}

		if err := tx.Model(&types.Workflow{}).Where("store_id = ?", id).Update("store_id", 0).Error; err != nil {
			return err
		}

		return tx.Where("id = ?", id).Delete(&types.Store{}).Error
	})
}

func (r *storeRepository) ListStore(ctx context.Context) ([]*types.Store, error) {
	items := make([]*types.Store, 0)
	err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *storeRepository) PageStore(ctx context.Context, pagination *types.Pagination, query *types.StorePageQuery) ([]*types.Store, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}
	if query == nil {
		return nil, 0, fmt.Errorf("query is required")
	}
	storeType := query.GetStoreType()
	if storeType != "workflow" && storeType != "script" {
		return nil, 0, fmt.Errorf("store_type must be workflow or script")
	}

	items := make([]*types.Store, 0)
	var total int64

	buildQuery := func() *gorm.DB {
		db := r.db.WithContext(ctx).
			Table("store").
			Where("store.store_type = ?", storeType)

		if keywords := query.GetKeywords(); keywords != "" {
			like := "%" + keywords + "%"
			db = db.Where(
				r.db.WithContext(ctx).Where("store.name LIKE ?", like).
					Or("store.category LIKE ?", like).
					Or("store.store_id LIKE ?", like).
					Or("store.url LIKE ?", like),
			)
		}

		return db
	}

	if err := buildQuery().Count(&total).Error; err != nil {
		return nil, 0, err
	}

	sortColumn := query.GetSortColumn()
	sortOrder := query.GetSortOrder()

	err := buildQuery().
		Order(fmt.Sprintf("%s %s", sortColumn, sortOrder)).
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	if len(items) == 0 {
		return []*types.Store{}, total, nil
	}

	return items, total, nil
}

func (r *storeRepository) ListInstalledWorkflowMap(ctx context.Context, activeProjectID int64, storeIDs []int64) (map[int64]uint, error) {
	out := make(map[int64]uint)
	if activeProjectID == 0 || len(storeIDs) == 0 {
		return out, nil
	}

	type workflowInstallRow struct {
		ID      uint  `gorm:"column:id"`
		StoreID int64 `gorm:"column:store_id"`
	}

	rows := make([]workflowInstallRow, 0)
	err := r.db.WithContext(ctx).
		Table("pipeline_components_relation").
		Select("id, store_id").
		Where("project_id = ?", activeProjectID).
		Where("store_id IN ?", storeIDs).
		Order("id DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		if _, exists := out[row.StoreID]; exists {
			continue
		}
		out[row.StoreID] = row.ID
	}

	return out, nil
}

func (r *storeRepository) ListInstalledScriptMap(ctx context.Context, activeProjectID int64, storeIDs []int64) (map[int64]int64, error) {
	out := make(map[int64]int64)
	if activeProjectID == 0 || len(storeIDs) == 0 {
		return out, nil
	}

	type scriptInstallRow struct {
		ID      int64 `gorm:"column:id"`
		StoreID int64 `gorm:"column:store_id"`
	}

	rows := make([]scriptInstallRow, 0)
	err := r.db.WithContext(ctx).
		Table("pipeline_components").
		Select("id, store_id").
		Where("project_id = ?", activeProjectID).
		Where("store_id IN ?", storeIDs).
		Order("id DESC").
		Find(&rows).Error
	if err != nil {
		return nil, err
	}

	for _, row := range rows {
		if _, exists := out[row.StoreID]; exists {
			continue
		}
		out[row.StoreID] = row.ID
	}

	return out, nil
}
