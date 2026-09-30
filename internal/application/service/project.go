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
	cfg          *config.Config
}

func NewProjectService(
	projectRepo interfaces.ProjectRepository,
	analysisRepo interfaces.AnalysisRepository,
	summaryRepo interfaces.AISummaryRepository,
	cfg *config.Config,
) interfaces.ProjectService {
	return &projectService{
		projectRepo:  projectRepo,
		analysisRepo: analysisRepo,
		summaryRepo:  summaryRepo,
		cfg:          cfg,
	}
}

func (s *projectService) ListProjectByUserID(ctx context.Context, userID string) ([]*types.ProjectListItem, error) {
	return s.projectRepo.ListProjectByUserID(ctx, userID)
}
func (s *projectService) GetActiveProjectByUserID(ctx context.Context, userID string) (*types.Project, error) {
	return s.projectRepo.GetActiveProjectByUserID(ctx, userID)
}

func (s *projectService) GetActiveProjectDirByUserID(ctx context.Context, userID, baseDir string) (*types.Project, string, error) {
	baseDir = strings.TrimSpace(baseDir)
	if baseDir == "" {
		return nil, "", fmt.Errorf("storage base dir is empty")
	}

	project, err := s.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return nil, "", err
	}

	return project, utils.GetProjectDir(baseDir, project.ProjectID), nil
}

func (s *projectService) GetProjectByID(ctx context.Context, id int64) (*types.Project, error) {
	return s.projectRepo.GetProjectByID(ctx, id)
}

func (s *projectService) AddUserProject(ctx context.Context, userID, projectID string) error {
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

func (s *projectService) AddUserProjectByShareCode(ctx context.Context, userID, shareCode string) error {
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

func (s *projectService) UpdateProjectSharing(ctx context.Context, userID, projectID string, enabled bool) (string, error) {
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

func (s *projectService) ActivateUserProject(ctx context.Context, userID, projectID string) error {
	return s.projectRepo.ActivateUserProject(ctx, userID, projectID)
}

func (s *projectService) DeleteUserProject(ctx context.Context, userID, projectID string) error {
	up, err := s.projectRepo.GetUserProject(ctx, userID, projectID)
	if err != nil {
		return err
	}
	if up.IsActive {
		return ErrUserProjectActive
	}
	return s.projectRepo.DeleteUserProject(ctx, userID, projectID)
}

func (s *projectService) CreateDefaultProjectForUser(ctx context.Context, userID, username string) error {
	projectID := uuid.New().String()
	projectName := fmt.Sprintf("%s's Project", username)

	project := &types.Project{
		ID:          utils.GenerateID(),
		ProjectID:   projectID,
		ProjectName: projectName,
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}

	if err := s.projectRepo.CreateProject(ctx, project); err != nil {
		return fmt.Errorf("failed to create default project: %w", err)
	}

	if err := s.projectRepo.AddUserProject(ctx, &types.UserProject{
		UserID:    userID,
		ProjectID: projectID,
		IsActive:  true,
		CreatedAt: time.Now(),
	}); err != nil {
		return fmt.Errorf("failed to link user to project: %w", err)
	}

	return nil
}

func (s *projectService) CreateProjectForUser(ctx context.Context, userID string, project *types.Project) (*types.Project, error) {
	if project == nil {
		return nil, errors.New("project is nil")
	}

	if strings.TrimSpace(project.ProjectName) == "" {
		return nil, errors.New("project name is empty")
	}

	if project.ProjectID == "" {
		project.ProjectID = uuid.New().String()
	}

	now := time.Now()
	project.CreatedAt = now
	project.UpdatedAt = now

	if err := s.projectRepo.CreateProject(ctx, project); err != nil {
		return nil, fmt.Errorf("failed to create project: %w", err)
	}

	if err := s.projectRepo.AddUserProject(ctx, &types.UserProject{
		UserID:    userID,
		ProjectID: project.ProjectID,
		CreatedAt: now,
	}); err != nil {
		return nil, fmt.Errorf("failed to link user to project: %w", err)
	}

	// if err := s.projectRepo.ActivateUserProject(ctx, userID, project.ProjectID); err != nil {
	// 	return nil, fmt.Errorf("failed to activate project: %w", err)
	// }

	return project, nil
}

func (s *projectService) AddProjectReport(ctx context.Context, userID string, report *types.ProjectReport) error {
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

	return s.projectRepo.AddProjectReport(ctx, report)
}

func (s *projectService) UpdateProjectReport(ctx context.Context, userID string, report *types.ProjectReport) error {
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

func (s *projectService) DeleteProjectReport(ctx context.Context, userID string, reportID int64) error {
	report, err := s.loadOwnedProjectReport(ctx, userID, reportID)
	if err != nil {
		return err
	}

	// 先删除报告下所有条目，再删除报告本身。
	if err := s.projectRepo.DeleteProjectReportItemsByReportID(ctx, report.ID); err != nil {
		return err
	}

	if err := s.projectRepo.DeleteProjectReport(ctx, report.ProjectID, reportID); err != nil {
		return err
	}

	// Best-effort cleanup of the per-report directory.
	if dir, dirErr := s.projectReportDir(report); dirErr == nil {
		_ = os.RemoveAll(dir)
	}
	return nil
}

func (s *projectService) ListProjectReportByProjectID(ctx context.Context, userID, projectID string) ([]*types.ProjectReport, error) {
	bound, err := s.projectRepo.ExistsUserProject(ctx, userID, projectID)
	if err != nil {
		return nil, err
	}
	if !bound {
		return nil, gorm.ErrRecordNotFound
	}

	return s.projectRepo.ListProjectReportByProjectID(ctx, projectID)
}

func (s *projectService) PageProjectReportByProjectID(ctx context.Context, userID, projectID string, pagination *types.Pagination) ([]*types.ProjectReport, int64, error) {
	bound, err := s.projectRepo.ExistsUserProject(ctx, userID, projectID)
	if err != nil {
		return nil, 0, err
	}
	if !bound {
		return nil, 0, gorm.ErrRecordNotFound
	}

	return s.projectRepo.PageProjectReportByProjectID(ctx, pagination, projectID)
}

func (s *projectService) GetProjectReportDetailByID(ctx context.Context, userID string, reportID int64) (*types.ProjectReport, error) {
	return s.loadOwnedProjectReport(ctx, userID, reportID)
}

func (s *projectService) GetProjectReportByID(ctx context.Context, reportID int64) (*types.ProjectReport, error) {
	return s.projectRepo.GetProjectReportByID(ctx, reportID)
}

// ---------- ProjectReportItem ----------

// loadOwnedProjectReport 读取报告并校验其所属项目已绑定到当前用户。
func (s *projectService) loadOwnedProjectReport(ctx context.Context, userID string, reportID int64) (*types.ProjectReport, error) {
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

// loadOwnedProjectReportItem 读取条目并校验其所属报告归属当前用户。
func (s *projectService) loadOwnedProjectReportItem(ctx context.Context, userID string, itemID int64) (*types.ProjectReportItem, *types.ProjectReport, error) {
	item, err := s.projectRepo.GetProjectReportItemByID(ctx, itemID)
	if err != nil {
		return nil, nil, err
	}

	report, err := s.loadOwnedProjectReport(ctx, userID, item.ProjectReportID)
	if err != nil {
		return nil, nil, err
	}

	return item, report, nil
}

func (s *projectService) ListProjectReportItemsByReportID(ctx context.Context, userID string, reportID int64) ([]*types.ProjectReportItem, error) {
	report, err := s.loadOwnedProjectReport(ctx, userID, reportID)
	if err != nil {
		return nil, err
	}

	items, err := s.projectRepo.ListProjectReportItemsByReportID(ctx, report.ID)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		s.hydrateProjectReportItemTitle(ctx, item)
	}
	return items, nil
}

func (s *projectService) AddProjectReportItem(ctx context.Context, userID string, item *types.ProjectReportItem) error {
	if item == nil {
		return errors.New("project report item is nil")
	}

	if _, err := s.loadOwnedProjectReport(ctx, userID, item.ProjectReportID); err != nil {
		return err
	}

	ownerType, ok := types.NormalizeProjectReportItemOwnerType(string(item.OwnerType))
	if !ok {
		return fmt.Errorf("invalid owner_type: %s", item.OwnerType)
	}
	item.OwnerType = ownerType
	if item.OwnerID <= 0 {
		return errors.New("owner_id is required for project report item")
	}

	if item.ID == 0 {
		item.ID = utils.GenerateID()
	}
	now := time.Now()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now

	if err := s.projectRepo.AddProjectReportItem(ctx, item); err != nil {
		return err
	}
	return nil
}

func (s *projectService) UpdateProjectReportItem(ctx context.Context, userID string, item *types.ProjectReportItem) error {
	if item == nil {
		return errors.New("project report item is nil")
	}

	stored, _, err := s.loadOwnedProjectReportItem(ctx, userID, item.ID)
	if err != nil {
		return err
	}

	stored.SortOrder = item.SortOrder
	if strings.TrimSpace(string(item.OwnerType)) != "" {
		ownerType, ok := types.NormalizeProjectReportItemOwnerType(string(item.OwnerType))
		if !ok {
			return fmt.Errorf("invalid owner_type: %s", item.OwnerType)
		}
		stored.OwnerType = ownerType
		if item.OwnerID > 0 {
			stored.OwnerID = item.OwnerID
		}
	}
	stored.UpdatedAt = time.Now()

	return s.projectRepo.UpdateProjectReportItem(ctx, stored)
}

func (s *projectService) DeleteProjectReportItem(ctx context.Context, userID string, itemID int64) error {
	item, _, err := s.loadOwnedProjectReportItem(ctx, userID, itemID)
	if err != nil {
		return err
	}

	return s.projectRepo.DeleteProjectReportItem(ctx, item.ID)
}

func (s *projectService) GetProjectReportItemDetailByID(ctx context.Context, userID string, itemID int64) (*types.ProjectReportItem, error) {
	item, _, err := s.loadOwnedProjectReportItem(ctx, userID, itemID)
	if err != nil {
		return nil, err
	}

	s.hydrateProjectReportItemTitle(ctx, item)
	return item, nil
}

// GetProjectReportContent 汇总报告下所有条目，按 SortOrder 拼接成正文。
func (s *projectService) GetProjectReportContent(ctx context.Context, userID string, reportID int64) (*types.ProjectReport, []*types.ProjectReportItem, string, error) {
	report, err := s.loadOwnedProjectReport(ctx, userID, reportID)
	if err != nil {
		return nil, nil, "", err
	}

	items, err := s.projectRepo.ListProjectReportItemsByReportID(ctx, report.ID)
	if err != nil {
		return nil, nil, "", err
	}

	var builder strings.Builder
	for _, item := range items {
		s.hydrateProjectReportItemTitle(ctx, item)
		section, err := s.projectReportItemMarkdown(ctx, report, item)
		if err != nil {
			return nil, nil, "", err
		}
		builder.WriteString(section)
	}

	return report, items, builder.String(), nil
}

// GetProjectReportItemContent 返回指定条目（入参为 ProjectReportItem ID）的 markdown 内容。
func (s *projectService) GetProjectReportItemContent(ctx context.Context, userID string, itemID int64) (*types.ProjectReportItem, string, error) {
	item, report, err := s.loadOwnedProjectReportItem(ctx, userID, itemID)
	if err != nil {
		return nil, "", err
	}

	s.hydrateProjectReportItemTitle(ctx, item)
	content, err := s.projectReportItemMarkdown(ctx, report, item)
	if err != nil {
		return nil, "", err
	}

	return item, content, nil
}

// hydrateProjectReportItemTitle 解析条目的展示标题；解析失败时回退到 "owner_type #id"。
func (s *projectService) hydrateProjectReportItemTitle(ctx context.Context, item *types.ProjectReportItem) {
	if item == nil {
		return
	}
	fallback := func() {
		item.Title = fmt.Sprintf("%s #%d", item.OwnerType, item.OwnerID)
	}

	switch item.OwnerType {
	case types.ProjectReportItemOwnerAnalysis:
		if s.analysisRepo == nil {
			fallback()
			return
		}
		analysis, err := s.analysisRepo.GetAnalysisByID(ctx, item.OwnerID)
		if err != nil || strings.TrimSpace(analysis.AnalysisName) == "" {
			fallback()
			return
		}
		item.Title = analysis.AnalysisName
	case types.ProjectReportItemOwnerAnalysisNode:
		if s.analysisRepo == nil {
			fallback()
			return
		}
		node, err := s.analysisRepo.GetAnalysisNodeByID(ctx, item.OwnerID)
		if err != nil || strings.TrimSpace(node.NodeName) == "" {
			fallback()
			return
		}
		item.Title = node.NodeName
	case types.ProjectReportItemOwnerAISummary:
		if s.summaryRepo == nil {
			fallback()
			return
		}
		summary, err := s.summaryRepo.GetAISummaryByID(ctx, item.OwnerID)
		if err != nil || strings.TrimSpace(summary.Title) == "" {
			fallback()
			return
		}
		item.Title = summary.Title
	default:
		fallback()
	}
}

// projectReportItemMarkdown 返回单个条目对应的 markdown 片段。
func (s *projectService) projectReportItemMarkdown(ctx context.Context, _ *types.ProjectReport, item *types.ProjectReportItem) (string, error) {
	switch item.OwnerType {
	case types.ProjectReportItemOwnerAISummary:
		return s.aiSummaryMarkdown(ctx, item)
	case types.ProjectReportItemOwnerAnalysisNode:
		return s.analysisNodeMarkdown(ctx, item)
	case types.ProjectReportItemOwnerAnalysis:
		return s.analysisMarkdown(ctx, item)
	default:
		return "", fmt.Errorf("unsupported project report item owner type: %s", item.OwnerType)
	}
}

func (s *projectService) aiSummaryMarkdown(ctx context.Context, item *types.ProjectReportItem) (string, error) {
	if s.summaryRepo == nil {
		return "", errors.New("ai summary repository is unavailable")
	}
	summary, err := s.summaryRepo.GetAISummaryByID(ctx, item.OwnerID)
	if err != nil {
		return "", err
	}
	return formatMarkdownSection(sectionTitle(item.Title, summary.Title), summary.Content), nil
}

func (s *projectService) analysisNodeMarkdown(ctx context.Context, item *types.ProjectReportItem) (string, error) {
	if s.analysisRepo == nil {
		return "", errors.New("analysis repository is unavailable")
	}
	node, err := s.analysisRepo.GetAnalysisNodeByID(ctx, item.OwnerID)
	if err != nil {
		return "", err
	}

	content := readMarkdownFile(node.OutputDir)
	if strings.TrimSpace(content) == "" {
		content = fmt.Sprintf("> 未找到节点输出文件：%s", filepath.Join(node.OutputDir, types.DefaultProjectReportFilename))
	}
	return formatMarkdownSection(sectionTitle(item.Title, node.NodeName), content), nil
}

func (s *projectService) analysisMarkdown(ctx context.Context, item *types.ProjectReportItem) (string, error) {
	if s.analysisRepo == nil {
		return "", errors.New("analysis repository is unavailable")
	}
	analysis, err := s.analysisRepo.GetAnalysisByID(ctx, item.OwnerID)
	if err != nil {
		return "", err
	}
	nodes, err := s.analysisRepo.ListAnalysisNodesByAnalysisID(ctx, item.OwnerID)
	if err != nil {
		return "", err
	}

	var builder strings.Builder
	builder.WriteString(fmt.Sprintf("# %s\n\n", sectionTitle(item.Title, analysis.AnalysisName)))
	for _, node := range nodes {
		content := readMarkdownFile(node.OutputDir)
		if strings.TrimSpace(content) == "" {
			continue
		}
		builder.WriteString(fmt.Sprintf("## %s\n\n%s\n\n", node.NodeName, content))
	}
	return builder.String(), nil
}

func sectionTitle(resolved, fallback string) string {
	if strings.TrimSpace(resolved) != "" {
		return resolved
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return "Untitled"
}

func formatMarkdownSection(title, content string) string {
	if strings.TrimSpace(content) == "" {
		return ""
	}
	return fmt.Sprintf("# %s\n\n%s\n\n", title, content)
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
	if s.cfg == nil || s.cfg.Storage == nil {
		return "", errors.New("storage config is missing")
	}
	baseDir := strings.TrimSpace(s.cfg.Storage.BaseDir)
	if baseDir == "" {
		return "", errors.New("storage base dir is empty")
	}
	return utils.GetProjectReportDir(baseDir, report.ProjectID, strconv.FormatInt(report.ID, 10)), nil
}

// ---------- Literature ----------

func (s *projectService) AddLiterature(ctx context.Context, userID string, literature *types.Literature) (*types.Literature, error) {
	if literature == nil {
		return nil, errors.New("literature is nil")
	}

	project, err := s.projectRepo.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}

	s.ensureLiteratureDefaults(literature)
	if literature.ID == 0 {
		literature.ID = utils.GenerateID()
	}
	if strings.TrimSpace(literature.OwnerProjectID) == "" {
		literature.OwnerProjectID = project.ProjectID
	}

	now := time.Now()
	literature.CreatedAt = now
	literature.UpdatedAt = now

	// For file-backed literature the database column stays empty; the full text
	// is written to the project literature directory instead.
	var fileContent string
	if literature.ContentSource == types.LiteratureContentSourceFile {
		fileContent = literature.Content
		literature.Content = ""
	}

	if err := s.projectRepo.CreateLiterature(ctx, literature); err != nil {
		return nil, err
	}

	if err := s.projectRepo.AddProjectLiterature(ctx, &types.ProjectLiterature{
		ProjectID:    project.ProjectID,
		LiteratureID: literature.ID,
		CreatedAt:    now,
	}); err != nil {
		return nil, err
	}

	if literature.ContentSource == types.LiteratureContentSourceFile {
		literature.Content = fileContent
		if err := s.writeLiteratureFile(literature); err != nil {
			return nil, err
		}
		literature.Content = ""
	}

	return literature, nil
}

func (s *projectService) UpdateLiterature(ctx context.Context, userID string, literature *types.Literature) error {
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

	s.ensureLiteratureDefaults(stored)

	// Owner project and filename are stable unless explicitly switched.
	literature.OwnerProjectID = stored.OwnerProjectID
	if strings.TrimSpace(literature.Filename) == "" {
		literature.Filename = stored.Filename
	}
	if literature.ContentSource != types.LiteratureContentSourceDatabase &&
		literature.ContentSource != types.LiteratureContentSourceFile {
		literature.ContentSource = stored.ContentSource
	}
	if filepath.Base(literature.Filename) != literature.Filename {
		literature.Filename = stored.Filename
	}

	switch {
	case stored.ContentSource == types.LiteratureContentSourceDatabase &&
		literature.ContentSource == types.LiteratureContentSourceFile:
		// database -> file: persist the database content into the file.
		if strings.TrimSpace(literature.Content) == "" {
			literature.Content = stored.Content
		}
		if err := s.writeLiteratureFile(literature); err != nil {
			return err
		}
		literature.Content = ""

	case literature.ContentSource == types.LiteratureContentSourceFile:
		// file -> file: rewrite the full text file.
		if err := s.writeLiteratureFile(literature); err != nil {
			return err
		}
		literature.Content = ""

	default:
		// database -> database: content stays in the database column.
	}

	return s.projectRepo.UpdateLiterature(ctx, literature)
}

func (s *projectService) DeleteLiterature(ctx context.Context, userID string, literatureID int64) error {
	literature, err := s.projectRepo.GetLiteratureByID(ctx, literatureID)
	if err != nil {
		return err
	}

	if err := s.requireLiteratureAccess(ctx, userID, literatureID); err != nil {
		return err
	}

	// Best-effort removal of the full-text file and its directory.
	if err := s.deleteLiteratureFile(literature); err != nil {
		return err
	}

	// Remove the association for every project that references this literature.
	if err := s.projectRepo.DeleteProjectLiteratureByLiteratureID(ctx, literatureID); err != nil {
		return err
	}

	return s.projectRepo.DeleteLiterature(ctx, literatureID)
}

func (s *projectService) GetLiteratureDetailByID(ctx context.Context, userID string, literatureID int64) (*types.Literature, error) {
	literature, err := s.projectRepo.GetLiteratureByID(ctx, literatureID)
	if err != nil {
		return nil, err
	}

	if err := s.requireLiteratureAccess(ctx, userID, literatureID); err != nil {
		return nil, err
	}

	s.ensureLiteratureDefaults(literature)

	if literature.ContentSource == types.LiteratureContentSourceFile {
		content, err := s.readLiteratureFile(literature)
		if err != nil {
			if os.IsNotExist(err) {
				return literature, nil
			}
			return nil, err
		}
		literature.Content = content
	}

	return literature, nil
}

func (s *projectService) ListLiteratureByProjectID(ctx context.Context, userID string) ([]*types.Literature, error) {
	project, err := s.projectRepo.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}

	return s.projectRepo.ListLiteratureByProjectID(ctx, project.ProjectID)
}

func (s *projectService) PageLiteratureByProjectID(ctx context.Context, userID string, pagination *types.Pagination) ([]*types.Literature, int64, error) {
	project, err := s.projectRepo.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return nil, 0, err
	}

	return s.projectRepo.PageLiteratureByProjectID(ctx, pagination, project.ProjectID)
}

func (s *projectService) BindLiteratureToProject(ctx context.Context, userID string, literatureID int64) error {
	project, err := s.projectRepo.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return err
	}

	literature, err := s.projectRepo.GetLiteratureByID(ctx, literatureID)
	if err != nil {
		return err
	}

	exists, err := s.projectRepo.ExistsProjectLiterature(ctx, project.ProjectID, literatureID)
	if err != nil {
		return err
	}
	if exists {
		return errors.New("literature is already bound to the active project")
	}

	return s.projectRepo.AddProjectLiterature(ctx, &types.ProjectLiterature{
		ProjectID:    project.ProjectID,
		LiteratureID: literature.ID,
		CreatedAt:    time.Now(),
	})
}

func (s *projectService) UnbindLiteratureFromProject(ctx context.Context, userID string, literatureID int64) error {
	project, err := s.projectRepo.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return err
	}

	exists, err := s.projectRepo.ExistsProjectLiterature(ctx, project.ProjectID, literatureID)
	if err != nil {
		return err
	}
	if !exists {
		return gorm.ErrRecordNotFound
	}

	return s.projectRepo.DeleteProjectLiterature(ctx, project.ProjectID, literatureID)
}

func (s *projectService) PageLiteraturePool(ctx context.Context, userID string, pagination *types.Pagination) ([]*types.LiteraturePoolItem, int64, error) {
	project, err := s.projectRepo.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return nil, 0, err
	}

	return s.projectRepo.PageLiteraturePool(ctx, pagination, project.ProjectID)
}

// requireLiteratureAccess verifies the literature is bound to the current user's
// active project. It returns gorm.ErrRecordNotFound when there is no active
// project or the binding does not exist.
func (s *projectService) requireLiteratureAccess(ctx context.Context, userID string, literatureID int64) error {
	project, err := s.projectRepo.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return err
	}

	bound, err := s.projectRepo.ExistsProjectLiterature(ctx, project.ProjectID, literatureID)
	if err != nil {
		return err
	}
	if !bound {
		return gorm.ErrRecordNotFound
	}

	return nil
}

func (s *projectService) ensureLiteratureDefaults(literature *types.Literature) {
	if literature == nil {
		return
	}
	if literature.ContentSource != types.LiteratureContentSourceDatabase &&
		literature.ContentSource != types.LiteratureContentSourceFile {
		literature.ContentSource = types.LiteratureContentSourceFile
	}
	if strings.TrimSpace(literature.Filename) == "" {
		literature.Filename = types.DefaultLiteratureFilename
	}
}

func (s *projectService) literatureFilePath(literature *types.Literature) (string, error) {
	if literature == nil {
		return "", errors.New("literature is nil")
	}
	if strings.TrimSpace(literature.OwnerProjectID) == "" {
		return "", errors.New("literature owner project id is empty")
	}

	if s.cfg == nil || s.cfg.Storage == nil {
		return "", errors.New("storage config is missing")
	}

	baseDir := strings.TrimSpace(s.cfg.Storage.BaseDir)
	if baseDir == "" {
		return "", errors.New("storage base dir is empty")
	}

	filename := strings.TrimSpace(literature.Filename)
	if filename == "" {
		filename = types.DefaultLiteratureFilename
	}
	if filepath.Base(filename) != filename {
		return "", errors.New("invalid literature filename")
	}

	dir := utils.GetProjectLiteratureDir(baseDir, literature.OwnerProjectID, strconv.FormatInt(literature.ID, 10))
	return filepath.Join(dir, filename), nil
}

func (s *projectService) writeLiteratureFile(literature *types.Literature) error {
	filePath, err := s.literatureFilePath(literature)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}

	return os.WriteFile(filePath, []byte(literature.Content), 0o644)
}

func (s *projectService) readLiteratureFile(literature *types.Literature) (string, error) {
	filePath, err := s.literatureFilePath(literature)
	if err != nil {
		return "", err
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (s *projectService) deleteLiteratureFile(literature *types.Literature) error {
	filePath, err := s.literatureFilePath(literature)
	if err != nil {
		return err
	}

	if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
		return err
	}

	// Best-effort cleanup of the now-empty per-literature directory.
	_ = os.Remove(filepath.Dir(filePath))
	return nil
}
