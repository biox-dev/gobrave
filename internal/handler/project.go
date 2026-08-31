package handler

import (
	"encoding/json"
	stderrs "errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	appservice "github.com/biox-dev/gobrave/internal/application/service"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/gin-gonic/gin"
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

// createProjectRequest is the request body for CreateProject.
type createProjectRequest struct {
	ProjectName  string `json:"project_name" binding:"required"`
	MetadataForm string `json:"metadata_form"`
	Research     string `json:"research"`
	Parameter    string `json:"parameter"`
	Description  string `json:"description"`
}

// CreateProject godoc
// @Summary      为当前用户创建项目
// @Description  创建一个新项目并将其关联到当前登录用户，同时将该用户其他项目置为非激活
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      createProjectRequest  true  "请求参数"
// @Success      200      {object}  projectListItem       "创建成功"
// @Failure      400      {object}  errors.AppError       "参数错误"
// @Failure      401      {object}  errors.AppError       "未认证"
// @Failure      500      {object}  errors.AppError       "服务器错误"
// @Security     Bearer
// @Router       /project/create-project [post]
func (h *ProjectHandler) CreateProject(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req createProjectRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	project := &types.Project{
		ProjectName:  strings.TrimSpace(req.ProjectName),
		MetadataForm: req.MetadataForm,
		Research:     req.Research,
		Parameter:    req.Parameter,
		Description:  req.Description,
	}

	created, err := h.projectService.CreateProjectForUser(ctx, userID, project)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to create project").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, projectListItem{
		ID:           created.ID,
		ProjectID:    created.ProjectID,
		ProjectName:  created.ProjectName,
		MetadataForm: parseMetadataForm(created.MetadataForm),
		Research:     created.Research,
		Parameter:    created.Parameter,
		Description:  created.Description,
	})
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
	ProjectID     string `json:"project_id" binding:"required"`
	Title         string `json:"title" binding:"required"`
	Content       string `json:"content"`
	ContentSource string `json:"content_source"`
	Filename      string `json:"filename"`
	SortOrder     int    `json:"sort_order"`
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
		ProjectID:     req.ProjectID,
		Title:         req.Title,
		Content:       req.Content,
		ContentSource: req.ContentSource,
		Filename:      req.Filename,
		SortOrder:     req.SortOrder,
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
	ID            int64  `json:"id,string" binding:"required"`
	ProjectID     string `json:"project_id" binding:"required"`
	Title         string `json:"title" binding:"required"`
	Content       string `json:"content"`
	ContentSource string `json:"content_source"`
	Filename      string `json:"filename"`
	SortOrder     int    `json:"sort_order"`
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
		ID:            req.ID,
		ProjectID:     req.ProjectID,
		Title:         req.Title,
		Content:       req.Content,
		ContentSource: req.ContentSource,
		Filename:      req.Filename,
		SortOrder:     req.SortOrder,
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
	ID int64 `form:"id" binding:"required"`
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
	ID            string    `json:"id"`
	ProjectID     string    `json:"project_id"`
	Title         string    `json:"title"`
	Content       string    `json:"content"`
	ContentSource string    `json:"content_source"`
	Filename      string    `json:"filename"`
	SortOrder     int       `json:"sort_order"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
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
			Source:    report.ContentSource,
			SortOrder: report.SortOrder,
			CreatedAt: report.CreatedAt.Format("2006-01-02 15:04:05"),
			UpdatedAt: report.UpdatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	c.JSON(http.StatusOK, result)
}

type projectReportPageRequest struct {
	types.Pagination
}

// PageProjectReport godoc
// @Summary      分页查询项目报告列表
// @Description  按当前用户激活项目分页查询报告列表，不返回 content 字段，按 sort_order 降序、created_at 升序排序
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      handler.projectReportPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /project/list-project-report-page [post]
func (h *ProjectHandler) PageProjectReport(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req projectReportPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
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

	reports, total, err := h.projectService.PageProjectReportByProjectID(ctx, userID, project.ProjectID, &req.Pagination)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("active project is not bound to current user"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to page project report").WithDetails(err.Error()))
		return
	}

	result := make([]projectReportListItem, 0, len(reports))
	for _, report := range reports {
		result = append(result, projectReportListItem{
			ID:        strconv.FormatInt(report.ID, 10),
			ProjectID: report.ProjectID,
			Title:     report.Title,
			Source:    report.ContentSource,
			SortOrder: report.SortOrder,
			CreatedAt: report.CreatedAt.Format("2006-01-02 15:04:05"),
			UpdatedAt: report.UpdatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"data":       result,
		"total":      total,
		"page":       req.GetPage(),
		"page_size":  req.GetPageSize(),
		"project_id": project.ProjectID,
	})
}

// GetProjectReportDetail godoc
// @Summary      查询项目报告详情
// @Description  根据 id 查询报告详情（包含 content）
// @Tags         项目
// @Produce      json
// @Param        id          query     int64                   true  "报告ID"
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

	report, err := h.projectService.GetProjectReportDetailByID(ctx, userID, req.ID)
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

func newProjectReportDetailItem(id string, report *types.ProjectReport) *projectReportDetailItem {
	if report == nil {
		return nil
	}

	return &projectReportDetailItem{
		ID:            id,
		ProjectID:     report.ProjectID,
		Title:         report.Title,
		Content:       report.Content,
		ContentSource: report.ContentSource,
		Filename:      report.Filename,
		SortOrder:     report.SortOrder,
		CreatedAt:     report.CreatedAt,
		UpdatedAt:     report.UpdatedAt,
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

// ---------- Literature (参考文献) ----------

type addLiteratureRequest struct {
	Title         string `json:"title" binding:"required"`
	Content       string `json:"content"`
	ContentSource string `json:"content_source"`
	Filename      string `json:"filename"`
}

// AddLiterature godoc
// @Summary      为激活项目添加文献
// @Description  创建一条参考文献记录并绑定到当前用户激活项目，全文按 content_source 存储（默认写入文件）
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      addLiteratureRequest  true  "请求参数"
// @Success      200      {object}  types.Literature      "创建成功"
// @Failure      400      {object}  errors.AppError       "参数错误"
// @Failure      401      {object}  errors.AppError       "未认证"
// @Failure      404      {object}  errors.AppError       "未找到激活项目"
// @Failure      500      {object}  errors.AppError       "服务器错误"
// @Security     Bearer
// @Router       /project/add-literature [post]
func (h *ProjectHandler) AddLiterature(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req addLiteratureRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	literature, err := h.projectService.AddLiterature(ctx, userID, &types.Literature{
		Title:         req.Title,
		Content:       req.Content,
		ContentSource: req.ContentSource,
		Filename:      req.Filename,
	})
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("active project not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to add literature").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, literature)
}

type updateLiteratureRequest struct {
	ID            int64  `json:"id,string" binding:"required"`
	Title         string `json:"title" binding:"required"`
	Content       string `json:"content"`
	ContentSource string `json:"content_source"`
	Filename      string `json:"filename"`
}

// UpdateLiterature godoc
// @Summary      更新文献
// @Description  按文献ID更新标题、全文内容与存储方式
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      updateLiteratureRequest  true  "请求参数"
// @Success      200      {object}  map[string]string        "更新成功"
// @Failure      400      {object}  errors.AppError          "参数错误"
// @Failure      401      {object}  errors.AppError          "未认证"
// @Failure      404      {object}  errors.AppError          "文献不存在或未绑定当前激活项目"
// @Failure      500      {object}  errors.AppError          "服务器错误"
// @Security     Bearer
// @Router       /project/update-literature [post]
func (h *ProjectHandler) UpdateLiterature(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req updateLiteratureRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.projectService.UpdateLiterature(ctx, userID, &types.Literature{
		ID:            req.ID,
		Title:         req.Title,
		Content:       req.Content,
		ContentSource: req.ContentSource,
		Filename:      req.Filename,
	}); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("literature not found or not bound to active project"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to update literature").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "literature updated successfully"})
}

type deleteLiteratureRequest struct {
	ID int64 `json:"id,string" binding:"required"`
}

// DeleteLiterature godoc
// @Summary      删除文献
// @Description  按文献ID删除文献记录、全文文件及其所有项目关联
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      deleteLiteratureRequest  true  "请求参数"
// @Success      200      {object}  map[string]string        "删除成功"
// @Failure      400      {object}  errors.AppError          "参数错误"
// @Failure      401      {object}  errors.AppError          "未认证"
// @Failure      404      {object}  errors.AppError          "文献不存在或未绑定当前激活项目"
// @Failure      500      {object}  errors.AppError          "服务器错误"
// @Security     Bearer
// @Router       /project/delete-literature [post]
func (h *ProjectHandler) DeleteLiterature(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req deleteLiteratureRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.projectService.DeleteLiterature(ctx, userID, req.ID); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("literature not found or not bound to active project"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to delete literature").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "literature deleted successfully"})
}

type literatureDetailRequest struct {
	ID int64 `form:"id" binding:"required"`
}

type literatureListItem struct {
	ID             string `json:"id"`
	OwnerProjectID string `json:"owner_project_id"`
	Title          string `json:"title"`
	Source         string `json:"source"`
	Filename       string `json:"filename"`
	CreatedAt      string `json:"created_at"`
	UpdatedAt      string `json:"updated_at"`
}

type literatureDetailItem struct {
	ID             string    `json:"id"`
	OwnerProjectID string    `json:"owner_project_id"`
	Title          string    `json:"title"`
	Content        string    `json:"content"`
	ContentSource  string    `json:"content_source"`
	Filename       string    `json:"filename"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// GetLiteratureDetail godoc
// @Summary      查询文献详情
// @Description  根据 id 查询文献详情（包含全文 content）
// @Tags         项目
// @Produce      json
// @Param        id          query     int64                true  "文献ID"
// @Success      200         {object}  literatureDetailItem "文献详情"
// @Failure      400         {object}  errors.AppError      "参数错误"
// @Failure      401         {object}  errors.AppError      "未认证"
// @Failure      404         {object}  errors.AppError      "文献不存在或未绑定当前激活项目"
// @Failure      500         {object}  errors.AppError      "服务器错误"
// @Security     Bearer
// @Router       /project/literature-detail [get]
func (h *ProjectHandler) GetLiteratureDetail(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req literatureDetailRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	literature, err := h.projectService.GetLiteratureDetailByID(ctx, userID, req.ID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("literature not found or not bound to active project"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get literature detail").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, newLiteratureDetailItem(strconv.FormatInt(literature.ID, 10), literature))
}

func newLiteratureDetailItem(id string, literature *types.Literature) *literatureDetailItem {
	if literature == nil {
		return nil
	}

	return &literatureDetailItem{
		ID:             id,
		OwnerProjectID: literature.OwnerProjectID,
		Title:          literature.Title,
		Content:        literature.Content,
		ContentSource:  literature.ContentSource,
		Filename:       literature.Filename,
		CreatedAt:      literature.CreatedAt,
		UpdatedAt:      literature.UpdatedAt,
	}
}

func toLiteratureListItem(literature *types.Literature) literatureListItem {
	return literatureListItem{
		ID:             strconv.FormatInt(literature.ID, 10),
		OwnerProjectID: literature.OwnerProjectID,
		Title:          literature.Title,
		Source:         literature.ContentSource,
		Filename:       literature.Filename,
		CreatedAt:      literature.CreatedAt.Format("2006-01-02 15:04:05"),
		UpdatedAt:      literature.UpdatedAt.Format("2006-01-02 15:04:05"),
	}
}

// ListLiterature godoc
// @Summary      查询激活项目文献列表
// @Description  查询当前用户激活项目绑定的文献列表，不返回 content 字段
// @Tags         项目
// @Produce      json
// @Success      200         {array}   literatureListItem "文献列表"
// @Failure      401         {object}  errors.AppError    "未认证"
// @Failure      404         {object}  errors.AppError    "未找到激活项目"
// @Failure      500         {object}  errors.AppError    "服务器错误"
// @Security     Bearer
// @Router       /project/list-literature [get]
func (h *ProjectHandler) ListLiterature(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	literatures, err := h.projectService.ListLiteratureByProjectID(ctx, userID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("active project not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to list literature").WithDetails(err.Error()))
		return
	}

	result := make([]literatureListItem, 0, len(literatures))
	for _, literature := range literatures {
		result = append(result, toLiteratureListItem(literature))
	}

	c.JSON(http.StatusOK, result)
}

type literaturePageRequest struct {
	types.Pagination
}

// PageLiterature godoc
// @Summary      分页查询激活项目文献列表
// @Description  按当前用户激活项目分页查询文献列表，不返回 content 字段
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      handler.literaturePageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /project/list-literature-page [post]
func (h *ProjectHandler) PageLiterature(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req literaturePageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
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

	literatures, total, err := h.projectService.PageLiteratureByProjectID(ctx, userID, &req.Pagination)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("active project not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to page literature").WithDetails(err.Error()))
		return
	}

	result := make([]literatureListItem, 0, len(literatures))
	for _, literature := range literatures {
		result = append(result, toLiteratureListItem(literature))
	}

	c.JSON(http.StatusOK, gin.H{
		"data":       result,
		"total":      total,
		"page":       req.GetPage(),
		"page_size":  req.GetPageSize(),
		"project_id": project.ProjectID,
	})
}

type bindLiteratureRequest struct {
	LiteratureID int64 `json:"literature_id,string" binding:"required"`
}

// BindLiterature godoc
// @Summary      绑定文献到激活项目
// @Description  将已存在的文献绑定到当前用户激活项目（写入 project_literature 中间表）
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      bindLiteratureRequest  true  "请求参数"
// @Success      200      {object}  map[string]string      "绑定成功"
// @Failure      400      {object}  errors.AppError        "参数错误或已绑定"
// @Failure      401      {object}  errors.AppError        "未认证"
// @Failure      404      {object}  errors.AppError        "文献或激活项目不存在"
// @Failure      500      {object}  errors.AppError        "服务器错误"
// @Security     Bearer
// @Router       /project/bind-literature [post]
func (h *ProjectHandler) BindLiterature(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req bindLiteratureRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.projectService.BindLiteratureToProject(ctx, userID, req.LiteratureID); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("literature or active project not found"))
			return
		}
		c.Error(errors.NewBadRequestError(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "literature bound to active project successfully"})
}

type unbindLiteratureRequest struct {
	LiteratureID int64 `json:"literature_id,string" binding:"required"`
}

// UnbindLiterature godoc
// @Summary      移除激活项目中的文献
// @Description  删除当前用户激活项目与文献的关联（仅删除 project_literature 中间表记录）
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      unbindLiteratureRequest  true  "请求参数"
// @Success      200      {object}  map[string]string        "移除成功"
// @Failure      400      {object}  errors.AppError          "参数错误"
// @Failure      401      {object}  errors.AppError          "未认证"
// @Failure      404      {object}  errors.AppError          "关联不存在"
// @Failure      500      {object}  errors.AppError          "服务器错误"
// @Security     Bearer
// @Router       /project/unbind-literature [post]
func (h *ProjectHandler) UnbindLiterature(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req unbindLiteratureRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.projectService.UnbindLiteratureFromProject(ctx, userID, req.LiteratureID); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("literature is not bound to active project"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to unbind literature").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "literature removed from active project successfully"})
}

type literaturePoolListItem struct {
	literatureListItem
	Bound bool `json:"bound"`
}

// PageLiteraturePool godoc
// @Summary      分页查询文献池
// @Description  分页查询所有文献并标记是否已绑定当前用户激活项目，用于绑定文献选择
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      handler.literaturePageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /project/list-literature-pool-page [post]
func (h *ProjectHandler) PageLiteraturePool(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req literaturePageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	items, total, err := h.projectService.PageLiteraturePool(ctx, userID, &req.Pagination)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("active project not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to page literature pool").WithDetails(err.Error()))
		return
	}

	result := make([]literaturePoolListItem, 0, len(items))
	for _, item := range items {
		if item == nil {
			continue
		}
		result = append(result, literaturePoolListItem{
			literatureListItem: toLiteratureListItem(&item.Literature),
			Bound:              item.Bound,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      result,
		"total":     total,
		"page":      req.GetPage(),
		"page_size": req.GetPageSize(),
	})
}
