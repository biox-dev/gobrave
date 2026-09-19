package repository

import (
	"context"
	"errors"
	"time"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
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

// ===== 容器模板：共享运行配置 =====

func (r *containerRepository) CreateContainerTemplateSpec(ctx context.Context, item *types.ContainerTemplateSpec) error {
	return r.db.WithContext(ctx).Create(item).Error
}

func (r *containerRepository) GetContainerTemplateSpecByID(ctx context.Context, id int64) (*types.ContainerTemplateSpec, error) {
	item := &types.ContainerTemplateSpec{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *containerRepository) UpdateContainerTemplateSpec(ctx context.Context, item *types.ContainerTemplateSpec) error {
	return r.db.WithContext(ctx).Model(&types.ContainerTemplateSpec{}).Where("id = ?", item.ID).Updates(item).Error
}

func (r *containerRepository) DeleteContainerTemplateSpec(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.ContainerTemplateSpec{}).Error
}

func (r *containerRepository) ListContainerTemplateSpec(ctx context.Context) ([]*types.ContainerTemplateSpec, error) {
	items := make([]*types.ContainerTemplateSpec, 0)
	if err := r.db.WithContext(ctx).Order("id DESC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (r *containerRepository) PageContainerTemplateSpec(ctx context.Context, pagination *types.Pagination) ([]*types.ContainerTemplateSpec, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	var total int64
	if err := r.db.WithContext(ctx).Model(&types.ContainerTemplateSpec{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	items := make([]*types.ContainerTemplateSpec, 0)
	if err := r.db.WithContext(ctx).
		Order("id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}

	return items, total, nil
}

// ===== 容器模板：运行配置 × 镜像 绑定行 =====

func (r *containerRepository) CreateContainerTemplateDefinition(ctx context.Context, item *types.ContainerTemplateDefinition) error {
	return r.db.WithContext(ctx).Create(item).Error
}

func (r *containerRepository) GetContainerTemplateDefinitionByID(ctx context.Context, id int64) (*types.ContainerTemplateDefinition, error) {
	item := &types.ContainerTemplateDefinition{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *containerRepository) UpdateContainerTemplateDefinition(ctx context.Context, item *types.ContainerTemplateDefinition) error {
	return r.db.WithContext(ctx).Model(&types.ContainerTemplateDefinition{}).Where("id = ?", item.ID).Updates(item).Error
}

func (r *containerRepository) DeleteContainerTemplateDefinition(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.ContainerTemplateDefinition{}).Error
}

func (r *containerRepository) ListContainerTemplateDefinitionBySpecID(ctx context.Context, specID int64) ([]*types.ContainerTemplateDefinition, error) {
	items := make([]*types.ContainerTemplateDefinition, 0)
	if err := r.db.WithContext(ctx).Where("spec_id = ?", specID).Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (r *containerRepository) ListContainerTemplateDefinitionByImageIDs(ctx context.Context, imageIDs []int64) ([]*types.ContainerTemplateDefinition, error) {
	items := make([]*types.ContainerTemplateDefinition, 0)
	if len(imageIDs) == 0 {
		return items, nil
	}
	if err := r.db.WithContext(ctx).Where("image_id IN ?", imageIDs).Order("id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// ===== 容器模板：对外读模型组装 =====

// assembleContainerTemplates 批量补齐 spec 与 image 后组装读模型。
// 不用 JOIN 的原因：三张表的列名有重叠，别名写错容易在不同数据库上踩坑；
// 这里用两次 IN 查询批量补齐，既避免 N+1，也不需要额外维护一个投影结构体。
func (r *containerRepository) assembleContainerTemplates(ctx context.Context, definitions []*types.ContainerTemplateDefinition) ([]*types.ContainerTemplate, error) {
	if len(definitions) == 0 {
		return []*types.ContainerTemplate{}, nil
	}

	specIDs := make([]int64, 0, len(definitions))
	imageIDs := make([]int64, 0, len(definitions))
	for _, definition := range definitions {
		specIDs = append(specIDs, definition.SpecID)
		imageIDs = append(imageIDs, definition.ImageID)
	}

	specs := make(map[int64]*types.ContainerTemplateSpec, len(specIDs))
	specItems := make([]*types.ContainerTemplateSpec, 0, len(specIDs))
	if err := r.db.WithContext(ctx).Where("id IN ?", specIDs).Find(&specItems).Error; err != nil {
		return nil, err
	}
	for _, spec := range specItems {
		specs[spec.ID] = spec
	}

	images := make(map[int64]*types.ContainerImage, len(imageIDs))
	imageItems := make([]*types.ContainerImage, 0, len(imageIDs))
	if err := r.db.WithContext(ctx).Where("id IN ?", imageIDs).Find(&imageItems).Error; err != nil {
		return nil, err
	}
	for _, image := range imageItems {
		images[image.ID] = image
	}

	items := make([]*types.ContainerTemplate, 0, len(definitions))
	for _, definition := range definitions {
		items = append(items, types.NewContainerTemplate(specs[definition.SpecID], definition, images[definition.ImageID]))
	}
	return items, nil
}

// GetContainerTemplateByID 返回对外容器模板：ID 为绑定行主键，spec/image 缺失时对应字段为 nil。
func (r *containerRepository) GetContainerTemplateByID(ctx context.Context, id int64) (*types.ContainerTemplate, error) {
	definition, err := r.GetContainerTemplateDefinitionByID(ctx, id)
	if err != nil {
		return nil, err
	}

	spec, err := r.GetContainerTemplateSpecByID(ctx, definition.SpecID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	image, err := r.GetContainerImageByID(ctx, definition.ImageID)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	return types.NewContainerTemplate(spec, definition, image), nil
}

func (r *containerRepository) ListContainerTemplate(ctx context.Context) ([]*types.ContainerTemplate, error) {
	definitions := make([]*types.ContainerTemplateDefinition, 0)
	if err := r.db.WithContext(ctx).Order("id DESC").Find(&definitions).Error; err != nil {
		return nil, err
	}
	return r.assembleContainerTemplates(ctx, definitions)
}

func (r *containerRepository) PageContainerTemplate(ctx context.Context, pagination *types.Pagination) ([]*types.ContainerTemplate, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	var total int64
	// 分页主体就是绑定行表，count 不需要任何 join。
	if err := r.db.WithContext(ctx).Model(&types.ContainerTemplateDefinition{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}

	definitions := make([]*types.ContainerTemplateDefinition, 0)
	if err := r.db.WithContext(ctx).
		Order("id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&definitions).Error; err != nil {
		return nil, 0, err
	}

	items, err := r.assembleContainerTemplates(ctx, definitions)
	if err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// ListContainerTemplateBySpecID 返回同一套共享配置下绑定不同镜像的所有模板。
func (r *containerRepository) ListContainerTemplateBySpecID(ctx context.Context, specID int64) ([]*types.ContainerTemplate, error) {
	definitions, err := r.ListContainerTemplateDefinitionBySpecID(ctx, specID)
	if err != nil {
		return nil, err
	}
	return r.assembleContainerTemplates(ctx, definitions)
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

func (r *containerRepository) CountPendingOutboxEvents(ctx context.Context, eventTypes ...string) (int64, error) {
	var count int64

	query := r.db.WithContext(ctx).
		Model(&types.OutboxEvent{}).
		Where("status = ?", "pending")

	if len(eventTypes) == 1 {
		query = query.Where("type = ?", eventTypes[0])
	} else if len(eventTypes) > 1 {
		query = query.Where("type IN ?", eventTypes)
	}

	err := query.Count(&count).Error
	return count, err
}

func (r *containerRepository) CountPendingOutboxEventsByType(ctx context.Context, eventType string) (int64, error) {
	return r.CountPendingOutboxEvents(ctx, eventType)
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
func (r *containerRepository) MarkOutboxEventFailed(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).
		Model(&types.OutboxEvent{}).
		Where("id = ?", id).
		Update("status", "failed").Error
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
