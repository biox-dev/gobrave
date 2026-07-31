package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gobravedev/gobrave/internal/config"
	"github.com/gobravedev/gobrave/internal/errors"
	"github.com/gobravedev/gobrave/internal/manager"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
)

type ContainerHandler struct {
	containerService interfaces.ContainerService
	projectService   interfaces.ProjectService
	analysisService  interfaces.AnalysisService
	workflowService  interfaces.WorkflowService
	createWorker     *manager.ContainerCreateWorker
	cfg              *config.Config
}

type appSessionCreateRequest struct {
	ContainerTemplateID int64 `json:"container_template_id,string" binding:"required"`
	// ProjectID           string `json:"project_id" binding:"required"`
	Name string `json:"name"`
}

type appSessionCreateByAnalysisNodeRequest struct {
	AnalysisNodeID int64  `json:"analysis_node_id,string" binding:"required"`
	Name           string `json:"name"`
}

type appSessionIDBody struct {
	ID int64 `json:"id,string" binding:"required"`
}

type appSessionIDQuery struct {
	ID int64 `form:"id" binding:"required"`
}

type containerImagePageRequest struct {
	types.Pagination
}

type containerTemplatePageRequest struct {
	types.Pagination
}

type appSessionPageRequest struct {
	types.Pagination
	Query          *appSessionPageQuery `json:"query"`
	AnalysisNodeID string               `json:"analysis_node_id"`
	// ProjectID      string               `json:"project_id"`
}

type appSessionPageQuery struct {
	AnalysisNodeID string `json:"analysis_node_id"`
	ProjectID      string `json:"project_id"`
}

type containerInstancePageRequest struct {
	types.Pagination
}

type containerEventPageRequest struct {
	types.Pagination
}

type outboxEventPageRequest struct {
	types.Pagination
}

type appSessionPageItem struct {
	*types.AppSession
	PathPrefix string `json:"path_prefix"`
	NodeName   string `json:"node_name"`
}

func NewContainerHandler(containerService interfaces.ContainerService,
	projectService interfaces.ProjectService,
	analysisService interfaces.AnalysisService, workflowService interfaces.WorkflowService,
	createWorker *manager.ContainerCreateWorker,
	cfg *config.Config) *ContainerHandler {
	return &ContainerHandler{
		containerService: containerService,
		analysisService:  analysisService,
		projectService:   projectService,
		workflowService:  workflowService,
		createWorker:     createWorker,
		cfg:              cfg,
	}
}

// CreateContainerImage godoc
// @Summary      创建容器镜像
// @Description  创建 ContainerImage 记录
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.ContainerImage  true  "请求参数"
// @Success      200      {object}  types.ContainerImage
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/image/create [post]
func (h *ContainerHandler) CreateContainerImage(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.ContainerImage
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.containerService.CreateContainerImage(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to create container image")
		return
	}

	c.JSON(http.StatusOK, req)
}

// GetContainerImage godoc
// @Summary      获取容器镜像
// @Description  按 ID 查询 ContainerImage 详情
// @Tags         容器管理
// @Produce      json
// @Param        id       query     integer               true  "主键 ID"
// @Success      200      {object}  types.ContainerImage
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/image/get [get]
func (h *ContainerHandler) GetContainerImage(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.containerService.GetContainerImageByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get container image")
		return
	}

	c.JSON(http.StatusOK, item)
}

// UpdateContainerImage godoc
// @Summary      更新容器镜像
// @Description  按 ID 更新 ContainerImage 记录
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.ContainerImage  true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/image/update [post]
func (h *ContainerHandler) UpdateContainerImage(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.ContainerImage
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.ID == 0 {
		c.Error(errors.NewValidationError("id is required"))
		return
	}

	if err := h.containerService.UpdateContainerImage(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to update container image")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "container image updated successfully"})
}

// DeleteContainerImage godoc
// @Summary      删除容器镜像
// @Description  按 ID 删除 ContainerImage 记录
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      idBody               true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/image/delete [post]
func (h *ContainerHandler) DeleteContainerImage(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.containerService.DeleteContainerImage(c.Request.Context(), req.ID); err != nil {
		handleDataError(c, err, "failed to delete container image")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "container image deleted successfully"})
}

// ListContainerImage godoc
// @Summary      容器镜像列表
// @Description  查询 ContainerImage 列表
// @Tags         容器管理
// @Produce      json
// @Success      200      {array}   types.ContainerImage
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/image/list [get]
func (h *ContainerHandler) ListContainerImage(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	items, err := h.containerService.ListContainerImage(c.Request.Context())
	if err != nil {
		handleDataError(c, err, "failed to list container image")
		return
	}

	c.JSON(http.StatusOK, items)
}

// PageContainerImage godoc
// @Summary      分页查询容器镜像
// @Description  分页查询 ContainerImage 列表
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      containerImagePageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/image/list-by-page [post]
func (h *ContainerHandler) PageContainerImage(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req containerImagePageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	result, err := h.containerService.PageContainerImage(c.Request.Context(), &req.Pagination)
	if err != nil {
		handleDataError(c, err, "failed to page container image")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      result.Data,
		"total":     result.Total,
		"page":      result.Page,
		"page_size": result.PageSize,
	})
}

// CreateContainerTemplate godoc
// @Summary      创建容器模板
// @Description  创建 ContainerTemplate 记录（手动校验 ImageID，不使用 GORM 关系维护）
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.ContainerTemplate  true  "请求参数"
// @Success      200      {object}  types.ContainerTemplate
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template/create [post]
func (h *ContainerHandler) CreateContainerTemplate(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.ContainerTemplate
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.containerService.CreateContainerTemplate(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to create container template")
		return
	}

	c.JSON(http.StatusOK, req)
}

// GetContainerTemplate godoc
// @Summary      获取容器模板
// @Description  按 ID 查询 ContainerTemplate 详情
// @Tags         容器管理
// @Produce      json
// @Param        id       query     integer                  true  "主键 ID"
// @Success      200      {object}  types.ContainerTemplate
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template/get [get]
func (h *ContainerHandler) GetContainerTemplate(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.containerService.GetContainerTemplateByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get container template")
		return
	}

	c.JSON(http.StatusOK, item)
}

// UpdateContainerTemplate godoc
// @Summary      更新容器模板
// @Description  按 ID 更新 ContainerTemplate 记录（手动校验 ImageID，不使用 GORM 关系维护）
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.ContainerTemplate  true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template/update [post]
func (h *ContainerHandler) UpdateContainerTemplate(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.ContainerTemplate
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.ID == 0 {
		c.Error(errors.NewValidationError("id is required"))
		return
	}

	if err := h.containerService.UpdateContainerTemplate(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to update container template")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "container template updated successfully"})
}

// DeleteContainerTemplate godoc
// @Summary      删除容器模板
// @Description  按 ID 删除 ContainerTemplate 记录
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      idBody                 true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template/delete [post]
func (h *ContainerHandler) DeleteContainerTemplate(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.containerService.DeleteContainerTemplate(c.Request.Context(), req.ID); err != nil {
		handleDataError(c, err, "failed to delete container template")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "container template deleted successfully"})
}

// ListContainerTemplate godoc
// @Summary      容器模板列表
// @Description  查询 ContainerTemplate 列表
// @Tags         容器管理
// @Produce      json
// @Success      200      {array}   types.ContainerTemplate
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template/list [get]
func (h *ContainerHandler) ListContainerTemplate(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	items, err := h.containerService.ListContainerTemplate(c.Request.Context())
	if err != nil {
		handleDataError(c, err, "failed to list container template")
		return
	}

	c.JSON(http.StatusOK, items)
}

// PageContainerTemplate godoc
// @Summary      分页查询容器模板
// @Description  分页查询 ContainerTemplate 列表
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      containerTemplatePageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template/list-by-page [post]
func (h *ContainerHandler) PageContainerTemplate(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req containerTemplatePageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	result, err := h.containerService.PageContainerTemplate(c.Request.Context(), &req.Pagination)
	if err != nil {
		handleDataError(c, err, "failed to page container template")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      result.Data,
		"total":     result.Total,
		"page":      result.Page,
		"page_size": result.PageSize,
	})
}

// CreateAppSession godoc
// @Summary      创建应用会话
// @Description  输入 ContainerTemplateID + 当前用户 + ProjectID 创建 AppSession，并通过 ContainerManager 创建并绑定容器
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      appSessionCreateRequest  true  "请求参数"
// @Success      200      {object}  types.AppSession
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/app-session/create [post]
func (h *ContainerHandler) CreateAppSession(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}
	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		c.Error(errors.NewValidationError("project not found").WithDetails(err.Error()))
		return
	}
	var req appSessionCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.containerService.CreateAppSessionByTemplate(c.Request.Context(), userID, project.ID, req.ContainerTemplateID, req.Name)
	if err != nil {
		handleDataError(c, err, "failed to create app session")
		return
	}

	c.JSON(http.StatusOK, item)
}

// CreateAppSessionByAnalysisNode godoc
// @Summary      通过分析节点创建应用会话
// @Description  依据 analysis_node_id 查询 AnalysisNode/Analysis/Module，自动推导 ProjectID、ContainerTemplateID、WorkspacePath 并创建 AppSession
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      appSessionCreateByAnalysisNodeRequest  true  "请求参数"
// @Success      200      {object}  types.AppSession
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/app-session/create-by-analysis-node [post]
func (h *ContainerHandler) CreateAppSessionByAnalysisNode(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req appSessionCreateByAnalysisNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	analysisNode, err := h.analysisService.GetAnalysisNodeByID(c.Request.Context(), req.AnalysisNodeID)
	if err != nil {
		handleDataError(c, err, "failed to get analysis node")
		return
	}

	projectID := analysisNode.ProjectID
	if projectID == 0 {
		analysisItem, err := h.analysisService.GetAnalysisByID(c.Request.Context(), analysisNode.AnalysisID)
		if err != nil {
			handleDataError(c, err, "failed to get analysis")
			return
		}
		projectID = analysisItem.ProjectID

	}

	scriptItem, err := h.workflowService.GetScriptByID(c.Request.Context(), analysisNode.ScriptID)
	if err != nil {
		handleDataError(c, err, "failed to get script")
		return
	}
	if scriptItem.ContainerTemplateID == 0 {
		c.Error(errors.NewValidationError("script container_template_id is required"))
		return
	}

	item, err := h.containerService.CreateAppSessionByTemplateForAnalysisNode(
		c.Request.Context(),
		userID,
		projectID,
		scriptItem.ContainerTemplateID,
		req.Name,
		int64(analysisNode.ID),
		analysisNode.WorkspaceDir,
	)
	if err != nil {
		handleDataError(c, err, "failed to create app session")
		return
	}

	c.JSON(http.StatusOK, item)
}

// StartAppSession godoc
// @Summary      启动应用会话
// @Description  通过 ContainerManager 启动 AppSession 绑定容器
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      appSessionIDBody        true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/app-session/start [post]
func (h *ContainerHandler) StartAppSession(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req appSessionIDBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.containerService.StartAppSession(c.Request.Context(), userID, req.ID); err != nil {
		handleDataError(c, err, "failed to start app session")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "app session started successfully"})
}

// StopAppSession godoc
// @Summary      停止应用会话
// @Description  通过 ContainerManager 停止 AppSession 绑定容器
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      appSessionIDBody        true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/app-session/stop [post]
func (h *ContainerHandler) StopAppSession(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req appSessionIDBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.containerService.StopAppSession(c.Request.Context(), userID, req.ID); err != nil {
		handleDataError(c, err, "failed to stop app session")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "app session stopped successfully"})
}

// DeleteAppSession godoc
// @Summary      删除应用会话
// @Description  通过 ContainerManager 删除绑定容器，并删除 AppSession
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      appSessionIDBody        true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/app-session/delete [post]
func (h *ContainerHandler) DeleteAppSession(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req appSessionIDBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.containerService.DeleteAppSession(c.Request.Context(), userID, req.ID); err != nil {
		handleDataError(c, err, "failed to delete app session")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "app session deleted successfully"})
}

// GetAppSession godoc
// @Summary      获取应用会话
// @Description  按 ID 查询当前用户的 AppSession 详情
// @Tags         容器管理
// @Produce      json
// @Param        id       query     integer               true  "主键 ID"
// @Success      200      {object}  types.AppSession
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/app-session/get [get]
func (h *ContainerHandler) GetAppSession(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req appSessionIDQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.containerService.GetAppSessionByID(c.Request.Context(), userID, req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get app session")
		return
	}

	c.JSON(http.StatusOK, item)
}

// ListAppSession godoc
// @Summary      应用会话列表
// @Description  查询当前用户的 AppSession 列表
// @Tags         容器管理
// @Produce      json
// @Success      200      {array}   types.AppSession
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/app-session/list [get]
func (h *ContainerHandler) ListAppSession(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	items, err := h.containerService.ListAppSessionByUserID(c.Request.Context(), userID)
	if err != nil {
		handleDataError(c, err, "failed to list app session")
		return
	}

	c.JSON(http.StatusOK, items)
}

// PageAppSession godoc
// @Summary      分页查询应用会话
// @Description  按当前用户分页查询 AppSession 列表
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      appSessionPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/app-session/list-by-page [post]
func (h *ContainerHandler) PageAppSession(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}
	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		c.Error(err)
		return
	}
	var req appSessionPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	query, err := buildAppSessionPageQuery(&req)
	if err != nil {
		c.Error(err)
		return
	}
	query.ProjectID = &project.ID

	result, err := h.containerService.PageAppSessionByUserID(c.Request.Context(), userID, &req.Pagination, query)
	if err != nil {
		handleDataError(c, err, "failed to page app session")
		return
	}

	if items, ok := result.Data.([]*types.AppSession); ok {
		nodeNameBySessionID := map[int64]string{}
		ownerIDs := make([]int64, 0, len(items))
		for _, item := range items {
			if item == nil || item.ID == 0 {
				continue
			}
			ownerIDs = append(ownerIDs, item.ID)
		}
		instances, err := h.containerService.ListContainerInstanceByOwnerTypeAndOwnerIDs(c.Request.Context(), types.ContainerOwnerAppSession, ownerIDs)
		if err == nil {
			for _, inst := range instances {
				if inst == nil || inst.OwnerID == 0 {
					continue
				}
				if _, exists := nodeNameBySessionID[inst.OwnerID]; exists {
					continue
				}
				nodeNameBySessionID[inst.OwnerID] = strings.TrimSpace(inst.RuntimeNodeName)
			}
		}

		pageItems := make([]*appSessionPageItem, 0, len(items))
		for _, item := range items {
			if item == nil {
				continue
			}
			pageItems = append(pageItems, &appSessionPageItem{
				AppSession: item,
				PathPrefix: fmt.Sprintf("%s/%s/%d", config.ResolveAppsPathPrefix(h.cfg), item.AppType, item.ID),
				NodeName:   nodeNameBySessionID[item.ID],
			})
		}
		result.Data = pageItems
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      result.Data,
		"total":     result.Total,
		"page":      result.Page,
		"page_size": result.PageSize,
	})
}

func buildAppSessionPageQuery(req *appSessionPageRequest) (*types.AppSessionPageQuery, error) {
	if req == nil {
		return nil, nil
	}

	rawAnalysisNodeID := strings.TrimSpace(req.AnalysisNodeID)
	if req.Query != nil && strings.TrimSpace(req.Query.AnalysisNodeID) != "" {
		rawAnalysisNodeID = strings.TrimSpace(req.Query.AnalysisNodeID)
	}
	// rawProjectID := strings.TrimSpace(req.ProjectID)
	// if req.Query != nil && strings.TrimSpace(req.Query.ProjectID) != "" {
	// 	rawProjectID = req.Query.ProjectID
	// }

	// if rawAnalysisNodeID == "" && rawProjectID == "" {
	// 	return nil, nil
	// }

	query := &types.AppSessionPageQuery{}
	// if rawProjectID != "" {
	// 	query.ProjectID = &rawProjectID
	// }

	if rawAnalysisNodeID == "" {
		return query, nil
	}

	parsedAnalysisNodeID, err := strconv.ParseInt(rawAnalysisNodeID, 10, 64)
	if err != nil || parsedAnalysisNodeID <= 0 {
		return nil, errors.NewValidationError("invalid query.analysis_node_id")
	}

	query.AnalysisNodeID = &parsedAnalysisNodeID

	return query, nil
}

// PageContainerInstance godoc
// @Summary      分页查询容器实例
// @Description  分页查询 ContainerInstance 列表
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      containerInstancePageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/instance/list-by-page [post]
func (h *ContainerHandler) PageContainerInstance(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req containerInstancePageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	result, err := h.containerService.PageContainerInstance(c.Request.Context(), &req.Pagination)
	if err != nil {
		handleDataError(c, err, "failed to page container instance")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      result.Data,
		"total":     result.Total,
		"page":      result.Page,
		"page_size": result.PageSize,
	})
}

// PageContainerEvent godoc
// @Summary      分页查询容器事件
// @Description  分页查询 ContainerEvent 列表
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      containerEventPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/event/list-by-page [post]
func (h *ContainerHandler) PageContainerEvent(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req containerEventPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	result, err := h.containerService.PageContainerEvent(c.Request.Context(), &req.Pagination)
	if err != nil {
		handleDataError(c, err, "failed to page container event")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      result.Data,
		"total":     result.Total,
		"page":      result.Page,
		"page_size": result.PageSize,
	})
}

// PageOutboxEvent godoc
// @Summary      分页查询事件出箱
// @Description  分页查询 OutboxEvent 列表
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      outboxEventPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/outbox/list-by-page [post]
func (h *ContainerHandler) PageOutboxEvent(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req outboxEventPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	result, err := h.containerService.PageOutboxEvent(c.Request.Context(), &req.Pagination)
	if err != nil {
		handleDataError(c, err, "failed to page outbox event")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      result.Data,
		"total":     result.Total,
		"page":      result.Page,
		"page_size": result.PageSize,
	})
}

// GetCreateQueueStatus godoc
// @Summary      容器创建队列状态
// @Description  返回创建队列的运行中/等待中/最大并发/最大排队数量
// @Tags         容器管理
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Failure      401  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/create-queue/status [get]
func (h *ContainerHandler) GetCreateQueueStatus(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	if h.createWorker == nil {
		c.JSON(http.StatusOK, gin.H{
			"active_count":    0,
			"pending_count":   0,
			"max_concurrency": 0,
			"max_pending":     0,
			"queue_enabled":   false,
		})
		return
	}

	pending, err := h.createWorker.PendingCount(c.Request.Context())
	if err != nil {
		pending = -1
	}

	c.JSON(http.StatusOK, gin.H{
		"active_count":    h.createWorker.ActiveCount(),
		"pending_count":   pending,
		"max_concurrency": h.createWorker.MaxConcurrency(),
		"max_pending":     h.createWorker.MaxPending(),
		"queue_enabled":   true,
	})
}
