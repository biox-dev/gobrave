package handler

import (
	"context"
	"encoding/json"
	stderrs "errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	appservice "github.com/gobravedev/gobrave/internal/application/service"
	"github.com/gobravedev/gobrave/internal/config"
	"github.com/gobravedev/gobrave/internal/errors"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"github.com/gobravedev/gobrave/internal/utils"
	"gorm.io/gorm"
)

type ProjectHandler struct {
	projectService interfaces.ProjectService
}

func NewProjectHandler(projectService interfaces.ProjectService) *ProjectHandler {
	return &ProjectHandler{projectService: projectService}
}

// ProjectListItem godoc
type projectListItem struct {
	ID           int64       `json:"id,string"`
	ProjectID    string      `json:"project_id"`
	ProjectName  string      `json:"project_name"`
	MetadataForm interface{} `json:"metadata_form"`
	Research     string      `json:"research"`
	Parameter    string      `json:"parameter"`
	Description  string      `json:"description"`
	ShareCode    string      `json:"share_code"`
	ShareEnabled bool        `json:"share_enabled"`
}

// ListProject godoc
// @Summary      获取当前用户的项目列表
// @Description  返回与当前登录用户关联的所有项目（通过 user_project 关系表过滤）
// @Tags         项目
// @Produce      json
// @Success      200  {array}   projectListItem  "项目列表"
// @Failure      401  {object}  errors.AppError  "未认证"
// @Failure      500  {object}  errors.AppError  "服务器错误"
// @Security     Bearer
// @Router       /project/list-project [get]
func (h *ProjectHandler) ListProject(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	projects, err := h.projectService.ListProjectByUserID(ctx, userID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to list projects").WithDetails(err.Error()))
		return
	}

	result := make([]projectListItem, 0, len(projects))
	for _, p := range projects {
		metadata := parseMetadataForm(p.MetadataForm)
		result = append(result, projectListItem{
			ID:           p.ID,
			ProjectID:    p.ProjectID,
			ProjectName:  p.ProjectName,
			MetadataForm: metadata,
			Research:     p.Research,
			Parameter:    p.Parameter,
			Description:  p.Description,
			ShareCode:    p.ShareCode,
			ShareEnabled: p.ShareEnabled,
		})
	}

	c.JSON(http.StatusOK, result)
}

// GetActiveProject godoc
// @Summary      获取当前用户激活项目
// @Description  返回当前登录用户唯一激活的项目
// @Tags         项目
// @Produce      json
// @Success      200  {object}  projectListItem  "激活项目"
// @Failure      401  {object}  errors.AppError  "未认证"
// @Failure      404  {object}  errors.AppError  "未找到激活项目"
// @Failure      500  {object}  errors.AppError  "服务器错误"
// @Security     Bearer
// @Router       /project/active-project [get]
func (h *ProjectHandler) GetActiveProject(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	project, err := h.projectService.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("active project not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get active project").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, projectListItem{
		ID:           project.ID,
		ProjectID:    project.ProjectID,
		ProjectName:  project.ProjectName,
		MetadataForm: parseMetadataForm(project.MetadataForm),
		Research:     project.Research,
		Parameter:    project.Parameter,
		Description:  project.Description,
	})
}

// addUserProjectRequest is the request body for AddUserProject.
type addUserProjectRequest struct {
	ShareCode string `json:"share_code" binding:"required"`
}

// AddUserProject godoc
// @Summary      通过分享码关联用户与项目
// @Description  根据分享码查找项目，并将当前登录用户与该项目关联（写入 user_project 中间表）
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      addUserProjectRequest  true  "请求参数"
// @Success      200      {object}  map[string]string      "关联成功"
// @Failure      400      {object}  errors.AppError        "参数错误、分享码无效或已存在关联"
// @Failure      401      {object}  errors.AppError        "未认证"
// @Failure      500      {object}  errors.AppError        "服务器错误"
// @Security     Bearer
// @Router       /project/add-user-project [post]
func (h *ProjectHandler) AddUserProject(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req addUserProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.projectService.AddUserProjectByShareCode(ctx, userID, req.ShareCode); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "user project added successfully"})
}

// updateProjectSharingRequest is the request body for UpdateProjectSharing.
type updateProjectSharingRequest struct {
	ProjectID string `json:"project_id" binding:"required"`
	Enabled   bool   `json:"enabled"`
}

// UpdateProjectSharing godoc
// @Summary      开启或关闭项目分享
// @Description  开启分享时生成分享码并返回；关闭分享时清空分享码
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      updateProjectSharingRequest  true  "请求参数"
// @Success      200      {object}  map[string]interface{}       "分享状态与分享码"
// @Failure      400      {object}  errors.AppError              "参数错误"
// @Failure      401      {object}  errors.AppError              "未认证"
// @Failure      404      {object}  errors.AppError              "项目未关联当前用户"
// @Failure      500      {object}  errors.AppError              "服务器错误"
// @Security     Bearer
// @Router       /project/update-project-sharing [post]
func (h *ProjectHandler) UpdateProjectSharing(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req updateProjectSharingRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	shareCode, err := h.projectService.UpdateProjectSharing(ctx, userID, req.ProjectID, req.Enabled)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project is not bound to current user"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to update project sharing").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"share_enabled": req.Enabled, "share_code": shareCode})
}

// addProjectToUserRequest is the request body for AddProjectToUser.
type addProjectToUserRequest struct {
	UserID    string `json:"user_id" binding:"required"`
	ProjectID string `json:"project_id" binding:"required"`
}

// AddProjectToUser godoc
// @Summary      按用户ID关联用户与项目
// @Description  将指定用户与指定项目关联（写入 user_project 中间表），用户ID由请求传入
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      addProjectToUserRequest  true  "请求参数"
// @Success      200      {object}  map[string]string        "关联成功"
// @Failure      400      {object}  errors.AppError          "参数错误或已存在关联"
// @Failure      401      {object}  errors.AppError          "未认证"
// @Failure      500      {object}  errors.AppError          "服务器错误"
// @Security     Bearer
// @Router       /project/add-project-to-user [post]
func (h *ProjectHandler) AddProjectToUser(c *gin.Context) {
	ctx := c.Request.Context()

	var req addProjectToUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.projectService.AddUserProject(ctx, req.UserID, req.ProjectID); err != nil {
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "user project added successfully"})
}

type activateProjectRequest struct {
	ProjectID string `json:"project_id" binding:"required"`
}

// ActivateProject godoc
// @Summary      激活当前用户项目
// @Description  按项目ID激活当前用户的项目，并将该用户其他项目全部置为未激活
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      activateProjectRequest  true  "请求参数"
// @Success      200      {object}  map[string]string       "激活成功"
// @Failure      400      {object}  errors.AppError         "参数错误"
// @Failure      401      {object}  errors.AppError         "未认证"
// @Failure      404      {object}  errors.AppError         "项目未关联当前用户"
// @Failure      500      {object}  errors.AppError         "服务器错误"
// @Security     Bearer
// @Router       /project/activate-project [post]
func (h *ProjectHandler) ActivateProject(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req activateProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.projectService.ActivateUserProject(ctx, userID, req.ProjectID); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project is not bound to current user"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to activate project").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "project activated successfully"})
}

type deleteUserProjectRequest struct {
	ProjectID string `json:"project_id" binding:"required"`
}

// DeleteUserProject godoc
// @Summary      删除用户与项目关联
// @Description  根据当前登录用户与项目ID删除 user_project 中间表记录，仅当该关联为非激活状态时允许删除
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      deleteUserProjectRequest  true  "请求参数"
// @Success      200      {object}  map[string]string         "删除成功"
// @Failure      400      {object}  errors.AppError           "参数错误或关联仍为激活状态"
// @Failure      404      {object}  errors.AppError           "关联不存在"
// @Failure      500      {object}  errors.AppError           "服务器错误"
// @Security     Bearer
// @Router       /project/delete-user-project [post]
func (h *ProjectHandler) DeleteUserProject(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req deleteUserProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.projectService.DeleteUserProject(ctx, userID, req.ProjectID); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("user project binding not found"))
			return
		}
		if stderrs.Is(err, appservice.ErrUserProjectActive) {
			c.Error(errors.NewBadRequestError(err.Error()))
			return
		}
		c.Error(errors.NewInternalServerError("failed to delete user project").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "user project deleted successfully"})
}

type addProjectReportRequest struct {
	ProjectID string `json:"project_id" binding:"required"`
	Title     string `json:"title" binding:"required"`
	Content   string `json:"content"`
	SortOrder int    `json:"sort_order"`
}

// AddProjectReport godoc
// @Summary      添加项目报告
// @Description  向指定项目添加报告记录
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      addProjectReportRequest  true  "请求参数"
// @Success      200      {object}  types.ProjectReport      "创建成功"
// @Failure      400      {object}  errors.AppError          "参数错误"
// @Failure      401      {object}  errors.AppError          "未认证"
// @Failure      404      {object}  errors.AppError          "项目未关联当前用户"
// @Failure      500      {object}  errors.AppError          "服务器错误"
// @Security     Bearer
// @Router       /project/add-project-report [post]
func (h *ProjectHandler) AddProjectReport(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req addProjectReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	report := &types.ProjectReport{
		ProjectID: req.ProjectID,
		Title:     req.Title,
		Content:   req.Content,
		SortOrder: req.SortOrder,
	}

	if err := h.projectService.AddProjectReport(ctx, userID, report); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project is not bound to current user"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to add project report").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, report)
}

type updateProjectReportRequest struct {
	ID        int64  `json:"id,string" binding:"required"`
	ProjectID string `json:"project_id" binding:"required"`
	Title     string `json:"title" binding:"required"`
	Content   string `json:"content"`
	SortOrder int    `json:"sort_order"`
}

// UpdateProjectReport godoc
// @Summary      更新项目报告
// @Description  按报告ID更新指定项目下的报告
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      updateProjectReportRequest  true  "请求参数"
// @Success      200      {object}  map[string]string           "更新成功"
// @Failure      400      {object}  errors.AppError             "参数错误"
// @Failure      401      {object}  errors.AppError             "未认证"
// @Failure      404      {object}  errors.AppError             "项目或报告不存在"
// @Failure      500      {object}  errors.AppError             "服务器错误"
// @Security     Bearer
// @Router       /project/update-project-report [post]
func (h *ProjectHandler) UpdateProjectReport(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req updateProjectReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.projectService.UpdateProjectReport(ctx, userID, &types.ProjectReport{
		ID:        req.ID,
		ProjectID: req.ProjectID,
		Title:     req.Title,
		Content:   req.Content,
		SortOrder: req.SortOrder,
	}); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project report not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to update project report").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "project report updated successfully"})
}

type deleteProjectReportRequest struct {
	ID int64 `json:"id,string" binding:"required"`
}

// DeleteProjectReport godoc
// @Summary      删除项目报告
// @Description  按报告ID删除指定项目下的报告
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      deleteProjectReportRequest  true  "请求参数"
// @Success      200      {object}  map[string]string           "删除成功"
// @Failure      400      {object}  errors.AppError             "参数错误"
// @Failure      401      {object}  errors.AppError             "未认证"
// @Failure      404      {object}  errors.AppError             "项目或报告不存在"
// @Failure      500      {object}  errors.AppError             "服务器错误"
// @Security     Bearer
// @Router       /project/delete-project-report [post]
func (h *ProjectHandler) DeleteProjectReport(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req deleteProjectReportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.projectService.DeleteProjectReport(ctx, userID, req.ID); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project report not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to delete project report").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "project report deleted successfully"})
}

type projectReportDetailRequest struct {
	ID string `form:"id" binding:"required"`
}

type projectReportListItem struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Title     string `json:"title"`
	Source    string `json:"source"`
	SortOrder int    `json:"sort_order"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type projectReportDetailItem struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	SortOrder int       `json:"sort_order"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListProjectReport godoc
// @Summary      查询项目报告列表
// @Description  查询当前用户激活项目的报告列表，不返回 content 字段，按 sort_order、created_at 升序排序
// @Tags         项目
// @Produce      json
// @Success      200         {array}   projectReportListItem   "报告列表"
// @Failure      401         {object}  errors.AppError         "未认证"
// @Failure      404         {object}  errors.AppError         "未找到激活项目"
// @Failure      500         {object}  errors.AppError         "服务器错误"
// @Security     Bearer
// @Router       /project/list-project-report [get]
func (h *ProjectHandler) ListProjectReport(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	project, err := h.projectService.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("active project not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get active project").WithDetails(err.Error()))
		return
	}

	reports, err := h.projectService.ListProjectReportByProjectID(ctx, userID, project.ProjectID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("active project is not bound to current user"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to list project report").WithDetails(err.Error()))
		return
	}

	result := make([]projectReportListItem, 0, len(reports))
	for _, report := range reports {
		result = append(result, projectReportListItem{
			ID:        strconv.FormatInt(report.ID, 10),
			ProjectID: report.ProjectID,
			Title:     report.Title,
			Source:    "database",
			SortOrder: report.SortOrder,
			CreatedAt: report.CreatedAt.Format("2006-01-02 15:04:05"),
			UpdatedAt: report.UpdatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	result = appendProjectReportMarkdownFiles(result, project.ProjectID)

	c.JSON(http.StatusOK, result)
}

func appendProjectReportMarkdownFiles(result []projectReportListItem, projectID string) []projectReportListItem {
	cfg, err := config.LoadConfig()
	if err != nil || cfg == nil || cfg.Storage == nil {
		return result
	}

	baseDir := strings.TrimSpace(cfg.Storage.BaseDir)
	if baseDir == "" {
		return result
	}

	srcDir, err := utils.SafePathUnderBase(baseDir, filepath.Join(baseDir, "data", projectID, "docs", "src"))
	if err != nil {
		return result
	}

	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return result
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		fileName := entry.Name()
		if !strings.HasSuffix(strings.ToLower(fileName), ".md") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		updatedAt := info.ModTime().Format("2006-01-02 15:04:05")
		result = append(result, projectReportListItem{
			ID:        "file:" + fileName,
			ProjectID: projectID,
			Title:     fileName,
			Source:    "file",
			SortOrder: 0,
			CreatedAt: updatedAt,
			UpdatedAt: updatedAt,
		})
	}

	return result
}

// GetProjectReportDetail godoc
// @Summary      查询项目报告详情
// @Description  根据 id 查询报告详情（包含 content）
// @Tags         项目
// @Produce      json
// @Param        id          query     string                  true  "报告ID或 file:文件名.md"
// @Success      200         {object}  projectReportDetailItem "报告详情"
// @Failure      400         {object}  errors.AppError         "参数错误"
// @Failure      401         {object}  errors.AppError         "未认证"
// @Failure      404         {object}  errors.AppError         "项目未关联当前用户"
// @Failure      500         {object}  errors.AppError         "服务器错误"
// @Security     Bearer
// @Router       /project/project-report-detail [get]
func (h *ProjectHandler) GetProjectReportDetail(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req projectReportDetailRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if strings.HasPrefix(req.ID, "file:") {
		report, err := h.getProjectReportDetailFromFile(ctx, userID, req.ID)
		if err != nil {
			if appErr, ok := errors.IsAppError(err); ok {
				c.Error(appErr)
				return
			}
			if stderrs.Is(err, gorm.ErrRecordNotFound) {
				c.Error(errors.NewNotFoundError("project report is not bound to current user"))
				return
			}
			c.Error(errors.NewInternalServerError("failed to get project report detail").WithDetails(err.Error()))
			return
		}

		c.JSON(http.StatusOK, report)
		return
	}

	reportID, err := strconv.ParseInt(strings.TrimSpace(req.ID), 10, 64)
	if err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails("id must be an integer or file:filename.md"))
		return
	}

	report, err := h.projectService.GetProjectReportDetailByID(ctx, userID, reportID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project report is not bound to current user"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get project report detail").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, newProjectReportDetailItem(strconv.FormatInt(report.ID, 10), report))
}

func (h *ProjectHandler) getProjectReportDetailFromFile(ctx context.Context, userID, fileID string) (*projectReportDetailItem, error) {
	project, err := h.projectService.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}

	fileName := strings.TrimSpace(strings.TrimPrefix(fileID, "file:"))
	if fileName == "" || !strings.HasSuffix(strings.ToLower(fileName), ".md") {
		return nil, errors.NewValidationError("invalid request parameters").WithDetails("invalid file report id")
	}
	if filepath.Base(fileName) != fileName {
		return nil, errors.NewValidationError("invalid request parameters").WithDetails("invalid file report id")
	}

	cfg, err := config.LoadConfig()
	if err != nil || cfg == nil || cfg.Storage == nil {
		return nil, gorm.ErrRecordNotFound
	}

	baseDir := strings.TrimSpace(cfg.Storage.BaseDir)
	if baseDir == "" {
		return nil, gorm.ErrRecordNotFound
	}

	filePath, err := utils.SafePathUnderBase(baseDir, filepath.Join(baseDir, "data", project.ProjectID, "docs", "src", fileName))
	if err != nil {
		return nil, errors.NewValidationError("invalid request parameters").WithDetails("invalid file report id")
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, gorm.ErrRecordNotFound
		}
		return nil, err
	}

	info, err := os.Stat(filePath)
	if err != nil {
		return nil, err
	}

	report := &types.ProjectReport{
		ProjectID: project.ProjectID,
		Title:     fileName,
		Content:   string(content),
		SortOrder: 0,
		CreatedAt: info.ModTime(),
		UpdatedAt: info.ModTime(),
	}

	return newProjectReportDetailItem(fileID, report), nil
}

func newProjectReportDetailItem(id string, report *types.ProjectReport) *projectReportDetailItem {
	if report == nil {
		return nil
	}

	return &projectReportDetailItem{
		ID:        id,
		ProjectID: report.ProjectID,
		Title:     report.Title,
		Content:   report.Content,
		SortOrder: report.SortOrder,
		CreatedAt: report.CreatedAt,
		UpdatedAt: report.UpdatedAt,
	}
}

func getCurrentUserID(c *gin.Context) (string, bool) {
	userIDVal, exists := c.Get(types.UserIDContextKey.String())
	if !exists {
		c.Error(errors.NewUnauthorizedError("missing user identity"))
		return "", false
	}

	userID, ok := userIDVal.(string)
	if !ok || userID == "" {
		c.Error(errors.NewUnauthorizedError("invalid user identity"))
		return "", false
	}

	return userID, true
}

func parseMetadataForm(raw string) interface{} {
	if raw == "" {
		return []interface{}{}
	}

	var decoded interface{}
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return []interface{}{}
	}

	return decoded
}
