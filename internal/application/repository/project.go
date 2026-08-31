package repository

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

type projectRepository struct {
	db *gorm.DB
}

func NewProjectRepository(db *gorm.DB) interfaces.ProjectRepository {
	return &projectRepository{db: db}
}

func (r *projectRepository) CreateProject(ctx context.Context, project *types.Project) error {
	return r.db.WithContext(ctx).Create(project).Error
}

func (r *projectRepository) AddUserProject(ctx context.Context, up *types.UserProject) error {
	return r.db.WithContext(ctx).Create(up).Error
}

func (r *projectRepository) ExistsUserProject(ctx context.Context, userID, projectID string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.UserProject{}).
		Where("user_id = ? AND project_id = ?", userID, projectID).
		Count(&count).Error
	return count > 0, err
}

func (r *projectRepository) GetUserProject(ctx context.Context, userID, projectID string) (*types.UserProject, error) {
	up := &types.UserProject{}
	err := r.db.WithContext(ctx).
		Where("user_id = ? AND project_id = ?", userID, projectID).
		Take(up).Error
	if err != nil {
		return nil, err
	}
	return up, nil
}

func (r *projectRepository) GetUserProjectByShareCode(ctx context.Context, shareCode string) (*types.UserProject, error) {
	up := &types.UserProject{}
	err := r.db.WithContext(ctx).
		Where("share_code = ?", shareCode).
		Take(up).Error
	if err != nil {
		return nil, err
	}
	return up, nil
}

func (r *projectRepository) UpdateProjectSharing(ctx context.Context, userID, projectID string, enabled bool, shareCode string) error {
	return r.db.WithContext(ctx).
		Model(&types.UserProject{}).
		Where("user_id = ? AND project_id = ?", userID, projectID).
		Updates(map[string]interface{}{
			"share_enabled": enabled,
			"share_code":    shareCode,
		}).Error
}

func (r *projectRepository) DeleteUserProject(ctx context.Context, userID, projectID string) error {
	return r.db.WithContext(ctx).
		Where("user_id = ? AND project_id = ?", userID, projectID).
		Delete(&types.UserProject{}).Error
}

func (r *projectRepository) GetActiveProjectByUserID(ctx context.Context, userID string) (*types.Project, error) {
	project := &types.Project{}
	err := r.db.WithContext(ctx).
		Table("t_project AS p").
		Select("p.*").
		Joins("INNER JOIN user_project up ON up.project_id = p.project_id").
		Where("up.user_id = ? AND up.is_active = ?", userID, true).
		Order("up.id DESC").
		Limit(1).
		Scan(project).Error
	if err != nil {
		return nil, err
	}
	if project.ID == 0 {
		return nil, gorm.ErrRecordNotFound
	}

	return project, nil
}

func (r *projectRepository) ActivateUserProject(ctx context.Context, userID, projectID string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Model(&types.UserProject{}).
			Where("user_id = ? AND project_id = ?", userID, projectID).
			Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return gorm.ErrRecordNotFound
		}

		if err := tx.Model(&types.UserProject{}).
			Where("user_id = ?", userID).
			Update("is_active", false).Error; err != nil {
			return err
		}

		if err := tx.Model(&types.UserProject{}).
			Where("user_id = ? AND project_id = ?", userID, projectID).
			Update("is_active", true).Error; err != nil {
			return err
		}

		return nil
	})
}

func (r *projectRepository) ListProjectByUserID(ctx context.Context, userID string) ([]*types.ProjectListItem, error) {
	projects := make([]*types.ProjectListItem, 0)
	err := r.db.WithContext(ctx).
		Table("t_project AS p").
		Select("p.*, up.share_code, up.share_enabled").
		Joins("INNER JOIN user_project up ON up.project_id = p.project_id").
		Where("up.user_id = ?", userID).
		Order("p.id DESC").
		Scan(&projects).Error
	if err != nil {
		return nil, err
	}

	return projects, nil
}

func (r *projectRepository) GetProjectByID(ctx context.Context, id int64) (*types.Project, error) {
	project := &types.Project{}
	err := r.db.WithContext(ctx).
		Where("id = ?", id).
		Take(project).Error
	if err != nil {
		return nil, err
	}

	return project, nil
}

func (r *projectRepository) AddProjectReport(ctx context.Context, report *types.ProjectReport) error {
	return r.db.WithContext(ctx).Create(report).Error
}

func (r *projectRepository) GetProjectReportByID(ctx context.Context, reportID int64) (*types.ProjectReport, error) {
	report := &types.ProjectReport{}
	err := r.db.WithContext(ctx).
		Where("id = ?", reportID).
		Take(report).Error
	if err != nil {
		return nil, err
	}

	return report, nil
}

func (r *projectRepository) UpdateProjectReport(ctx context.Context, report *types.ProjectReport) error {
	return r.db.WithContext(ctx).
		Model(&types.ProjectReport{}).
		Where("id = ? AND project_id = ?", report.ID, report.ProjectID).
		Updates(map[string]interface{}{
			"title":          report.Title,
			"content":        report.Content,
			"sort_order":     report.SortOrder,
			"content_source": report.ContentSource,
			"filename":       report.Filename,
		}).Error
}

func (r *projectRepository) DeleteProjectReport(ctx context.Context, projectID string, reportID int64) error {
	return r.db.WithContext(ctx).
		Where("id = ? AND project_id = ?", reportID, projectID).
		Delete(&types.ProjectReport{}).Error
}

func (r *projectRepository) ListProjectReportByProjectID(ctx context.Context, projectID string) ([]*types.ProjectReport, error) {
	reports := make([]*types.ProjectReport, 0)
	err := r.db.WithContext(ctx).
		Model(&types.ProjectReport{}).
		Select("id, project_id, title, sort_order, created_at, updated_at").
		Where("project_id = ?", projectID).
		Order("sort_order DESC").
		Order("created_at ASC").
		Find(&reports).Error
	if err != nil {
		return nil, err
	}

	return reports, nil
}

func (r *projectRepository) PageProjectReportByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) ([]*types.ProjectReport, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.ProjectReport, 0)
	base := r.db.WithContext(ctx).
		Model(&types.ProjectReport{}).
		Select("id, project_id, title, sort_order, created_at, updated_at").
		Where("project_id = ?", projectID)

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := base.
		Order("sort_order DESC").
		Order("created_at ASC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	return items, total, nil
}

func (r *projectRepository) ListProjectReportDetailByProjectID(ctx context.Context, projectID string) ([]*types.ProjectReport, error) {
	reports := make([]*types.ProjectReport, 0)
	err := r.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("sort_order DESC").
		Order("created_at ASC").
		Find(&reports).Error
	if err != nil {
		return nil, err
	}

	return reports, nil
}

// ---------- Literature ----------

func (r *projectRepository) CreateLiterature(ctx context.Context, literature *types.Literature) error {
	return r.db.WithContext(ctx).Create(literature).Error
}

func (r *projectRepository) GetLiteratureByID(ctx context.Context, literatureID int64) (*types.Literature, error) {
	literature := &types.Literature{}
	err := r.db.WithContext(ctx).
		Where("id = ?", literatureID).
		Take(literature).Error
	if err != nil {
		return nil, err
	}

	return literature, nil
}

func (r *projectRepository) UpdateLiterature(ctx context.Context, literature *types.Literature) error {
	return r.db.WithContext(ctx).
		Model(&types.Literature{}).
		Where("id = ?", literature.ID).
		Updates(map[string]interface{}{
			"title":            literature.Title,
			"content":          literature.Content,
			"content_source":   literature.ContentSource,
			"filename":         literature.Filename,
			"owner_project_id": literature.OwnerProjectID,
		}).Error
}

func (r *projectRepository) DeleteLiterature(ctx context.Context, literatureID int64) error {
	return r.db.WithContext(ctx).
		Where("id = ?", literatureID).
		Delete(&types.Literature{}).Error
}

func (r *projectRepository) ListLiteratureByProjectID(ctx context.Context, projectID string) ([]*types.Literature, error) {
	items := make([]*types.Literature, 0)
	err := r.db.WithContext(ctx).
		Table("t_literature AS l").
		Select("l.*").
		Joins("INNER JOIN project_literature pl ON pl.literature_id = l.id").
		Where("pl.project_id = ?", projectID).
		Order("l.updated_at DESC").
		Find(&items).Error
	if err != nil {
		return nil, err
	}

	return items, nil
}

func (r *projectRepository) PageLiteratureByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) ([]*types.Literature, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.Literature, 0)
	base := r.db.WithContext(ctx).
		Table("t_literature AS l").
		Joins("INNER JOIN project_literature pl ON pl.literature_id = l.id").
		Where("pl.project_id = ?", projectID)

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := base.
		Select("l.*").
		Order("l.updated_at DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Find(&items).Error
	if err != nil {
		return nil, 0, err
	}

	return items, total, nil
}

func (r *projectRepository) AddProjectLiterature(ctx context.Context, pl *types.ProjectLiterature) error {
	return r.db.WithContext(ctx).Create(pl).Error
}

func (r *projectRepository) ExistsProjectLiterature(ctx context.Context, projectID string, literatureID int64) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.ProjectLiterature{}).
		Where("project_id = ? AND literature_id = ?", projectID, literatureID).
		Count(&count).Error
	return count > 0, err
}

func (r *projectRepository) DeleteProjectLiterature(ctx context.Context, projectID string, literatureID int64) error {
	return r.db.WithContext(ctx).
		Where("project_id = ? AND literature_id = ?", projectID, literatureID).
		Delete(&types.ProjectLiterature{}).Error
}

func (r *projectRepository) DeleteProjectLiteratureByLiteratureID(ctx context.Context, literatureID int64) error {
	return r.db.WithContext(ctx).
		Where("literature_id = ?", literatureID).
		Delete(&types.ProjectLiterature{}).Error
}

func (r *projectRepository) PageLiteraturePool(ctx context.Context, pagination *types.Pagination, projectID string) ([]*types.LiteraturePoolItem, int64, error) {
	if pagination == nil {
		pagination = &types.Pagination{}
	}

	items := make([]*types.LiteraturePoolItem, 0)
	base := r.db.WithContext(ctx).
		Table("t_literature AS l").
		Select("l.*, (pl.id IS NOT NULL) AS bound").
		Joins("LEFT JOIN project_literature pl ON pl.literature_id = l.id AND pl.project_id = ?", projectID)

	var total int64
	if err := base.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	err := base.
		Order("l.updated_at DESC").
		Offset(pagination.Offset()).
		Limit(pagination.Limit()).
		Scan(&items).Error
	if err != nil {
		return nil, 0, err
	}

	return items, total, nil
}
