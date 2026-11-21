package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// ErrUserProjectActive indicates the user-project binding is still active and cannot be deleted.
var ErrUserProjectActive = errors.New("user project is active")

type projectService struct {
	projectRepo  interfaces.ProjectRepository
	analysisRepo interfaces.AnalysisRepository
	summaryRepo  interfaces.AISummaryRepository
	dataRepo     interfaces.DataRepository
	cfg          *config.Config
}

func NewProjectService(
	projectRepo interfaces.ProjectRepository,
	analysisRepo interfaces.AnalysisRepository,
	summaryRepo interfaces.AISummaryRepository,
	dataRepo interfaces.DataRepository,
	cfg *config.Config,
) interfaces.ProjectService {
	return &projectService{
		projectRepo:  projectRepo,
		analysisRepo: analysisRepo,
		summaryRepo:  summaryRepo,
		dataRepo:     dataRepo,
		cfg:          cfg,
	}
}

func (s *projectService) ListProjectByUserID(ctx context.Context, userID int64) ([]*types.ProjectListItem, error) {
	return s.projectRepo.ListProjectByUserID(ctx, userID)
}
func (s *projectService) GetActiveProjectByUserID(ctx context.Context, userID int64) (*types.Project, error) {
	return s.projectRepo.GetActiveProjectByUserID(ctx, userID)
}

func (s *projectService) GetActiveProjectDirByUserID(ctx context.Context, userID int64, baseDir string) (*types.Project, string, error) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil, "", fmt.Errorf("storage base dir is empty")
	}

	project, err := s.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return nil, "", err
	}

	return project, utils.GetProjectDir(baseDir, project.ID), nil
}

func (s *projectService) GetProjectByID(ctx context.Context, id int64) (*types.Project, error) {
	return s.projectRepo.GetProjectByID(ctx, id)
}

// ListProjectUserIDs 返回项目下的全部用户 ID，供「按项目共享」的资源鉴权使用。
func (s *projectService) ListProjectUserIDs(ctx context.Context, projectID int64) ([]int64, error) {
	return s.projectRepo.ListUserIDsByProjectID(ctx, projectID)
}

func (s *projectService) AddUserProject(ctx context.Context, userID int64, projectID int64) error {
	exists, err := s.projectRepo.ExistsUserProject(ctx, userID, projectID)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("user already has access to this project")
	}
	return s.projectRepo.AddUserProject(ctx, &types.UserProject{
		UserID:    userID,
		ProjectID: projectID,
		CreatedAt: time.Now(),
	})
}

func (s *projectService) AddUserProjectByShareCode(ctx context.Context, userID int64, shareCode string) error {
	shareCode = strings.TrimSpace(shareCode)
	if shareCode == "" {
		return errors.New("share code is empty")
	}

	owner, err := s.projectRepo.GetUserProjectByShareCode(ctx, shareCode)
	if err != nil {
		return err
	}
	if !owner.ShareEnabled {
		return errors.New("project sharing is disabled")
	}

	exists, err := s.projectRepo.ExistsUserProject(ctx, userID, owner.ProjectID)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("user already has access to this project")
	}

	return s.projectRepo.AddUserProject(ctx, &types.UserProject{
		UserID:    userID,
		ProjectID: owner.ProjectID,
		CreatedAt: time.Now(),
	})
}

func (s *projectService) UpdateProjectSharing(ctx context.Context, userID int64, projectID int64, enabled bool) (string, error) {
	bound, err := s.projectRepo.ExistsUserProject(ctx, userID, projectID)
	if err != nil {
		return "", err
	}
	if !bound {
		return "", gorm.ErrRecordNotFound
	}

	shareCode := ""
	if enabled {
		shareCode = uuid.New().String()
	}

	if err := s.projectRepo.UpdateProjectSharing(ctx, userID, projectID, enabled, shareCode); err != nil {
		return "", err
	}

	return shareCode, nil
}

func (s *projectService) ActivateUserProject(ctx context.Context, userID int64, projectID int64) error {
	return s.projectRepo.ActivateUserProject(ctx, userID, projectID)
}

func (s *projectService) DeleteUserProject(ctx context.Context, userID int64, projectID int64) error {
	up, err := s.projectRepo.GetUserProject(ctx, userID, projectID)
	if err != nil {
		return err
	}
	if up.IsActive {
		return ErrUserProjectActive
	}
	return s.projectRepo.DeleteUserProject(ctx, userID, projectID)
}

// GetActiveUserMenuConfig 返回用户在当前激活项目下的菜单配置。
// 菜单配置是项目维度的：没有激活项目时返回空配置，前端按默认行为渲染。
func (s *projectService) GetActiveUserMenuConfig(ctx context.Context, userID int64) (types.UserMenuConfig, error) {
	up, err := s.projectRepo.GetActiveUserProjectByUserID(ctx, userID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return types.UserMenuConfig{}, nil
		}
		return types.UserMenuConfig{}, err
	}
	return up.MenuConfig, nil
}

// UpdateActiveUserMenuConfig 整体覆盖用户在当前激活项目下的菜单配置。
func (s *projectService) UpdateActiveUserMenuConfig(ctx context.Context, userID int64, config types.UserMenuConfig) error {
	up, err := s.projectRepo.GetActiveUserProjectByUserID(ctx, userID)
	if err != nil {
		return err
	}
	return s.projectRepo.UpdateUserProjectMenuConfig(ctx, userID, up.ProjectID, config)
}

func (s *projectService) CreateDefaultProjectForUser(ctx context.Context, userID int64, username string) error {
	projectName := fmt.Sprintf("%s's Project", username)

	project := &types.Project{
		ID:          utils.GenerateID(),
		ProjectName: projectName,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := s.projectRepo.CreateProject(ctx, project); err != nil {
		return fmt.Errorf("failed to create default project: %w", err)
	}

	if err := s.projectRepo.AddUserProject(ctx, &types.UserProject{
		UserID:    userID,
		ProjectID: project.ID,
		IsActive:  true,
		CreatedAt: time.Now(),
	}); err != nil {
		return fmt.Errorf("failed to link user to project: %w", err)
	}

	// Create the default dataset and bind it to the new project so the user can
	// use the data features right away.
	if err := s.createDefaultDatasetForProject(ctx, project.ID, username); err != nil {
		return err
	}

	return nil
}

// createDefaultDatasetForProject creates the initial dataset of a project and
// binds it through go_project_dataset. The ProjectDataset PK is left zero so
// that BeforeCreate generates it.
func (s *projectService) createDefaultDatasetForProject(ctx context.Context, projectID int64, username string) error {
	if s.dataRepo == nil {
		return nil
	}

	dataset := &types.Dataset{
		ID:          utils.GenerateID(),
		DatasetName: fmt.Sprintf("%s's Dataset", username),
		Description: "Default dataset",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	return s.dataRepo.WithTransaction(ctx, func(tx interfaces.DataRepository) error {
		if err := tx.CreateDataset(ctx, dataset); err != nil {
			return fmt.Errorf("failed to create default dataset: %w", err)
		}

		if err := tx.CreateProjectDataset(ctx, &types.ProjectDataset{
			ProjectID: projectID,
			DatasetID: dataset.ID,
			CreatedAt: time.Now(),
		}); err != nil {
			return fmt.Errorf("failed to bind default dataset to project: %w", err)
		}

		return nil
	})
}

func (s *projectService) CreateProjectForUser(ctx context.Context, userID int64, project *types.Project) (*types.Project, error) {
	if project == nil {
		return nil, errors.New("project is nil")
	}

	if strings.TrimSpace(project.ProjectName) == "" {
		return nil, errors.New("project name is empty")
	}

	if project.ID == 0 {
		project.ID = utils.GenerateID()
	}

	now := time.Now()
	project.CreatedAt = now
	project.UpdatedAt = now

	if err := s.projectRepo.CreateProject(ctx, project); err != nil {
		return nil, fmt.Errorf("failed to create project: %w", err)
	}

	if err := s.projectRepo.AddUserProject(ctx, &types.UserProject{
		UserID:    userID,
		ProjectID: project.ID,
		CreatedAt: now,
	}); err != nil {
		return nil, fmt.Errorf("failed to link user to project: %w", err)
	}

	// if err := s.projectRepo.ActivateUserProject(ctx, userID, project.ProjectID); err != nil {
	// 	return nil, fmt.Errorf("failed to activate project: %w", err)
	// }

	return project, nil
}

func (s *projectService) AddProjectReport(ctx context.Context, userID int64, report *types.ProjectReport) error {
	if report == nil {
		return errors.New("project report is nil")
	}
	bound, err := s.projectRepo.ExistsUserProject(ctx, userID, report.ProjectID)
	if err != nil {
		return err
	}
	if !bound {
		return gorm.ErrRecordNotFound
	}

	if report.ID == 0 {
		report.ID = utils.GenerateID()
	}
	now := time.Now()
	if report.CreatedAt.IsZero() {
		report.CreatedAt = now
	}
	report.UpdatedAt = now

	if err := s.projectRepo.AddProjectReport(ctx, report); err != nil {
		return err
	}

	// 新增报告时同步创建报告工作目录与其自身 markdown 文件，
	// 保证前端可以立即打开「报告文件」查看/编辑页；已存在时不覆盖。
	s.ensureProjectReportFile(report)

	return nil
}

// ensureProjectReportFile 确保报告工作目录与自身 markdown 文件存在。
// 目录或文件已存在时不覆盖，任何失败都视为 best-effort 忽略。
func (s *projectService) ensureProjectReportFile(report *types.ProjectReport) {
	dir, file, err := s.projectReportPaths(report)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}

	if _, statErr := os.Stat(file); statErr == nil {
		return
	}
	title := strings.TrimSpace(report.Title)
	if title == "" {
		title = "Report"
	}
	_ = os.WriteFile(file, []byte(fmt.Sprintf("# %s\n", title)), 0o644)
}

func (s *projectService) UpdateProjectReport(ctx context.Context, userID int64, report *types.ProjectReport) error {
	if report == nil {
		return errors.New("project report is nil")
	}
	bound, err := s.projectRepo.ExistsUserProject(ctx, userID, report.ProjectID)
	if err != nil {
		return err
	}
	if !bound {
		return gorm.ErrRecordNotFound
	}

	stored, err := s.projectRepo.GetProjectReportByID(ctx, report.ID)
	if err != nil {
		return err
	}
	if stored.ProjectID != report.ProjectID {
		return gorm.ErrRecordNotFound
	}

	stored.Title = report.Title
	stored.UpdatedAt = time.Now()
	return s.projectRepo.UpdateProjectReport(ctx, stored)
}

func (s *projectService) DeleteProjectReport(ctx context.Context, userID int64, reportID int64) error {
	report, err := s.loadOwnedProjectReport(ctx, userID, reportID)
	if err != nil {
		return err
	}

	// 先删除报告下所有条目，再删除报告本身。
	if err := s.projectRepo.DeleteProjectReport(ctx, report.ProjectID, reportID); err != nil {
		return err
	}

	// 删除报告工作目录（含报告自身文件 output.md）；best-effort，失败不影响删除结果。
	if dir, dirErr := s.projectReportDir(report); dirErr == nil {
		_ = os.RemoveAll(dir)
	}
	return nil
}

func (s *projectService) ListProjectReportByProjectID(ctx context.Context, userID int64, projectID int64) ([]*types.ProjectReport, error) {
	bound, err := s.projectRepo.ExistsUserProject(ctx, userID, projectID)
	if err != nil {
		return nil, err
	}
	if !bound {
		return nil, gorm.ErrRecordNotFound
	}

	return s.projectRepo.ListProjectReportByProjectID(ctx, projectID)
}

func (s *projectService) PageProjectReportByProjectID(ctx context.Context, userID int64, projectID int64, pagination *types.Pagination) ([]*types.ProjectReport, int64, error) {
	bound, err := s.projectRepo.ExistsUserProject(ctx, userID, projectID)
	if err != nil {
		return nil, 0, err
	}
	if !bound {
		return nil, 0, gorm.ErrRecordNotFound
	}

	return s.projectRepo.PageProjectReportByProjectID(ctx, pagination, projectID)
}

func (s *projectService) GetProjectReportDetailByID(ctx context.Context, userID int64, reportID int64) (*types.ProjectReport, error) {
	return s.loadOwnedProjectReport(ctx, userID, reportID)
}

func (s *projectService) GetProjectReportByID(ctx context.Context, reportID int64) (*types.ProjectReport, error) {
	return s.projectRepo.GetProjectReportByID(ctx, reportID)
}

// loadOwnedProjectReport 读取报告并校验其所属项目已绑定到当前用户。
func (s *projectService) loadOwnedProjectReport(ctx context.Context, userID int64, reportID int64) (*types.ProjectReport, error) {
	report, err := s.projectRepo.GetProjectReportByID(ctx, reportID)
	if err != nil {
		return nil, err
	}

	bound, err := s.projectRepo.ExistsUserProject(ctx, userID, report.ProjectID)
	if err != nil {
		return nil, err
	}
	if !bound {
		return nil, gorm.ErrRecordNotFound
	}

	return report, nil
}

// readMarkdownFile 读取节点输出目录下的 output.md，文件不存在时返回空串。
func readMarkdownFile(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, types.DefaultProjectReportFilename))
	if err != nil {
		return ""
	}
	return string(data)
}

func (s *projectService) projectReportDir(report *types.ProjectReport) (string, error) {
	dir, _, err := s.projectReportPaths(report)
	return dir, err
}

// projectReportPaths 返回报告工作目录与其正文文件路径。
//
// 两者由 ProjectReport.WorkspaceDir / OutputFile 统一推导（单一事实源），
// 调用方不再各自拼 utils.GetProjectReportDir + output.md。
func (s *projectService) projectReportPaths(report *types.ProjectReport) (string, string, error) {
	baseDir := s.storageBaseDir()
	if baseDir == "" {
		return "", "", errors.New("storage base dir is empty or not configured")
	}
	return report.WorkspaceDir(baseDir), report.OutputFile(baseDir), nil
}

// projectReportDirByID 按报告主键解析报告工作目录（内部先做归属校验）。
// 供报告目录的 git 能力（diff / 提交 / 撤回）复用，避免每个入口重复校验与拼路径。
func (s *projectService) projectReportDirByID(ctx context.Context, userID int64, reportID int64) (*types.ProjectReport, string, error) {
	report, err := s.loadOwnedProjectReport(ctx, userID, reportID)
	if err != nil {
		return nil, "", err
	}
	dir, err := s.projectReportDir(report)
	if err != nil {
		return nil, "", err
	}
	return report, dir, nil
}

// GetProjectReportFile 读取报告工作目录下的自身 markdown 文件（output.md）。
// 文件不存在时返回空字符串（前端按空内容展示，而非报错）。
func (s *projectService) GetProjectReportFile(ctx context.Context, userID int64, reportID int64) (string, error) {
	report, err := s.loadOwnedProjectReport(ctx, userID, reportID)
	if err != nil {
		return "", err
	}

	dir, file, err := s.projectReportPaths(report)
	if err != nil {
		return "", err
	}

	// 报告文件所在目录从第一次读取起就纳入版本管理（best-effort：失败不影响读取）。
	s.ensureProjectReportGitRepo(dir)

	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// UpdateProjectReportFile 覆写报告工作目录下的自身 markdown 文件（output.md）。
// 目录不存在时自动创建；目录尚不是 git 仓库时就地初始化（不自动提交，
// 由前端显式触发「提交」），因此保存后报告目录始终处于「有未提交改动」状态。
func (s *projectService) UpdateProjectReportFile(ctx context.Context, userID int64, reportID int64, content string) error {
	report, err := s.loadOwnedProjectReport(ctx, userID, reportID)
	if err != nil {
		return err
	}

	dir, file, err := s.projectReportPaths(report)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		return err
	}

	s.ensureProjectReportGitRepo(dir)
	return nil
}

// ensureProjectReportGitRepo 确保报告工作目录是一个 git 仓库，并在「本次刚初始化」时
// 补一次基线提交：
//
//   - 目录本来不是仓库（首次保存/首次读取，或历史遗留的普通文件目录）：初始化为仓库后
//     立即把当前内容提交为根提交。这样前端首次就能看到「已初始化 + 干净」的状态，
//     后续编辑才会呈现为「未提交改动」；否则首次初始化会把既有文件全当成未跟踪改动，
//     「撤回」也就没有可回退的基线。
//   - 目录已经是仓库：no-op，不做任何提交。
//
// 全过程 best-effort：报告文件的读写不应因为 git 操作失败而失败。
func (s *projectService) ensureProjectReportGitRepo(dir string) {
	repo, created, err := utils.EnsureReportWorktreeGitRepo(dir)
	if err != nil || !created {
		return
	}

	name, email := config.ResolveGitIdentity(s.cfg)
	_, _ = utils.CommitAll(repo, "init project report", utils.GitIdentity{Name: name, Email: email})
}

// ---------- ProjectReport git (报告工作目录的版本管理) ----------

// GetProjectReportGitDiff 返回报告工作目录的本地变化：工作区相对本地 HEAD 的未提交改动。
// 报告没有发布目标，因此不返回「未发布提交」段落。
func (s *projectService) GetProjectReportGitDiff(ctx context.Context, userID int64, reportID int64) (*types.ProjectReportGitDiff, error) {
	report, dir, err := s.projectReportDirByID(ctx, userID, reportID)
	if err != nil {
		return nil, err
	}

	// 读取时同样保证目录已初始化，否则「首次打开报告文件」会看到未初始化状态而无法查看变化。
	s.ensureProjectReportGitRepo(dir)

	diff := utils.ReadGitDiff(dir, "")
	state := utils.ReadReportWorktreeGitState(dir)

	return &types.ProjectReportGitDiff{
		ReportID:    strconv.FormatInt(report.ID, 10),
		ProjectID:   strconv.FormatInt(report.ProjectID, 10),
		RepoDir:     diff.RepoDir,
		Initialized: diff.Initialized,
		HeadCommit:  diff.HeadCommit,
		HasChanges:  diff.HasChanges,
		Worktree:    diff.Worktree,
		GitState:    state,
	}, nil
}

// CommitProjectReport 把报告工作目录的所有改动提交为一个 commit（等同 git add -A + commit）。
// 目录尚不是仓库时先初始化；没有改动时不产生空提交（Committed=false）。
func (s *projectService) CommitProjectReport(ctx context.Context, userID int64, reportID int64, message string) (*types.ProjectReportCommitResult, error) {
	report, dir, err := s.projectReportDirByID(ctx, userID, reportID)
	if err != nil {
		return nil, err
	}

	repo, _, err := utils.EnsureReportWorktreeGitRepo(dir)
	if err != nil {
		return nil, err
	}

	name, email := config.ResolveGitIdentity(s.cfg)
	message = strings.TrimSpace(message)
	if message == "" {
		message = fmt.Sprintf("update project report %d", report.ID)
	}

	committed, err := utils.CommitAll(repo, message, utils.GitIdentity{Name: name, Email: email})
	if err != nil {
		return nil, err
	}

	return &types.ProjectReportCommitResult{
		ReportID:      strconv.FormatInt(report.ID, 10),
		ProjectID:     strconv.FormatInt(report.ProjectID, 10),
		Committed:     committed,
		Commit:        strings.TrimSpace(utils.ReadReportWorktreeGitState(dir).Commit),
		CommitMessage: message,
	}, nil
}

// RevertProjectReportFile 撤回报告工作目录里单个文件的未提交改动（恢复为本地 HEAD 版本）。
// relPath 是相对报告目录的路径（当前只有 output.md）。
func (s *projectService) RevertProjectReportFile(ctx context.Context, userID int64, reportID int64, relPath string) error {
	_, dir, err := s.projectReportDirByID(ctx, userID, reportID)
	if err != nil {
		return err
	}
	return utils.RevertWorktreeFile(dir, relPath)
}

// storageBaseDir 返回去除首尾空白后的 storage.base_dir，未配置时返回空串。
func (s *projectService) storageBaseDir() string {
	if s.cfg == nil || s.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(s.cfg.Storage.BaseDir)
}

// ---------- Literature ----------

func (s *projectService) AddLiterature(ctx context.Context, userID int64, literature *types.Literature, content string) (*types.Literature, string, error) {
	if literature == nil {
		return nil, "", errors.New("literature is nil")
	}

	project, err := s.projectRepo.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return nil, "", err
	}

	if literature.ID == 0 {
		literature.ID = utils.GenerateID()
	}
	now := time.Now()
	literature.CreatedAt = now
	literature.UpdatedAt = now

	if err := s.projectRepo.CreateLiterature(ctx, literature); err != nil {
		return nil, "", err
	}

	if err := s.projectRepo.AddProjectLiterature(ctx, &types.ProjectLiterature{
		ProjectID:    project.ID,
		LiteratureID: literature.ID,
		CreatedAt:    now,
	}); err != nil {
		return nil, "", err
	}

	if err := s.writeLiteratureFile(project.ID, literature.ID, content); err != nil {
		return nil, "", err
	}

	return literature, content, nil
}

func (s *projectService) UpdateLiterature(ctx context.Context, userID int64, literature *types.Literature, content string) error {
	if literature == nil {
		return errors.New("literature is nil")
	}

	if err := s.requireLiteratureAccess(ctx, userID, literature.ID); err != nil {
		return err
	}

	stored, err := s.projectRepo.GetLiteratureByID(ctx, literature.ID)
	if err != nil {
		return err
	}

	stored.Title = literature.Title
	stored.UpdatedAt = time.Now()

	if err := s.projectRepo.UpdateLiterature(ctx, stored); err != nil {
		return err
	}

	projectID, err := s.projectRepo.GetLiteratureOwnerProjectID(ctx, literature.ID)
	if err != nil {
		return err
	}
	return s.writeLiteratureFile(projectID, literature.ID, content)
}

func (s *projectService) DeleteLiterature(ctx context.Context, userID int64, literatureID int64) error {
	if _, err := s.projectRepo.GetLiteratureByID(ctx, literatureID); err != nil {
		return err
	}

	if err := s.requireLiteratureAccess(ctx, userID, literatureID); err != nil {
		return err
	}

	// Best-effort removal of the full-text file and its directory.
	_ = s.deleteLiteratureFile(ctx, literatureID)

	// Remove the association for every project that references this literature.
	if err := s.projectRepo.DeleteProjectLiteratureByLiteratureID(ctx, literatureID); err != nil {
		return err
	}

	return s.projectRepo.DeleteLiterature(ctx, literatureID)
}

// GetLiteratureDetailByID 返回文献元信息及其全文文件内容。
func (s *projectService) GetLiteratureDetailByID(ctx context.Context, userID int64, literatureID int64) (*types.LiteratureDetail, error) {
	literature, err := s.projectRepo.GetLiteratureByID(ctx, literatureID)
	if err != nil {
		return nil, err
	}

	if err := s.requireLiteratureAccess(ctx, userID, literatureID); err != nil {
		return nil, err
	}

	projectID, err := s.projectRepo.GetLiteratureOwnerProjectID(ctx, literatureID)
	if err != nil {
		return nil, err
	}

	content, err := s.readLiteratureFile(projectID, literatureID)
	if err != nil {
		if os.IsNotExist(err) {
			return &types.LiteratureDetail{Literature: literature}, nil
		}
		return nil, err
	}

	return &types.LiteratureDetail{Literature: literature, Content: content}, nil
}

func (s *projectService) ListLiteratureByProjectID(ctx context.Context, userID int64) ([]*types.Literature, error) {
	project, err := s.projectRepo.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}

	return s.projectRepo.ListLiteratureByProjectID(ctx, project.ID)
}

func (s *projectService) PageLiteratureByProjectID(ctx context.Context, userID int64, pagination *types.Pagination) ([]*types.Literature, int64, error) {
	project, err := s.projectRepo.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return nil, 0, err
	}

	return s.projectRepo.PageLiteratureByProjectID(ctx, pagination, project.ID)
}

func (s *projectService) BindLiteratureToProject(ctx context.Context, userID int64, literatureID int64) error {
	project, err := s.projectRepo.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return err
	}

	literature, err := s.projectRepo.GetLiteratureByID(ctx, literatureID)
	if err != nil {
		return err
	}

	exists, err := s.projectRepo.ExistsProjectLiterature(ctx, project.ID, literatureID)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("literature is already bound to the active project")
	}

	return s.projectRepo.AddProjectLiterature(ctx, &types.ProjectLiterature{
		ProjectID:    project.ID,
		LiteratureID: literature.ID,
		CreatedAt:    time.Now(),
	})
}

func (s *projectService) UnbindLiteratureFromProject(ctx context.Context, userID int64, literatureID int64) error {
	project, err := s.projectRepo.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return err
	}

	exists, err := s.projectRepo.ExistsProjectLiterature(ctx, project.ID, literatureID)
	if err != nil {
		return err
	}
	if !exists {
		return gorm.ErrRecordNotFound
	}

	return s.projectRepo.DeleteProjectLiterature(ctx, project.ID, literatureID)
}

func (s *projectService) PageLiteraturePool(ctx context.Context, userID int64, pagination *types.Pagination) ([]*types.LiteraturePoolItem, int64, error) {
	project, err := s.projectRepo.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return nil, 0, err
	}

	return s.projectRepo.PageLiteraturePool(ctx, pagination, project.ID)
}

// requireLiteratureAccess verifies the literature is bound to the current user's
// active project. It returns gorm.ErrRecordNotFound when there is no active
// project or the binding does not exist.
func (s *projectService) requireLiteratureAccess(ctx context.Context, userID int64, literatureID int64) error {
	project, err := s.projectRepo.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return err
	}

	bound, err := s.projectRepo.ExistsProjectLiterature(ctx, project.ID, literatureID)
	if err != nil {
		return err
	}
	if !bound {
		return gorm.ErrRecordNotFound
	}

	return nil
}

func (s *projectService) literatureFilePath(projectID int64, literatureID int64) (string, error) {
	if projectID == 0 {
		return "", errors.New("literature owner project id is empty")
	}

	if s.cfg == nil || s.cfg.Storage == nil {
		return "", errors.New("storage config is missing")
	}

	baseDir := strings.TrimSpace(s.cfg.Storage.BaseDir)
	if baseDir == "" {
		return "", errors.New("storage base dir is empty")
	}

	return utils.GetProjectLiteratureFile(baseDir, projectID, strconv.FormatInt(literatureID, 10)), nil
}

func (s *projectService) writeLiteratureFile(projectID int64, literatureID int64, content string) error {
	filePath, err := s.literatureFilePath(projectID, literatureID)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}

	return os.WriteFile(filePath, []byte(content), 0o644)
}

func (s *projectService) readLiteratureFile(projectID int64, literatureID int64) (string, error) {
	filePath, err := s.literatureFilePath(projectID, literatureID)
	if err != nil {
		return "", err
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (s *projectService) deleteLiteratureFile(ctx context.Context, literatureID int64) error {
	projectID, err := s.projectRepo.GetLiteratureOwnerProjectID(ctx, literatureID)
	if err != nil {
		return err
	}

	dir, err := s.literatureFilePath(projectID, literatureID)
	if err != nil {
		return err
	}
	dir = filepath.Dir(dir)

	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
