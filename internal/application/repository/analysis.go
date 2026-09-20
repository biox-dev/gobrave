package repository

import (
	"context"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type analysisRepository struct {
	db *gorm.DB
	// baseDir 是 storage.base_dir。落库的路径一律相对它存储，
	// 读取时在这里还原成绝对路径，所以 base_dir 变更只需拷贝目录。
	baseDir string
}

func NewAnalysisRepository(db *gorm.DB, cfg *config.Config) interfaces.AnalysisRepository {
	return &analysisRepository{db: db, baseDir: resolveStorageBaseDir(cfg)}
}

// resolveStorageBaseDir 与 analysisService.resolveStorageBaseDir 保持一致的兜底策略。
func resolveStorageBaseDir(cfg *config.Config) string {
	if cfg != nil && cfg.Storage != nil {
		if base := strings.TrimSpace(cfg.Storage.BaseDir); base != "" {
			return base
		}
	}
	return "."
}

// --- 路径解析 --------------------------------------------------------------
//
// 数据库只保存相对 base_dir 的路径（唯一例外是历史行里遗留的绝对路径，
// ResolvePath 会原样返回以保证兼容）。派生的子路径不落库，读取时统一重建。

func (r *analysisRepository) resolveAnalysis(item *types.Analysis) *types.Analysis {
	if item == nil {
		return nil
	}
	item.WorkspaceDir = utils.ResolvePath(r.baseDir, item.WorkspaceDir)
	item.HydrateDerivedPaths()
	return item
}

func (r *analysisRepository) resolveAnalyses(items []*types.Analysis) []*types.Analysis {
	for _, item := range items {
		r.resolveAnalysis(item)
	}
	return items
}

func (r *analysisRepository) resolveNode(item *types.AnalysisNode) *types.AnalysisNode {
	if item == nil {
		return nil
	}
	item.WorkspaceDir = utils.ResolvePath(r.baseDir, item.WorkspaceDir)
	item.HydrateDerivedPaths()
	return item
}

func (r *analysisRepository) resolveNodes(items []*types.AnalysisNode) []*types.AnalysisNode {
	for _, item := range items {
		r.resolveNode(item)
	}
	return items
}

// analysisDerivedPathColumns 是 Analysis 上由 WorkspaceDir 派生的列。
// 它们不再落库，任何试图写入的调用都应被丢弃而不是报 "unknown column"。
var analysisDerivedPathColumns = map[string]struct{}{
	"work_dir":          {},
	"output_dir":        {},
	"params_path":       {},
	"command_path":      {},
	"command_log_path":  {},
	"trace_file":        {},
	"workflow_log_file": {},
	"executor_log_file": {},
}

// analysisNodeDerivedPathColumns 是 AnalysisNode 上由 WorkspaceDir 派生的列。
var analysisNodeDerivedPathColumns = map[string]struct{}{
	"output_dir":   {},
	"cache_dir":    {},
	"command_path": {},
	"params_path":  {},
	"log_path":     {},
}

// sanitizeAnalysisUpdate 把更新 map 收敛到仍然存在的列：
// 派生路径列被丢弃，workspace_dir 被转成相对路径。
func (r *analysisRepository) sanitizeAnalysisUpdate(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		if _, derived := analysisDerivedPathColumns[key]; derived {
			continue
		}
		out[key] = value
	}
	if _, ok := out["workspace_dir"]; ok {
		if path, isString := out["workspace_dir"].(string); isString {
			out["workspace_dir"] = utils.RelPath(r.baseDir, path)
		}
	}
	return out
}

// sanitizeNodeUpdate 同上，作用于 analysis_nodes。
func (r *analysisRepository) sanitizeNodeUpdate(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		if _, derived := analysisNodeDerivedPathColumns[key]; derived {
			continue
		}
		out[key] = value
	}
	if _, ok := out["workspace_dir"]; ok {
		if path, isString := out["workspace_dir"].(string); isString {
			out["workspace_dir"] = utils.RelPath(r.baseDir, path)
		}
	}
	return out
}

func (r *analysisRepository) WithTransaction(ctx context.Context, fn func(interfaces.AnalysisRepository) error) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		txRepo := &analysisRepository{db: tx, baseDir: r.baseDir}
		return fn(txRepo)
	})
}

func (r *analysisRepository) CreateAnalysis(ctx context.Context, item *types.Analysis) error {
	if item == nil {
		return nil
	}
	absolute := item.WorkspaceDir
	item.WorkspaceDir = utils.RelPath(r.baseDir, absolute)
	err := r.db.WithContext(ctx).Create(item).Error
	// 还原调用方持有的绝对路径，避免 handler 直接把相对路径返回给前端。
	item.WorkspaceDir = absolute
	return err
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
	values = r.sanitizeAnalysisUpdate(values)
	if len(values) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&types.Analysis{}).Where("analysis_id = ?", analysisID).Updates(values).Error
}
func (r *analysisRepository) UpdateAnalysisByID(ctx context.Context, analysisID int64, values map[string]any) error {
	values = r.sanitizeAnalysisUpdate(values)
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
	return r.resolveAnalysis(item), nil
}
func (r *analysisRepository) GetAnalysisByAnalysisID(ctx context.Context, analysisID string) (*types.Analysis, error) {
	item := &types.Analysis{}
	if err := r.db.WithContext(ctx).Where("analysis_id = ?", analysisID).Take(item).Error; err != nil {
		return nil, err
	}
	return r.resolveAnalysis(item), nil
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
	return r.resolveAnalyses(items), nil
}

func (r *analysisRepository) ListAnalysisByProjectID(ctx context.Context, projectID int64, query *types.AnalysisQuey) ([]*types.Analysis, error) {
	items := make([]*types.Analysis, 0)
	base := r.db.WithContext(ctx).Model(&types.Analysis{}).Where("project_id = ?", projectID)

	if query != nil {
		if len(query.IDs) > 0 {
			base = base.Where("id IN ?", query.IDs)
		}

		if query.ID != nil && *query.ID > 0 {
			base = base.Where("id = ?", *query.ID)
		}

		if analysisID := query.GetAnalysisID(); analysisID != "" {
			base = base.Where("analysis_id = ?", analysisID)
		}

		if analysisName := query.GetAnalysisName(); analysisName != "" {
			base = base.Where("analysis_name LIKE ?", "%"+analysisName+"%")
		}

		if workflowID := query.GetWorkflowID(); workflowID > 0 {
			base = base.Where("workflow_id = ?", workflowID)
		}

		if jobStatus := query.GetJobStatus(); jobStatus != "" {
			base = base.Where("job_status = ?", jobStatus)
		}

		if serverStatus := query.GetServerStatus(); serverStatus != "" {
			base = base.Where("server_status = ?", serverStatus)
		}

		if query.IsReport != nil {
			base = base.Where("is_report = ?", *query.IsReport)
		}

		if query.CacheType != nil {
			base = base.Where("cache_type = ?", *query.CacheType)
		}
	}

	err := base.
		Order("created_at DESC").
		Order("id DESC").
		Find(&items).Error
	if err != nil {
		return nil, err
	}

	return r.resolveAnalyses(items), nil
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

		if workflowID := query.GetWorkflowID(); workflowID > 0 {
			db = db.Where("workflow_id = ?", workflowID)
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

	return r.resolveAnalyses(items), total, nil
}

func (r *analysisRepository) GetAnalysisNodeByID(ctx context.Context, id int64) (*types.AnalysisNode, error) {
	item := &types.AnalysisNode{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return r.resolveNode(item), nil
}

func (r *analysisRepository) GetAnalysisNodeByAnalysisNodeID(ctx context.Context, analysisNodeID string) (*types.AnalysisNode, error) {
	item := &types.AnalysisNode{}
	if err := r.db.WithContext(ctx).Where("analysis_node_id = ?", analysisNodeID).Take(item).Error; err != nil {
		return nil, err
	}
	return r.resolveNode(item), nil
}

func (r *analysisRepository) GetAnalysisNodeByNodeID(ctx context.Context, analysisID int64, nodeID string) (*types.AnalysisNode, error) {
	item := &types.AnalysisNode{}
	if err := r.db.WithContext(ctx).Where("analysis_id = ? AND node_id = ?", analysisID, nodeID).Take(item).Error; err != nil {
		return nil, err
	}
	return r.resolveNode(item), nil
}

func (r *analysisRepository) ListAnalysisNodesByAnalysisID(ctx context.Context, analysisID int64) ([]*types.AnalysisNode, error) {
	items := make([]*types.AnalysisNode, 0)
	err := r.db.WithContext(ctx).Where("analysis_id = ?", analysisID).Find(&items).Error
	if err != nil {
		return nil, err
	}
	return r.resolveNodes(items), nil
}

func (r *analysisRepository) ListAnalysisNodesByProjectIDAndScriptID(ctx context.Context, projectID, scriptID int64) ([]*types.AnalysisNode, error) {
	items := make([]*types.AnalysisNode, 0)
	err := r.db.WithContext(ctx).Where("project_id = ? AND script_id = ?", projectID, scriptID).Order("updated_at DESC").Find(&items).Error
	if err != nil {
		return nil, err
	}
	return r.resolveNodes(items), nil
}

func (r *analysisRepository) ListAnalysisNodesByProjectIDAndStatus(ctx context.Context, projectID int64, status string) ([]*types.AnalysisNode, error) {
	items := make([]*types.AnalysisNode, 0)
	err := r.db.WithContext(ctx).
		Where("project_id = ? AND status = ?", projectID, status).
		Order("updated_at DESC").
		Limit(100).
		Find(&items).Error
	if err != nil {
		return nil, err
	}
	return r.resolveNodes(items), nil
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

	return r.resolveNodes(items), total, nil
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
	values = r.sanitizeNodeUpdate(values)
	if len(values) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Model(&types.AnalysisNode{}).Where("id = ?", id).Updates(values).Error
}

func (r *analysisRepository) UpdateAnalysisNodeByAnalysisNodeID(ctx context.Context, analysisNodeID string, values map[string]any) error {
	values = r.sanitizeNodeUpdate(values)
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
	return r.resolveNode(claimed), nil
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
	// 落库前把绝对路径转成相对路径，写完后还原，避免调用方拿到相对路径。
	absolute := make([]string, len(items))
	for i, item := range items {
		if item == nil {
			continue
		}
		absolute[i] = item.WorkspaceDir
		item.WorkspaceDir = utils.RelPath(r.baseDir, absolute[i])
	}
	err := r.db.WithContext(ctx).Create(&items).Error
	for i, item := range items {
		if item == nil {
			continue
		}
		item.WorkspaceDir = absolute[i]
		item.HydrateDerivedPaths()
	}
	return err
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

func (r *analysisRepository) DeleteAnalysisByID(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.Analysis{}).Error
}

func (r *analysisRepository) ListAnalysisByWorkflowID(ctx context.Context, workflowID int64) ([]*types.Analysis, error) {
	items := make([]*types.Analysis, 0)
	if workflowID <= 0 {
		return items, nil
	}
	err := r.db.WithContext(ctx).Where("workflow_id = ?", workflowID).Find(&items).Error
	if err != nil {
		return nil, err
	}
	return r.resolveAnalyses(items), nil
}
