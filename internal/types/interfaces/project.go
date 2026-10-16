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
	UpdateProjectSharing(ctx context.Context, userID int64, projectID int64, enabled bool) (string, error)
	ActivateUserProject(ctx context.Context, userID int64, projectID int64) error
	DeleteUserProject(ctx context.Context, userID int64, projectID int64) error
	CreateDefaultProjectForUser(ctx context.Context, userID int64, username string) error
	CreateProjectForUser(ctx context.Context, userID int64, project *types.Project) (*types.Project, error)
	AddProjectReport(ctx context.Context, userID int64, report *types.ProjectReport) error
	UpdateProjectReport(ctx context.Context, userID int64, report *types.ProjectReport) error
	DeleteProjectReport(ctx context.Context, userID int64, reportID int64) error
	ListProjectReportByProjectID(ctx context.Context, userID int64, projectID int64) ([]*types.ProjectReport, error)
	PageProjectReportByProjectID(ctx context.Context, userID int64, projectID int64, pagination *types.Pagination) ([]*types.ProjectReport, int64, error)
	GetProjectReportDetailByID(ctx context.Context, userID int64, reportID int64) (*types.ProjectReport, error)
	GetProjectReportByID(ctx context.Context, reportID int64) (*types.ProjectReport, error)
	// ProjectReportItem business capabilities.
	// ListProjectReportItemsByReportID 返回报告下全部条目，按树的前序遍历展开（与前端 tree 顺序一致）。
	ListProjectReportItemsByReportID(ctx context.Context, userID int64, reportID int64) ([]*types.ProjectReportItem, error)
	AddProjectReportItem(ctx context.Context, userID int64, item *types.ProjectReportItem) error
	UpdateProjectReportItem(ctx context.Context, userID int64, item *types.ProjectReportItem) error
	DeleteProjectReportItem(ctx context.Context, userID int64, itemID int64) error
	// ReorderProjectReportItems 按拖拽结果批量更新条目的父节点与同级顺序。
	ReorderProjectReportItems(ctx context.Context, userID int64, reportID int64, orders []types.ProjectReportItemOrder) error
	GetProjectReportItemDetailByID(ctx context.Context, userID int64, itemID int64) (*types.ProjectReportItem, error)
	// GetProjectReportItemContent 返回指定条目（入参为 ProjectReportItem ID）渲染后的内容片段。
	GetProjectReportItemContent(ctx context.Context, itemID int64) (*types.ProjectReportItemContent, error)
	// GetProjectReportContent 汇总报告下所有条目，按顺序拼接成正文，仅返回拼接后的内容。
	GetProjectReportContent(ctx context.Context, reportID int64) (string, error)
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
	ExistsUserProject(ctx context.Context, userID int64, projectID int64) (bool, error)
	GetUserProject(ctx context.Context, userID int64, projectID int64) (*types.UserProject, error)
	GetUserProjectByShareCode(ctx context.Context, shareCode string) (*types.UserProject, error)
	UpdateProjectSharing(ctx context.Context, userID int64, projectID int64, enabled bool, shareCode string) error
	DeleteUserProject(ctx context.Context, userID int64, projectID int64) error
	ActivateUserProject(ctx context.Context, userID int64, projectID int64) error
	AddProjectReport(ctx context.Context, report *types.ProjectReport) error
	GetProjectReportByID(ctx context.Context, reportID int64) (*types.ProjectReport, error)
	UpdateProjectReport(ctx context.Context, report *types.ProjectReport) error
	DeleteProjectReport(ctx context.Context, projectID int64, reportID int64) error
	ListProjectReportByProjectID(ctx context.Context, projectID int64) ([]*types.ProjectReport, error)
	PageProjectReportByProjectID(ctx context.Context, pagination *types.Pagination, projectID int64) ([]*types.ProjectReport, int64, error)
	// ProjectReportItem data access methods.
	AddProjectReportItem(ctx context.Context, item *types.ProjectReportItem) error
	GetProjectReportItemByID(ctx context.Context, itemID int64) (*types.ProjectReportItem, error)
	UpdateProjectReportItem(ctx context.Context, item *types.ProjectReportItem) error
	DeleteProjectReportItem(ctx context.Context, itemID int64) error
	DeleteProjectReportItemsByIDs(ctx context.Context, itemIDs []int64) error
	DeleteProjectReportItemsByReportID(ctx context.Context, reportID int64) error
	// ListProjectReportItemsByReportID 按全局 sort_order 返回原始条目（未按树展开），
	// 需要 tree 顺序时由 service 层调用 flattenProjectReportItems 重排。
	ListProjectReportItemsByReportID(ctx context.Context, reportID int64) ([]*types.ProjectReportItem, error)
	// ExistsProjectReportItemOwner 判断同一报告下是否已存在相同 owner 引用（owner_type + owner_id）的条目。
	// excludeItemID 为需要排除的条目主键（更新场景排除自身），传 0 表示不排除。
	ExistsProjectReportItemOwner(ctx context.Context, reportID int64, ownerType types.ProjectReportItemOwnerType, ownerID int64, excludeItemID int64) (bool, error)
	ReorderProjectReportItems(ctx context.Context, reportID int64, orders []types.ProjectReportItemOrder) error

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
