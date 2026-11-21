package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

// ProjectService defines project business capabilities.
type ProjectService interface {
	ListProjectByUserID(ctx context.Context, userID int64) ([]*types.ProjectListItem, error)
	GetActiveProjectByUserID(ctx context.Context, userID int64) (*types.Project, error)
	GetActiveProjectDirByUserID(ctx context.Context, userID int64, baseDir string) (*types.Project, string, error)
	GetProjectByID(ctx context.Context, id int64) (*types.Project, error)
	AddUserProject(ctx context.Context, userID int64, projectID int64) error
	AddUserProjectByShareCode(ctx context.Context, userID int64, shareCode string) error
	// ListProjectUserIDs 返回项目下的全部用户 ID（user_project 关系表）。
	// 用于「按项目共享」的资源鉴权：项目成员都能查看该项目下的资源。
	ListProjectUserIDs(ctx context.Context, projectID int64) ([]int64, error)
	UpdateProjectSharing(ctx context.Context, userID int64, projectID int64, enabled bool) (string, error)
	ActivateUserProject(ctx context.Context, userID int64, projectID int64) error
	DeleteUserProject(ctx context.Context, userID int64, projectID int64) error
	// GetActiveUserMenuConfig 返回用户在当前激活项目下的菜单配置（主区域标签页 + 底部面板）。
	// 用户没有激活项目时返回空配置，不报错。
	GetActiveUserMenuConfig(ctx context.Context, userID int64) (types.UserMenuConfig, error)
	// UpdateActiveUserMenuConfig 整体覆盖用户在当前激活项目下的菜单配置。
	// 没有激活项目时返回 gorm.ErrRecordNotFound。
	UpdateActiveUserMenuConfig(ctx context.Context, userID int64, config types.UserMenuConfig) error
	CreateDefaultProjectForUser(ctx context.Context, userID int64, username string) error
	CreateProjectForUser(ctx context.Context, userID int64, project *types.Project) (*types.Project, error)
	AddProjectReport(ctx context.Context, userID int64, report *types.ProjectReport) error
	UpdateProjectReport(ctx context.Context, userID int64, report *types.ProjectReport) error
	DeleteProjectReport(ctx context.Context, userID int64, reportID int64) error
	ListProjectReportByProjectID(ctx context.Context, userID int64, projectID int64) ([]*types.ProjectReport, error)
	PageProjectReportByProjectID(ctx context.Context, userID int64, projectID int64, pagination *types.Pagination) ([]*types.ProjectReport, int64, error)
	GetProjectReportDetailByID(ctx context.Context, userID int64, reportID int64) (*types.ProjectReport, error)
	GetProjectReportByID(ctx context.Context, reportID int64) (*types.ProjectReport, error)
	// ProjectReportPlan 报告大纲（跨会话共享的报告结构）。
	GetProjectReportPlanByID(ctx context.Context, userID int64, planID int64) (*types.ProjectReportPlan, error)
	ListProjectReportPlansByProjectID(ctx context.Context, userID int64, projectID int64) ([]*types.ProjectReportPlan, error)
	// SaveProjectReportPlan 新建（ID=0）或整体覆盖大纲，返回保存后的记录。
	SaveProjectReportPlan(ctx context.Context, userID int64, plan *types.ProjectReportPlan) (*types.ProjectReportPlan, error)
	DeleteProjectReportPlan(ctx context.Context, userID int64, planID int64) error
	// GetProjectReportFile 读取报告工作目录下的自身 markdown 文件（output.md）内容。
	GetProjectReportFile(ctx context.Context, userID int64, reportID int64) (string, error)
	// UpdateProjectReportFile 覆写报告工作目录下的自身 markdown 文件（output.md）内容。
	// 目录尚不是 git 仓库时会就地初始化（不自动提交），保证报告目录始终处于版本管理下。
	UpdateProjectReportFile(ctx context.Context, userID int64, reportID int64, content string) error
	// GetProjectReportGitDiff 返回报告工作目录的本地未提交改动（报告没有发布目标）。
	GetProjectReportGitDiff(ctx context.Context, userID int64, reportID int64) (*types.ProjectReportGitDiff, error)
	// CommitProjectReport 把报告工作目录的所有改动提交为一个 commit（无改动时不产生空提交）。
	CommitProjectReport(ctx context.Context, userID int64, reportID int64, message string) (*types.ProjectReportCommitResult, error)
	// RevertProjectReportFile 撤回报告工作目录里单个文件的未提交改动（恢复为本地 HEAD 版本）。
	RevertProjectReportFile(ctx context.Context, userID int64, reportID int64, relPath string) error

	// Literature (参考文献) business capabilities.
	AddLiterature(ctx context.Context, userID int64, literature *types.Literature, content string) (*types.Literature, string, error)
	UpdateLiterature(ctx context.Context, userID int64, literature *types.Literature, content string) error
	DeleteLiterature(ctx context.Context, userID int64, literatureID int64) error
	GetLiteratureDetailByID(ctx context.Context, userID int64, literatureID int64) (*types.LiteratureDetail, error)
	ListLiteratureByProjectID(ctx context.Context, userID int64) ([]*types.Literature, error)
	PageLiteratureByProjectID(ctx context.Context, userID int64, pagination *types.Pagination) ([]*types.Literature, int64, error)
	BindLiteratureToProject(ctx context.Context, userID int64, literatureID int64) error
	UnbindLiteratureFromProject(ctx context.Context, userID int64, literatureID int64) error
	PageLiteraturePool(ctx context.Context, userID int64, pagination *types.Pagination) ([]*types.LiteraturePoolItem, int64, error)
}

// ProjectRepository defines project data access methods.
type ProjectRepository interface {
	ListProjectByUserID(ctx context.Context, userID int64) ([]*types.ProjectListItem, error)
	GetProjectByID(ctx context.Context, id int64) (*types.Project, error)
	GetActiveProjectByUserID(ctx context.Context, userID int64) (*types.Project, error)
	CreateProject(ctx context.Context, project *types.Project) error
	AddUserProject(ctx context.Context, up *types.UserProject) error
	// ListUserIDsByProjectID 返回项目下的全部用户 ID（user_project.project_id -> user_id）。
	ListUserIDsByProjectID(ctx context.Context, projectID int64) ([]int64, error)
	ExistsUserProject(ctx context.Context, userID int64, projectID int64) (bool, error)
	GetUserProject(ctx context.Context, userID int64, projectID int64) (*types.UserProject, error)
	// GetActiveUserProjectByUserID 返回用户当前激活的项目绑定（含该项目下的菜单配置）。
	GetActiveUserProjectByUserID(ctx context.Context, userID int64) (*types.UserProject, error)
	GetUserProjectByShareCode(ctx context.Context, shareCode string) (*types.UserProject, error)
	UpdateProjectSharing(ctx context.Context, userID int64, projectID int64, enabled bool, shareCode string) error
	// UpdateUserProjectMenuConfig 覆盖某个用户在某个项目下的菜单配置。
	UpdateUserProjectMenuConfig(ctx context.Context, userID int64, projectID int64, config types.UserMenuConfig) error
	DeleteUserProject(ctx context.Context, userID int64, projectID int64) error
	ActivateUserProject(ctx context.Context, userID int64, projectID int64) error
	AddProjectReport(ctx context.Context, report *types.ProjectReport) error
	GetProjectReportByID(ctx context.Context, reportID int64) (*types.ProjectReport, error)
	UpdateProjectReport(ctx context.Context, report *types.ProjectReport) error
	DeleteProjectReport(ctx context.Context, projectID int64, reportID int64) error
	ListProjectReportByProjectID(ctx context.Context, projectID int64) ([]*types.ProjectReport, error)
	PageProjectReportByProjectID(ctx context.Context, pagination *types.Pagination, projectID int64) ([]*types.ProjectReport, int64, error)
	// ProjectReportPlan data access methods.
	AddProjectReportPlan(ctx context.Context, plan *types.ProjectReportPlan) error
	GetProjectReportPlanByID(ctx context.Context, planID int64) (*types.ProjectReportPlan, error)
	UpdateProjectReportPlan(ctx context.Context, plan *types.ProjectReportPlan) error
	DeleteProjectReportPlan(ctx context.Context, planID int64) error
	ListProjectReportPlansByProjectID(ctx context.Context, projectID int64) ([]*types.ProjectReportPlan, error)
	// ListProjectReportsByPlanID 返回按某大纲撰写的报告。
	ListProjectReportsByPlanID(ctx context.Context, planID int64) ([]*types.ProjectReport, error)

	// Literature data access methods.
	CreateLiterature(ctx context.Context, literature *types.Literature) error
	GetLiteratureByID(ctx context.Context, literatureID int64) (*types.Literature, error)
	UpdateLiterature(ctx context.Context, literature *types.Literature) error
	DeleteLiterature(ctx context.Context, literatureID int64) error
	ListLiteratureByProjectID(ctx context.Context, projectID int64) ([]*types.Literature, error)
	PageLiteratureByProjectID(ctx context.Context, pagination *types.Pagination, projectID int64) ([]*types.Literature, int64, error)
	AddProjectLiterature(ctx context.Context, pl *types.ProjectLiterature) error
	ExistsProjectLiterature(ctx context.Context, projectID int64, literatureID int64) (bool, error)
	DeleteProjectLiterature(ctx context.Context, projectID int64, literatureID int64) error
	DeleteProjectLiteratureByLiteratureID(ctx context.Context, literatureID int64) error
	// GetLiteratureOwnerProjectID 返回文献第一条项目绑定所属的 project id。
	GetLiteratureOwnerProjectID(ctx context.Context, literatureID int64) (int64, error)
	PageLiteraturePool(ctx context.Context, pagination *types.Pagination, projectID int64) ([]*types.LiteraturePoolItem, int64, error)
}
