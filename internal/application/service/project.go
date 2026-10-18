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
	apperrors "github.com/biox-dev/gobrave/internal/errors"
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
	dir, err := s.projectReportDir(report)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}

	file := filepath.Join(dir, types.DefaultProjectReportFilename)
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
	if err := s.projectRepo.DeleteProjectReportItemsByReportID(ctx, report.ID); err != nil {
		return err
	}

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

// ---------- ProjectReportItem ----------

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

// loadOwnedProjectReportItem 读取条目并校验其所属报告归属当前用户。
func (s *projectService) loadOwnedProjectReportItem(ctx context.Context, userID int64, itemID int64) (*types.ProjectReportItem, *types.ProjectReport, error) {
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

// ListProjectReportItemsByReportID 返回报告下的全部条目，顺序与前端 tree 展示一致：
// 按树的先序遍历展开（父节点紧随其后跟子节点，同级按 sort_order 升序），
// 而不是仓库层那种全局 sort_order 排序（会把子孙节点与其父节点拆散）。
func (s *projectService) ListProjectReportItemsByReportID(ctx context.Context, userID int64, reportID int64) ([]*types.ProjectReportItem, error) {
	report, err := s.loadOwnedProjectReport(ctx, userID, reportID)
	if err != nil {
		return nil, err
	}

	items, err := s.projectRepo.ListProjectReportItemsByReportID(ctx, report.ID)
	if err != nil {
		return nil, err
	}

	// 仓库层按全局 sort_order 返回，这里重排为树的前序遍历（展开态），
	// 保证调用方（报告正文拼接、Agent 报告上下文等）拿到的是「与 tree 相同顺序」的扁平列表。
	items = flattenProjectReportItems(items)
	for _, item := range items {
		item.Title = s.projectReportItemTitle(ctx, item)
	}
	return items, nil
}

func (s *projectService) AddProjectReportItem(ctx context.Context, userID int64, item *types.ProjectReportItem) error {
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
	if ownerType == types.ProjectReportItemOwnerCustom {
		// 自定义内容条目无 owner 引用，仅保存用户编辑的标题与正文。
		item.OwnerID = 0
	} else {
		if item.OwnerID <= 0 {
			return errors.New("owner_id is required for project report item")
		}
		// 其他引用类型的标题/正文由 owner 推导，不落库。
		item.Title = ""
		item.Content = ""

		// 同一报告下同一 owner 引用（owner_type + owner_id）只允许出现一次，避免重复添加。
		// custom 类型不引用外部记录（OwnerID 恒为 0），不受此限制。
		exists, err := s.projectRepo.ExistsProjectReportItemOwner(ctx, item.ProjectReportID, ownerType, item.OwnerID, 0)
		if err != nil {
			return err
		}
		if exists {
			return apperrors.NewConflictError(fmt.Sprintf("project report item already exists for %s %d", ownerType, item.OwnerID))
		}
	}

	if err := s.validateProjectReportItemParent(ctx, item.ProjectReportID, item.ID, item.ParentID); err != nil {
		return err
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

func (s *projectService) UpdateProjectReportItem(ctx context.Context, userID int64, item *types.ProjectReportItem) error {
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
		if ownerType == types.ProjectReportItemOwnerCustom {
			stored.OwnerID = 0
		} else if item.OwnerID > 0 {
			stored.OwnerID = item.OwnerID
		}
	}
	if stored.OwnerType == types.ProjectReportItemOwnerCustom {
		// 自定义内容条目保存标题与正文。
		stored.Title = item.Title
		stored.Content = item.Content
	} else {
		// 其他引用类型不存储标题与内容（由 owner 推导）。
		stored.Title = ""
		stored.Content = ""

		// 变更 owner 引用后仍不允许与同报告内的其他条目重复（排除自身）。
		exists, err := s.projectRepo.ExistsProjectReportItemOwner(ctx, stored.ProjectReportID, stored.OwnerType, stored.OwnerID, stored.ID)
		if err != nil {
			return err
		}
		if exists {
			return apperrors.NewConflictError(fmt.Sprintf("project report item already exists for %s %d", stored.OwnerType, stored.OwnerID))
		}
	}
	stored.ParentID = item.ParentID
	if err := s.validateProjectReportItemParent(ctx, stored.ProjectReportID, stored.ID, stored.ParentID); err != nil {
		return err
	}
	stored.UpdatedAt = time.Now()

	return s.projectRepo.UpdateProjectReportItem(ctx, stored)
}

// ReorderProjectReportItems 持久化拖拽结果：批量更新条目的父节点与同级顺序。
func (s *projectService) ReorderProjectReportItems(ctx context.Context, userID int64, reportID int64, orders []types.ProjectReportItemOrder) error {
	report, err := s.loadOwnedProjectReport(ctx, userID, reportID)
	if err != nil {
		return err
	}

	if len(orders) == 0 {
		return nil
	}

	items, err := s.projectRepo.ListProjectReportItemsByReportID(ctx, report.ID)
	if err != nil {
		return err
	}
	belongs := make(map[int64]bool, len(items))
	for _, item := range items {
		belongs[item.ID] = true
	}

	seen := make(map[int64]bool, len(orders))
	for _, order := range orders {
		if !belongs[order.ID] {
			return fmt.Errorf("project report item %d does not belong to report %d", order.ID, report.ID)
		}
		if order.ParentID != 0 && !belongs[order.ParentID] {
			return fmt.Errorf("parent item %d does not belong to report %d", order.ParentID, report.ID)
		}
		if order.ParentID == order.ID {
			return fmt.Errorf("project report item %d cannot be its own parent", order.ID)
		}
		seen[order.ID] = true
	}
	for _, order := range orders {
		if order.ParentID != 0 && !seen[order.ParentID] {
			return fmt.Errorf("parent item %d is not part of the reorder payload", order.ParentID)
		}
	}

	return s.projectRepo.ReorderProjectReportItems(ctx, report.ID, orders)
}

// validateProjectReportItemParent 校验父节点存在且属于同一报告，避免出现跨报告父子关系。
func (s *projectService) validateProjectReportItemParent(ctx context.Context, reportID, itemID, parentID int64) error {
	if parentID == 0 {
		return nil
	}
	if parentID == itemID && itemID != 0 {
		return errors.New("project report item cannot be its own parent")
	}

	parent, err := s.projectRepo.GetProjectReportItemByID(ctx, parentID)
	if err != nil {
		return err
	}
	if parent.ProjectReportID != reportID {
		return errors.New("parent project report item belongs to another report")
	}
	return nil
}

func (s *projectService) DeleteProjectReportItem(ctx context.Context, userID int64, itemID int64) error {
	item, _, err := s.loadOwnedProjectReportItem(ctx, userID, itemID)
	if err != nil {
		return err
	}

	items, err := s.projectRepo.ListProjectReportItemsByReportID(ctx, item.ProjectReportID)
	if err != nil {
		return err
	}

	return s.projectRepo.DeleteProjectReportItemsByIDs(ctx, collectProjectReportItemSubtree(items, item.ID))
}

// collectProjectReportItemSubtree 返回 itemID 及其所有子孙节点的 ID 集合。
func collectProjectReportItemSubtree(items []*types.ProjectReportItem, rootID int64) []int64 {
	children := make(map[int64][]int64, len(items))
	for _, item := range items {
		if item.ParentID != 0 {
			children[item.ParentID] = append(children[item.ParentID], item.ID)
		}
	}

	ids := make([]int64, 0, len(items))
	stack := []int64{rootID}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		ids = append(ids, id)
		stack = append(stack, children[id]...)
	}
	return ids
}

func (s *projectService) GetProjectReportItemDetailByID(ctx context.Context, userID int64, itemID int64) (*types.ProjectReportItem, error) {
	item, _, err := s.loadOwnedProjectReportItem(ctx, userID, itemID)
	if err != nil {
		return nil, err
	}

	item.Title = s.projectReportItemTitle(ctx, item)
	return item, nil
}

// GetProjectReportContent 汇总报告下所有条目，按树的前序遍历拼接成正文，仅返回拼接后的内容。
func (s *projectService) GetProjectReportContent(ctx context.Context, reportID int64) (string, error) {
	report, err := s.projectRepo.GetProjectReportByID(ctx, reportID)
	if err != nil {
		return "", err
	}

	items, err := s.projectRepo.ListProjectReportItemsByReportID(ctx, report.ID)
	if err != nil {
		return "", err
	}

	var builder strings.Builder
	for _, item := range flattenProjectReportItems(items) {
		section, err := s.projectReportItemMarkdown(ctx, item)
		if err != nil {
			return "", err
		}
		builder.WriteString(section.Render())
	}

	return builder.String(), nil
}

// flattenProjectReportItems 按树的先序遍历排列条目（父节点先于其子节点）。
// 入参需已按同级 sort_order 升序排列；出现环或孤立的不可达节点时按原顺序兜底追加。
func flattenProjectReportItems(items []*types.ProjectReportItem) []*types.ProjectReportItem {
	existing := make(map[int64]bool, len(items))
	for _, item := range items {
		existing[item.ID] = true
	}

	childrenByParent := make(map[int64][]*types.ProjectReportItem, len(items))
	for _, item := range items {
		if item.ParentID != 0 && existing[item.ParentID] {
			childrenByParent[item.ParentID] = append(childrenByParent[item.ParentID], item)
			continue
		}
		childrenByParent[0] = append(childrenByParent[0], item)
	}

	result := make([]*types.ProjectReportItem, 0, len(items))
	visited := make(map[int64]bool, len(items))
	var walk func(parentID int64)
	walk = func(parentID int64) {
		for _, item := range childrenByParent[parentID] {
			if visited[item.ID] {
				continue
			}
			visited[item.ID] = true
			result = append(result, item)
			walk(item.ID)
		}
	}
	walk(0)

	for _, item := range items {
		if !visited[item.ID] {
			visited[item.ID] = true
			result = append(result, item)
		}
	}

	return result
}

// GetProjectReportItemContent 返回指定条目（入参为 ProjectReportItem ID）渲染后的内容片段。
func (s *projectService) GetProjectReportItemContent(ctx context.Context, itemID int64) (*types.ProjectReportItemContent, error) {
	item, err := s.projectRepo.GetProjectReportItemByID(ctx, itemID)
	if err != nil {
		return nil, err
	}

	return s.projectReportItemMarkdown(ctx, item)
}

// projectReportItemTitle 解析条目的展示标题；解析失败时回退到 "owner_type #id"。
func (s *projectService) projectReportItemTitle(ctx context.Context, item *types.ProjectReportItem) string {
	if item == nil {
		return ""
	}

	switch item.OwnerType {
	case types.ProjectReportItemOwnerCustom:
		if title := strings.TrimSpace(item.Title); title != "" {
			return title
		}
		return "Untitled"
	case types.ProjectReportItemOwnerAnalysis:
		if s.analysisRepo != nil {
			if analysis, err := s.analysisRepo.GetAnalysisByID(ctx, item.OwnerID); err == nil && strings.TrimSpace(analysis.AnalysisName) != "" {
				return analysis.AnalysisName
			}
		}
	case types.ProjectReportItemOwnerAnalysisNode:
		if s.analysisRepo != nil {
			if node, err := s.analysisRepo.GetAnalysisNodeByID(ctx, item.OwnerID); err == nil && strings.TrimSpace(node.NodeName) != "" {
				return node.NodeName
			}
		}
	case types.ProjectReportItemOwnerAISummary:
		if s.summaryRepo != nil {
			if summary, err := s.summaryRepo.GetAISummaryByID(ctx, item.OwnerID); err == nil && strings.TrimSpace(summary.Title) != "" {
				return summary.Title
			}
		}
	}

	return fmt.Sprintf("%s #%d", item.OwnerType, item.OwnerID)
}

// projectReportItemMarkdown 返回单个条目对应的内容片段（标题 + 正文）。
func (s *projectService) projectReportItemMarkdown(ctx context.Context, item *types.ProjectReportItem) (*types.ProjectReportItemContent, error) {
	var (
		section *types.ProjectReportItemContent
		err     error
	)
	switch item.OwnerType {
	case types.ProjectReportItemOwnerCustom:
		// 自定义内容条目直接渲染用户编辑的标题与 markdown 正文。
		section = &types.ProjectReportItemContent{
			Title:   item.Title,
			Content: item.Content,
		}
	case types.ProjectReportItemOwnerAISummary:
		section, err = s.aiSummaryMarkdown(ctx, item)
	case types.ProjectReportItemOwnerAnalysisNode:
		section, err = s.analysisNodeMarkdown(ctx, item)
	case types.ProjectReportItemOwnerAnalysis:
		section, err = s.analysisMarkdown(ctx, item)
	default:
		return nil, fmt.Errorf("unsupported project report item owner type: %s", item.OwnerType)
	}
	if err != nil {
		return nil, err
	}
	if section != nil {
		section.OwnerType = item.OwnerType
	}
	return section, nil
}

func (s *projectService) aiSummaryMarkdown(ctx context.Context, item *types.ProjectReportItem) (*types.ProjectReportItemContent, error) {
	if s.summaryRepo == nil {
		return nil, errors.New("ai summary repository is unavailable")
	}
	summary, err := s.summaryRepo.GetAISummaryByID(ctx, item.OwnerID)
	if err != nil {
		return nil, err
	}

	// 摘要正文保存在文件里（见 utils.GetAISummaryFile），不存在时按空正文处理。
	// 存储目录直接使用项目数值主键。
	var content string
	if projectID := s.resolveSummaryProjectID(ctx, summary); projectID != 0 {
		content, err = utils.ReadAISummaryFile(s.storageBaseDir(), projectID, summary.ID)
		if err != nil {
			return nil, err
		}
	}

	return &types.ProjectReportItemContent{
		Title:   sectionTitle(item.Title, summary.Title),
		Content: content,
	}, nil
}

// resolveSummaryProjectID 返回摘要所属对象的项目 ID：优先使用摘要记录上已持久化的
// ProjectID，缺失时回退到所属对象（Analysis / AnalysisNode）解析。
func (s *projectService) resolveSummaryProjectID(ctx context.Context, summary *types.AISummary) int64 {
	if summary == nil {
		return 0
	}
	if summary.ProjectID != 0 {
		return summary.ProjectID
	}
	if s.analysisRepo == nil {
		return 0
	}
	switch summary.OwnerType {
	case types.SummaryOwnerAnalysisNode:
		if node, err := s.analysisRepo.GetAnalysisNodeByID(ctx, summary.OwnerID); err == nil {
			return node.ProjectID
		}
	case types.SummaryOwnerAnalysis:
		if analysis, err := s.analysisRepo.GetAnalysisByID(ctx, summary.OwnerID); err == nil {
			return analysis.ProjectID
		}
	}
	return 0
}

func (s *projectService) analysisNodeMarkdown(ctx context.Context, item *types.ProjectReportItem) (*types.ProjectReportItemContent, error) {
	if s.analysisRepo == nil {
		return nil, errors.New("analysis repository is unavailable")
	}
	node, err := s.analysisRepo.GetAnalysisNodeByID(ctx, item.OwnerID)
	if err != nil {
		return nil, err
	}

	content := readMarkdownFile(node.OutputDir)
	if strings.TrimSpace(content) == "" {
		content = fmt.Sprintf("> 未找到节点输出文件：%s", filepath.Join(node.OutputDir, types.DefaultProjectReportFilename))
	}
	return &types.ProjectReportItemContent{
		Title:   sectionTitle(item.Title, node.NodeName),
		Content: content,
	}, nil
}

func (s *projectService) analysisMarkdown(ctx context.Context, item *types.ProjectReportItem) (*types.ProjectReportItemContent, error) {
	if s.analysisRepo == nil {
		return nil, errors.New("analysis repository is unavailable")
	}
	analysis, err := s.analysisRepo.GetAnalysisByID(ctx, item.OwnerID)
	if err != nil {
		return nil, err
	}
	nodes, err := s.analysisRepo.ListAnalysisNodesByAnalysisID(ctx, item.OwnerID)
	if err != nil {
		return nil, err
	}

	var builder strings.Builder
	for _, node := range nodes {
		content := readMarkdownFile(node.OutputDir)
		if strings.TrimSpace(content) == "" {
			continue
		}
		builder.WriteString(fmt.Sprintf("## %s\n\n%s\n\n", node.NodeName, content))
	}
	return &types.ProjectReportItemContent{
		Title:   sectionTitle(item.Title, analysis.AnalysisName),
		Content: builder.String(),
	}, nil
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

	dir, err := s.projectReportDir(report)
	if err != nil {
		return "", err
	}

	// 报告文件所在目录从第一次读取起就纳入版本管理（best-effort：失败不影响读取）。
	s.ensureProjectReportGitRepo(dir)

	data, err := os.ReadFile(filepath.Join(dir, types.DefaultProjectReportFilename))
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

	dir, err := s.projectReportDir(report)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(dir, types.DefaultProjectReportFilename), []byte(content), 0o644); err != nil {
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
