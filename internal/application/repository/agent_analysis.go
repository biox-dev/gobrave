package repository

import (
	"context"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

type agentAnalysisRepository struct {
	db *gorm.DB
	// baseDir 是 storage.base_dir。落库的 workspace_dir 相对它存储，
	// 读取时还原成绝对路径，使存储根目录迁移无需重写数据行。
	baseDir string
}

func NewAgentAnalysisRepository(db *gorm.DB, cfg *config.Config) interfaces.AgentAnalysisRepository {
	return &agentAnalysisRepository{db: db, baseDir: resolveStorageBaseDir(cfg)}
}

func (r *agentAnalysisRepository) resolve(item *types.AgentAnalysis) *types.AgentAnalysis {
	if item == nil {
		return nil
	}
	item.WorkspaceDir = utils.ResolvePath(r.baseDir, item.WorkspaceDir)
	item.HydrateDerivedPaths()
	return item
}

func (r *agentAnalysisRepository) CreateAgentAnalysis(ctx context.Context, item *types.AgentAnalysis) error {
	if item == nil {
		return nil
	}
	// 落库前转成相对路径，写完后还原，调用方拿到的仍是绝对路径。
	absolute := item.WorkspaceDir
	item.WorkspaceDir = utils.RelPath(r.baseDir, absolute)
	err := r.db.WithContext(ctx).Create(item).Error
	item.WorkspaceDir = absolute
	item.HydrateDerivedPaths()
	return err
}

func (r *agentAnalysisRepository) GetAgentAnalysisByID(ctx context.Context, id int64) (*types.AgentAnalysis, error) {
	item := &types.AgentAnalysis{}
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(item).Error; err != nil {
		return nil, err
	}
	return r.resolve(item), nil
}

func (r *agentAnalysisRepository) ListAgentAnalysisByProjectID(ctx context.Context, projectID int64) ([]*types.AgentAnalysis, error) {
	items := make([]*types.AgentAnalysis, 0)
	if err := r.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("created_at DESC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	for _, item := range items {
		r.resolve(item)
	}
	return items, nil
}

func (r *agentAnalysisRepository) DeleteAgentAnalysis(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&types.AgentAnalysis{}).Error
}

func (r *agentAnalysisRepository) UpdateAgentAnalysisByID(ctx context.Context, id int64, values map[string]any) error {
	if len(values) == 0 {
		return nil
	}
	if path, ok := values["workspace_dir"].(string); ok {
		values["workspace_dir"] = utils.RelPath(r.baseDir, path)
	}
	return r.db.WithContext(ctx).Model(&types.AgentAnalysis{}).Where("id = ?", id).Updates(values).Error
}
