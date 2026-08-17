package interfaces

import (
	"context"

	"github.com/gobravedev/gobrave/internal/types"
)

// ProjectService defines project business capabilities.
type ProjectService interface {
	ListProjectByUserID(ctx context.Context, userID string) ([]*types.Project, error)
	GetActiveProjectByUserID(ctx context.Context, userID string) (*types.Project, error)
	GetActiveProjectDirByUserID(ctx context.Context, userID, baseDir string) (*types.Project, string, error)
	GetProjectByID(ctx context.Context, id int64) (*types.Project, error)
	AddUserProject(ctx context.Context, userID, projectID string) error
	ActivateUserProject(ctx context.Context, userID, projectID string) error
	CreateDefaultProjectForUser(ctx context.Context, userID, username string) error
	AddProjectReport(ctx context.Context, userID string, report *types.ProjectReport) error
	UpdateProjectReport(ctx context.Context, userID string, report *types.ProjectReport) error
	DeleteProjectReport(ctx context.Context, userID string, reportID int64) error
	ListProjectReportByProjectID(ctx context.Context, userID, projectID string) ([]*types.ProjectReport, error)
	GetProjectReportDetailByID(ctx context.Context, userID string, reportID int64) (*types.ProjectReport, error)
}

// ProjectRepository defines project data access methods.
type ProjectRepository interface {
	ListProjectByUserID(ctx context.Context, userID string) ([]*types.Project, error)
	GetProjectByID(ctx context.Context, id int64) (*types.Project, error)
	GetActiveProjectByUserID(ctx context.Context, userID string) (*types.Project, error)
	CreateProject(ctx context.Context, project *types.Project) error
	AddUserProject(ctx context.Context, up *types.UserProject) error
	ExistsUserProject(ctx context.Context, userID, projectID string) (bool, error)
	ActivateUserProject(ctx context.Context, userID, projectID string) error
	AddProjectReport(ctx context.Context, report *types.ProjectReport) error
	GetProjectReportByID(ctx context.Context, reportID int64) (*types.ProjectReport, error)
	UpdateProjectReport(ctx context.Context, report *types.ProjectReport) error
	DeleteProjectReport(ctx context.Context, projectID string, reportID int64) error
	ListProjectReportByProjectID(ctx context.Context, projectID string) ([]*types.ProjectReport, error)
}
