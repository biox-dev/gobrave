package repository

import (
	"context"
	"strings"
	"time"

	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type analysisRepository struct {
	db *gorm.DB
}

func NewAnalysisRepository(db *gorm.DB) interfaces.AnalysisRepository {
	return &analysisRepository{db: db}
}

func (r *analysisRepository) WithTransaction(ctx context.Context, fn func(interfaces.AnalysisRepository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := &analysisRepository{db: tx}
		return fn(txRepo)
	})
}

func (r *analysisRepository) CreateAnalysis(ctx context.Context, item *types.Analysis) error {
	return r.db.WithContext(ctx).Create(item).Error
}

func (r *analysisRepository) TryMarkAnalysisRunning(ctx context.Context, analysisID int64, now time.Time, staleBefore time.Time) (bool, error) {
	result := r.db.WithContext(ctx).
		Model(&types.Analysis{}).
		Where("id = ? AND (job_status IS NULL OR job_status <> ? OR updated_at IS NULL OR updated_at < ?)", analysisID, "running", staleBefore).
		Updates(map[string]any{
			"job_status": "running",
			"updated_at": now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

func (r *analysisRepository) UpdateAnalysisByAnalysisID(ctx context.Context, analysisID string, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&types.Analysis{}).Where("analysis_id = ?", analysisID).Updates(values).Error
}
func (r *analysisRepository) UpdateAnalysisByID(ctx context.Context, analysisID int64, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&types.Analysis{}).Where("id = ?", analysisID).Updates(values).Error
}
func (r *analysisRepository) GetAnalysisByID(ctx context.Context, analysisID int64) (*types.Analysis, error) {
	item := &types.Analysis{}
	if err := r.db.WithContext(ctx).Where("id = ?", analysisID).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}
func (r *analysisRepository) GetAnalysisByAnalysisID(ctx context.Context, analysisID string) (*types.Analysis, error) {
	item := &types.Analysis{}
	if err := r.db.WithContext(ctx).Where("analysis_id = ?", analysisID).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *analysisRepository) ListAnalysisByJobStatus(ctx context.Context, jobStatus string) ([]*types.Analysis, error) {
	items := make([]*types.Analysis, 0)
	err := r.db.WithContext(ctx).
		Where("job_status = ?", jobStatus).
		Order("updated_at ASC").
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *analysisRepository) PageAnalysisByProjectID(ctx context.Context, pagination *types.Pagination, projectID int64, query *types.AnalysisQuey) ([]*types.Analysis, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.Analysis, 0)
	base := r.db.WithContext(ctx).Model(&types.Analysis{}).Where("project_id = ?", projectID)

	applyFilters := func(db *gorm.DB) *gorm.DB {
		if query == nil {
			return db
		}

		if len(query.IDs) > 0 {
			db = db.Where("id IN ?", query.IDs)
		}

		if query.ID != nil && *query.ID > 0 {
			db = db.Where("id = ?", *query.ID)
		}

		if analysisID := query.GetAnalysisID(); analysisID != "" {
			db = db.Where("analysis_id = ?", analysisID)
		}

		if analysisName := query.GetAnalysisName(); analysisName != "" {
			db = db.Where("analysis_name LIKE ?", "%"+analysisName+"%")
		}

		if workflowID := query.GetWorkflowID(); workflowID != "" {
			db = db.Where("relation_id = ?", workflowID)
		}

		if jobStatus := query.GetJobStatus(); jobStatus != "" {
			db = db.Where("job_status = ?", jobStatus)
		}

		if serverStatus := query.GetServerStatus(); serverStatus != "" {
			db = db.Where("server_status = ?", serverStatus)
		}

		if query.IsReport != nil {
			db = db.Where("is_report = ?", *query.IsReport)
		}

		if query.CacheType != nil {
			db = db.Where("cache_type = ?", *query.CacheType)
		}

		return db
	}

	filtered := applyFilters(base)

	var total int64
	if err := filtered.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	orderBy := "created_at"
	orderDirection := "DESC"
	if query != nil {
		orderBy = query.GetSortColumn()
		orderDirection = query.GetSortOrder()
	}

	err := filtered.
		Order(orderBy + " " + orderDirection).
		Order("id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	return items, total, nil
}

func (r *analysisRepository) GetAnalysisNodeByID(ctx context.Context, id int64) (*types.AnalysisNode, error) {
	item := &types.AnalysisNode{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *analysisRepository) GetAnalysisNodeByAnalysisNodeID(ctx context.Context, analysisNodeID string) (*types.AnalysisNode, error) {
	item := &types.AnalysisNode{}
	if err := r.db.WithContext(ctx).Where("analysis_node_id = ?", analysisNodeID).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *analysisRepository) GetAnalysisNodeByNodeID(ctx context.Context, analysisID int64, nodeID string) (*types.AnalysisNode, error) {
	item := &types.AnalysisNode{}
	if err := r.db.WithContext(ctx).Where("analysis_id = ? AND node_id = ?", analysisID, nodeID).Take(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *analysisRepository) ListAnalysisNodesByAnalysisID(ctx context.Context, analysisID int64) ([]*types.AnalysisNode, error) {
	items := make([]*types.AnalysisNode, 0)
	err := r.db.WithContext(ctx).Where("analysis_id = ?", analysisID).Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *analysisRepository) ListAnalysisNodesByProjectIDAndScriptID(ctx context.Context, projectID, scriptID int64) ([]*types.AnalysisNode, error) {
	items := make([]*types.AnalysisNode, 0)
	err := r.db.WithContext(ctx).Where("project_id = ? AND script_id = ?", projectID, scriptID).Order("updated_at DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *analysisRepository) PageAnalysisNodesByProjectID(ctx context.Context, pagination *types.Pagination, projectID, scriptID int64) ([]*types.AnalysisNode, int64, error) {
	items := make([]*types.AnalysisNode, 0)
	query := r.db.WithContext(ctx).Model(&types.AnalysisNode{}).Where("project_id = ?", projectID)
	if scriptID != 0 {
		query = query.Where("script_id = ?", scriptID)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	if pagination == nil {
		pagination = &types.Pagination{}
	}

	err := query.Order("created_at DESC").Order("id DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	return items, total, nil
}

func (r *analysisRepository) ListAnalysisEdgesByAnalysisID(ctx context.Context, analysisID int64) ([]*types.AnalysisEdge, error) {
	items := make([]*types.AnalysisEdge, 0)
	err := r.db.WithContext(ctx).Where("analysis_id = ?", analysisID).Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}

func (r *analysisRepository) UpdateAnalysisNodeByID(ctx context.Context, id int64, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&types.AnalysisNode{}).Where("id = ?", id).Updates(values).Error
}

func (r *analysisRepository) UpdateAnalysisNodeByAnalysisNodeID(ctx context.Context, analysisNodeID string, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&types.AnalysisNode{}).Where("analysis_node_id = ?", analysisNodeID).Updates(values).Error
}

func (r *analysisRepository) ClaimNextReadyNode(ctx context.Context, analysisID int64, fromStatus string, toStatus string) (*types.AnalysisNode, error) {
	var claimed *types.AnalysisNode
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.
			Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("analysis_id = ? AND status = ?", analysisID, fromStatus)
		if strings.EqualFold(strings.TrimSpace(fromStatus), "ready") {
			query = query.Where("(cache_hit IS NULL OR cache_hit = ?)", false)
		}

		node := &types.AnalysisNode{}
		if err := query.
			Order("id ASC").
			Take(node).Error; err != nil {
			return err
		}

		result := tx.Model(&types.AnalysisNode{}).
			Where("id = ? AND status = ?", node.ID, fromStatus).
			Updates(map[string]any{"status": toStatus})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}

		node.Status = toStatus
		claimed = node
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

func (r *analysisRepository) DeleteAnalysisNodesByAnalysisID(ctx context.Context, analysisID int64) error {
	return r.db.WithContext(ctx).Where("analysis_id = ?", analysisID).Delete(&types.AnalysisNode{}).Error
}

func (r *analysisRepository) DeleteAnalysisNodeByID(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.AnalysisNode{}).Error
}

func (r *analysisRepository) CreateAnalysisNodes(ctx context.Context, items []*types.AnalysisNode) error {
	if len(items) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&items).Error
}

func (r *analysisRepository) DeleteAnalysisEdgesByAnalysisID(ctx context.Context, analysisID int64) error {
	return r.db.WithContext(ctx).Where("analysis_id = ?", analysisID).Delete(&types.AnalysisEdge{}).Error
}

func (r *analysisRepository) CreateAnalysisEdges(ctx context.Context, items []*types.AnalysisEdge) error {
	if len(items) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Create(&items).Error
}

func (r *analysisRepository) ListAnalysisByWorkflowID(ctx context.Context, workflowID string) ([]*types.Analysis, error) {
	items := make([]*types.Analysis, 0)
	err := r.db.WithContext(ctx).Where("relation_id = ?", workflowID).Find(&items).Error
	if err != nil {
		return nil, err
	}
	return items, nil
}
