package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

// ProjectService defines project business capabilities.
type ProjectService interface {
	ListProjectByUserID(ctx context.Context, userID string) ([]*types.ProjectListItem, error)
	GetActiveProjectByUserID(ctx context.Context, userID string) (*types.Project, error)
	GetActiveProjectDirByUserID(ctx context.Context, userID, baseDir string) (*types.Project, string, error)
	GetProjectByID(ctx context.Context, id int64) (*types.Project, error)
	AddUserProject(ctx context.Context, userID, projectID string) error
	AddUserProjectByShareCode(ctx context.Context, userID, shareCode string) error
	UpdateProjectSharing(ctx context.Context, userID, projectID string, enabled bool) (string, error)
	ActivateUserProject(ctx context.Context, userID, projectID string) error
	DeleteUserProject(ctx context.Context, userID, projectID string) error
	CreateDefaultProjectForUser(ctx context.Context, userID, username string) error
	CreateProjectForUser(ctx context.Context, userID string, project *types.Project) (*types.Project, error)
	AddProjectReport(ctx context.Context, userID string, report *types.ProjectReport) error
	UpdateProjectReport(ctx context.Context, userID string, report *types.ProjectReport) error
	DeleteProjectReport(ctx context.Context, userID string, reportID int64) error
	ListProjectReportByProjectID(ctx context.Context, userID, projectID string) ([]*types.ProjectReport, error)
	PageProjectReportByProjectID(ctx context.Context, userID, projectID string, pagination *types.Pagination) ([]*types.ProjectReport, int64, error)
	GetProjectReportDetailByID(ctx context.Context, userID string, reportID int64) (*types.ProjectReport, error)
	GetProjectReportByID(ctx context.Context, reportID int64) (*types.ProjectReport, error)
	// ProjectReportItem business capabilities.
	ListProjectReportItemsByReportID(ctx context.Context, userID string, reportID int64) ([]*types.ProjectReportItem, error)
	AddProjectReportItem(ctx context.Context, userID string, item *types.ProjectReportItem) error
	UpdateProjectReportItem(ctx context.Context, userID string, item *types.ProjectReportItem) error
	UpdateProjectReportItemContent(ctx context.Context, userID string, itemID int64, content string) error
	DeleteProjectReportItem(ctx context.Context, userID string, itemID int64) error
	GetProjectReportItemDetailByID(ctx context.Context, userID string, itemID int64) (*types.ProjectReportItem, error)
	// GetProjectReportItemContent 返回指定条目的 markdown 内容（入参为 ProjectReportItem ID）。
	GetProjectReportItemContent(ctx context.Context, userID string, itemID int64) (*types.ProjectReportItem, string, error)
	// GetProjectReportContent 汇总报告下所有条目，按顺序拼接成正文。
	GetProjectReportContent(ctx context.Context, userID string, reportID int64) (*types.ProjectReport, []*types.ProjectReportItem, string, error)

	// Literature (参考文献) business capabilities.
	AddLiterature(ctx context.Context, userID string, literature *types.Literature) (*types.Literature, error)
	UpdateLiterature(ctx context.Context, userID string, literature *types.Literature) error
	DeleteLiterature(ctx context.Context, userID string, literatureID int64) error
	GetLiteratureDetailByID(ctx context.Context, userID string, literatureID int64) (*types.Literature, error)
	ListLiteratureByProjectID(ctx context.Context, userID string) ([]*types.Literature, error)
	PageLiteratureByProjectID(ctx context.Context, userID string, pagination *types.Pagination) ([]*types.Literature, int64, error)
	BindLiteratureToProject(ctx context.Context, userID string, literatureID int64) error
	UnbindLiteratureFromProject(ctx context.Context, userID string, literatureID int64) error
	PageLiteraturePool(ctx context.Context, userID string, pagination *types.Pagination) ([]*types.LiteraturePoolItem, int64, error)
}

// ProjectRepository defines project data access methods.
type ProjectRepository interface {
	ListProjectByUserID(ctx context.Context, userID string) ([]*types.ProjectListItem, error)
	GetProjectByID(ctx context.Context, id int64) (*types.Project, error)
	GetActiveProjectByUserID(ctx context.Context, userID string) (*types.Project, error)
	CreateProject(ctx context.Context, project *types.Project) error
	AddUserProject(ctx context.Context, up *types.UserProject) error
	ExistsUserProject(ctx context.Context, userID, projectID string) (bool, error)
	GetUserProject(ctx context.Context, userID, projectID string) (*types.UserProject, error)
	GetUserProjectByShareCode(ctx context.Context, shareCode string) (*types.UserProject, error)
	UpdateProjectSharing(ctx context.Context, userID, projectID string, enabled bool, shareCode string) error
	DeleteUserProject(ctx context.Context, userID, projectID string) error
	ActivateUserProject(ctx context.Context, userID, projectID string) error
	AddProjectReport(ctx context.Context, report *types.ProjectReport) error
	GetProjectReportByID(ctx context.Context, reportID int64) (*types.ProjectReport, error)
	UpdateProjectReport(ctx context.Context, report *types.ProjectReport) error
	DeleteProjectReport(ctx context.Context, projectID string, reportID int64) error
	ListProjectReportByProjectID(ctx context.Context, projectID string) ([]*types.ProjectReport, error)
	PageProjectReportByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) ([]*types.ProjectReport, int64, error)
	// ProjectReportItem data access methods.
	AddProjectReportItem(ctx context.Context, item *types.ProjectReportItem) error
	GetProjectReportItemByID(ctx context.Context, itemID int64) (*types.ProjectReportItem, error)
	UpdateProjectReportItem(ctx context.Context, item *types.ProjectReportItem) error
	DeleteProjectReportItem(ctx context.Context, itemID int64) error
	DeleteProjectReportItemsByReportID(ctx context.Context, reportID int64) error
	ListProjectReportItemsByReportID(ctx context.Context, reportID int64) ([]*types.ProjectReportItem, error)

	// Literature data access methods.
	CreateLiterature(ctx context.Context, literature *types.Literature) error
	GetLiteratureByID(ctx context.Context, literatureID int64) (*types.Literature, error)
	UpdateLiterature(ctx context.Context, literature *types.Literature) error
	DeleteLiterature(ctx context.Context, literatureID int64) error
	ListLiteratureByProjectID(ctx context.Context, projectID string) ([]*types.Literature, error)
	PageLiteratureByProjectID(ctx context.Context, pagination *types.Pagination, projectID string) ([]*types.Literature, int64, error)
	AddProjectLiterature(ctx context.Context, pl *types.ProjectLiterature) error
	ExistsProjectLiterature(ctx context.Context, projectID string, literatureID int64) (bool, error)
	DeleteProjectLiterature(ctx context.Context, projectID string, literatureID int64) error
	DeleteProjectLiteratureByLiteratureID(ctx context.Context, literatureID int64) error
	PageLiteraturePool(ctx context.Context, pagination *types.Pagination, projectID string) ([]*types.LiteraturePoolItem, int64, error)
}
