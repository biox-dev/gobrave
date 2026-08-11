package repository

import (
	"context"
	"time"

	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

type containerRepository struct {
	db *gorm.DB
}

func NewContainerRepository(db *gorm.DB) interfaces.ContainerRepository {
	return &containerRepository{db: db}
}

func (r *containerRepository) WithTransaction(ctx context.Context, fn func(interfaces.ContainerRepository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := &containerRepository{db: tx}
		return fn(txRepo)
	})
}

func (r *containerRepository) CreateContainerImage(ctx context.Context, item *types.ContainerImage) error {
	return r.db.WithContext(ctx).Create(item).Error
}

func (r *containerRepository) GetContainerImageByID(ctx context.Context, id int64) (*types.ContainerImage, error) {
	item := &types.ContainerImage{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *containerRepository) UpdateContainerImage(ctx context.Context, item *types.ContainerImage) error {
	return r.db.WithContext(ctx).Model(&types.ContainerImage{}).Where("id = ?", item.ID).Updates(item).Error
}

func (r *containerRepository) DeleteContainerImage(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.ContainerImage{}).Error
}

func (r *containerRepository) ListContainerImage(ctx context.Context) ([]*types.ContainerImage, error) {
	items := make([]*types.ContainerImage, 0)
	err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *containerRepository) PageContainerImage(ctx context.Context, pagination *types.Pagination) ([]*types.ContainerImage, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.ContainerImage, 0)
	var total int64

	if err := r.db.WithContext(ctx).Model(&types.ContainerImage{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := r.db.WithContext(ctx).
		Order("id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	if len(items) == 0 {
		return []*types.ContainerImage{}, total, nil
	}

	return items, total, nil
}

func (r *containerRepository) CreateContainerTemplate(ctx context.Context, item *types.ContainerTemplate) error {
	return r.db.WithContext(ctx).Create(item).Error
}

func (r *containerRepository) GetContainerTemplateByID(ctx context.Context, id int64) (*types.ContainerTemplate, error) {
	item := &types.ContainerTemplate{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *containerRepository) UpdateContainerTemplate(ctx context.Context, item *types.ContainerTemplate) error {
	return r.db.WithContext(ctx).Model(&types.ContainerTemplate{}).Where("id = ?", item.ID).Updates(item).Error
}

func (r *containerRepository) DeleteContainerTemplate(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.ContainerTemplate{}).Error
}

func (r *containerRepository) ListContainerTemplate(ctx context.Context) ([]*types.ContainerTemplate, error) {
	items := make([]*types.ContainerTemplate, 0)
	err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *containerRepository) PageContainerTemplate(ctx context.Context, pagination *types.Pagination) ([]*types.ContainerTemplate, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.ContainerTemplate, 0)
	var total int64

	if err := r.db.WithContext(ctx).Model(&types.ContainerTemplate{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := r.db.WithContext(ctx).
		Order("id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	if len(items) == 0 {
		return []*types.ContainerTemplate{}, total, nil
	}

	return items, total, nil
}

func (r *containerRepository) CreateAppSession(ctx context.Context, item *types.AppSession) error {
	return r.db.WithContext(ctx).Create(item).Error
}

func (r *containerRepository) GetAppSessionByID(ctx context.Context, id int64) (*types.AppSession, error) {
	item := &types.AppSession{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}
func (r *containerRepository) GetProjectByID(ctx context.Context, projectID string) (*types.Project, error) {
	item := &types.Project{}
	if err := r.db.WithContext(ctx).Where("project_id = ?", projectID).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *containerRepository) GetProjectByProjectID(ctx context.Context, projectID string) (*types.Project, error) {
	item := &types.Project{}
	if err := r.db.WithContext(ctx).Where("project_id = ?", projectID).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *containerRepository) UpdateAppSession(ctx context.Context, item *types.AppSession) error {
	return r.db.WithContext(ctx).Model(&types.AppSession{}).Where("id = ?", item.ID).Updates(item).Error
}

func (r *containerRepository) DeleteAppSession(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.AppSession{}).Error
}

func (r *containerRepository) ListAppSession(ctx context.Context) ([]*types.AppSession, error) {
	items := make([]*types.AppSession, 0)
	err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *containerRepository) PageAppSessionByUserID(ctx context.Context, userID string, pagination *types.Pagination, query *types.AppSessionPageQuery) ([]*types.AppSession, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.AppSession, 0)
	var total int64

	dbQuery := r.db.WithContext(ctx).Model(&types.AppSession{}).Where("user_id = ?", userID)
	if query != nil && query.AnalysisNodeID != nil {
		dbQuery = dbQuery.Where("analysis_node_id = ?", *query.AnalysisNodeID)
	}
	if query != nil && query.ProjectID != nil {
		dbQuery = dbQuery.Where("project_id = ?", *query.ProjectID)
	}
	if err := dbQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := dbQuery.
		Order("id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	if len(items) == 0 {
		return []*types.AppSession{}, total, nil
	}

	return items, total, nil
}

func (r *containerRepository) CreateContainerInstance(ctx context.Context, item *types.ContainerInstance) error {
	return r.db.WithContext(ctx).Create(item).Error
}

func (r *containerRepository) GetContainerInstanceByID(ctx context.Context, id int64) (*types.ContainerInstance, error) {
	item := &types.ContainerInstance{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *containerRepository) GetContainerInstanceByRuntimeID(ctx context.Context, runtimeID string) (*types.ContainerInstance, error) {
	item := &types.ContainerInstance{}
	if err := r.db.WithContext(ctx).Where("runtime_id = ?", runtimeID).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *containerRepository) GetContainerInstanceByOwner(ctx context.Context, ownerType types.ContainerOwnerType, ownerID int64) (*types.ContainerInstance, error) {
	item := &types.ContainerInstance{}
	if err := r.db.WithContext(ctx).Where("owner_type = ? AND owner_id = ?", ownerType, ownerID).Order("id DESC").Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *containerRepository) UpdateContainerInstance(ctx context.Context, item *types.ContainerInstance) error {
	return r.db.WithContext(ctx).Model(&types.ContainerInstance{}).Where("id = ?", item.ID).Updates(item).Error
}

func (r *containerRepository) DeleteContainerInstance(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.ContainerInstance{}).Error
}

func (r *containerRepository) ListContainerInstance(ctx context.Context) ([]*types.ContainerInstance, error) {
	items := make([]*types.ContainerInstance, 0)
	err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *containerRepository) CountContainerInstanceByStatuses(ctx context.Context, statuses []types.ContainerStatus) (int64, error) {
	if len(statuses) == 0 {
		return 0, nil
	}

	var total int64
	err := r.db.WithContext(ctx).
		Model(&types.ContainerInstance{}).
		Where("status IN ?", statuses).
		Count(&total).Error
	if err != nil {
		return 0, err
	}

	return total, nil
}

func (r *containerRepository) ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx context.Context, ownerType types.ContainerOwnerType, ownerIDs []int64) ([]*types.ContainerInstance, error) {
	if len(ownerIDs) == 0 {
		return []*types.ContainerInstance{}, nil
	}

	items := make([]*types.ContainerInstance, 0)
	err := r.db.WithContext(ctx).
		Where("owner_type = ? AND owner_id IN ?", ownerType, ownerIDs).
		Order("id DESC").
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *containerRepository) PageContainerInstance(ctx context.Context, pagination *types.Pagination) ([]*types.ContainerInstance, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.ContainerInstance, 0)
	var total int64

	if err := r.db.WithContext(ctx).Model(&types.ContainerInstance{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := r.db.WithContext(ctx).
		Order("id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	if len(items) == 0 {
		return []*types.ContainerInstance{}, total, nil
	}

	return items, total, nil
}

func (r *containerRepository) CreateContainerEvent(ctx context.Context, item *types.ContainerEvent) error {
	return r.db.WithContext(ctx).Create(item).Error
}

func (r *containerRepository) GetContainerEventByID(ctx context.Context, id int64) (*types.ContainerEvent, error) {
	item := &types.ContainerEvent{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *containerRepository) UpdateContainerEvent(ctx context.Context, item *types.ContainerEvent) error {
	return r.db.WithContext(ctx).Model(&types.ContainerEvent{}).Where("id = ?", item.ID).Updates(item).Error
}

func (r *containerRepository) DeleteContainerEvent(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.ContainerEvent{}).Error
}

func (r *containerRepository) ListContainerEvent(ctx context.Context) ([]*types.ContainerEvent, error) {
	items := make([]*types.ContainerEvent, 0)
	err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *containerRepository) PageContainerEvent(ctx context.Context, pagination *types.Pagination) ([]*types.ContainerEvent, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.ContainerEvent, 0)
	var total int64

	if err := r.db.WithContext(ctx).Model(&types.ContainerEvent{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := r.db.WithContext(ctx).
		Order("id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	if len(items) == 0 {
		return []*types.ContainerEvent{}, total, nil
	}

	return items, total, nil
}

func (r *containerRepository) CreateOutboxEvent(ctx context.Context, item *types.OutboxEvent) error {
	return r.db.WithContext(ctx).Create(item).Error
}

func (r *containerRepository) ListPendingOutboxEvent(ctx context.Context, limit int) ([]*types.OutboxEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	items := make([]*types.OutboxEvent, 0)
	err := r.db.WithContext(ctx).
		Where("status = ?", "pending").
		Order("id ASC").
		Limit(limit).
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *containerRepository) ListPendingOutboxEventsByType(ctx context.Context, eventType string, limit int) ([]*types.OutboxEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	items := make([]*types.OutboxEvent, 0)
	err := r.db.WithContext(ctx).
		Where("status = ? AND type = ?", "pending", eventType).
		Order("id ASC").
		Limit(limit).
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *containerRepository) CountPendingOutboxEventsByType(ctx context.Context, eventType string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).
		Model(&types.OutboxEvent{}).
		Where("status = ? AND type = ?", "pending", eventType).
		Count(&count).Error
	return count, err
}

func (r *containerRepository) MarkOutboxEventProcessing(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).
		Model(&types.OutboxEvent{}).
		Where("id = ?", id).
		Update("status", "processing").Error
}

func (r *containerRepository) MarkOutboxEventPending(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).
		Model(&types.OutboxEvent{}).
		Where("id = ?", id).
		Update("status", "pending").Error
}

func (r *containerRepository) MarkOutboxEventSent(ctx context.Context, id int64) error {
	now := time.Now()
	return r.db.WithContext(ctx).
		Model(&types.OutboxEvent{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"status":  "sent",
			"sent_at": &now,
		}).Error
}

func (r *containerRepository) PageOutboxEvent(ctx context.Context, pagination *types.Pagination) ([]*types.OutboxEvent, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.OutboxEvent, 0)
	var total int64

	if err := r.db.WithContext(ctx).Model(&types.OutboxEvent{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := r.db.WithContext(ctx).
		Order("id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	if len(items) == 0 {
		return []*types.OutboxEvent{}, total, nil
	}

	return items, total, nil
}
