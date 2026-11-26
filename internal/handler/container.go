package handler

import (
	"context"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/container_runtime/provision"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

type ContainerHandler struct {
	containerService interfaces.ContainerService
	projectService   interfaces.ProjectService
	analysisService  interfaces.AnalysisService
	workflowService  interfaces.WorkflowService
	containerManager *manager.ContainerManager
	terminalRegistry *manager.TerminalSessionRegistry
	provisionManager *provision.Manager
	cfg              *config.Config
}

type containerTemplatePageRequest struct {
	types.Pagination
}

type containerTemplateSpecPageRequest struct {
	types.Pagination
}

type containerTemplateListBySpecRequest struct {
	SpecID int64 `json:"spec_id,string" binding:"required"`
}

type containerInstancePageRequest struct {
	types.Pagination
}

type containerInstancePageItem struct {
	*types.ContainerInstance
	InMonitoringRegistry bool `json:"in_monitoring_registry"`
	RefCount             int  `json:"ref_count"`
}

type containerEventPageRequest struct {
	types.Pagination
}

type outboxEventPageRequest struct {
	types.Pagination
}

type runtimeMonitoringSnapshotItem struct {
	RuntimeID string `json:"runtime_id"`
	RefCount  int    `json:"ref_count"`
}

func NewContainerHandler(containerService interfaces.ContainerService,
	projectService interfaces.ProjectService,
	analysisService interfaces.AnalysisService, workflowService interfaces.WorkflowService,
	containerManager *manager.ContainerManager,
	terminalRegistry *manager.TerminalSessionRegistry,
	provisionManager *provision.Manager,
	cfg *config.Config) *ContainerHandler {
	return &ContainerHandler{
		containerService: containerService,
		analysisService:  analysisService,
		projectService:   projectService,
		workflowService:  workflowService,
		containerManager: containerManager,
		terminalRegistry: terminalRegistry,
		provisionManager: provisionManager,
		cfg:              cfg,
	}
}

// CreateContainerTemplateDefinition godoc
// @Summary      创建容器模板（绑定行）
// @Description  只新增一条绑定行（ContainerTemplateDefinition）：用 spec_id 引用已有的共享运行配置，
// @Description  并写入镜像名 / 拉取策略 / 展示名 / 描述 / 默认标记。返回新建的绑定行。
// @Description  共享运行配置请先通过 /container/template-spec/create 创建。
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.ContainerTemplateDefinition  true  "请求参数"
// @Success      200      {object}  types.ContainerTemplateDefinition
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template-definition/create [post]
func (h *ContainerHandler) CreateContainerTemplateDefinition(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.ContainerTemplateDefinition
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.SpecID == 0 {
		c.Error(errors.NewValidationError("spec_id is required"))
		return
	}
	if strings.TrimSpace(req.ImageName) == "" {
		c.Error(errors.NewValidationError("image_name is required"))
		return
	}

	if err := h.containerService.CreateContainerTemplateDefinition(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to create container template definition")
		return
	}

	item, err := h.containerService.GetContainerTemplateDefinitionByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get container template definition")
		return
	}

	c.JSON(http.StatusOK, item)
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

// UpdateContainerTemplateDefinition godoc
// @Summary      更新容器模板（绑定行）
// @Description  按绑定行 ID（ContainerTemplateDefinition.ID）只更新绑定行本体：
// @Description  image_name / pull_policy / name / description。
// @Description  共享运行配置（命令、资源、端口、环境变量、挂载等）请走 /container/template-spec/update。
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.ContainerTemplateDefinition  true  "请求参数"
// @Success      200      {object}  types.ContainerTemplateDefinition
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template-definition/update [post]
func (h *ContainerHandler) UpdateContainerTemplateDefinition(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.ContainerTemplateDefinition
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.ID == 0 {
		c.Error(errors.NewValidationError("id is required"))
		return
	}
	if strings.TrimSpace(req.ImageName) == "" {
		c.Error(errors.NewValidationError("image_name is required"))
		return
	}

	if err := h.containerService.UpdateContainerTemplateDefinition(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to update container template definition")
		return
	}

	item, err := h.containerService.GetContainerTemplateDefinitionByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get container template definition")
		return
	}

	c.JSON(http.StatusOK, item)
}

// CreateContainerTemplateSpec godoc
// @Summary      创建容器运行配置
// @Description  新增一条共享运行配置（ContainerTemplateSpec，表 go_container_template_spec）。
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.ContainerTemplateSpec  true  "请求参数"
// @Success      200      {object}  types.ContainerTemplateSpec
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template-spec/create [post]
func (h *ContainerHandler) CreateContainerTemplateSpec(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.ContainerTemplateSpec
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		c.Error(errors.NewValidationError("name is required"))
		return
	}

	if err := h.containerService.CreateContainerTemplateSpec(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to create container template spec")
		return
	}

	item, err := h.containerService.GetContainerTemplateSpecByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get container template spec")
		return
	}

	c.JSON(http.StatusOK, item)
}

// GetContainerTemplateSpec godoc
// @Summary      获取容器运行配置
// @Description  按 ID 查询 ContainerTemplateSpec 详情
// @Tags         容器管理
// @Produce      json
// @Param        id       query     integer  true  "主键 ID"
// @Success      200      {object}  types.ContainerTemplateSpec
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template-spec/get [get]
func (h *ContainerHandler) GetContainerTemplateSpec(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.containerService.GetContainerTemplateSpecByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get container template spec")
		return
	}

	c.JSON(http.StatusOK, item)
}

// UpdateContainerTemplateSpec godoc
// @Summary      更新容器运行配置
// @Description  按主键更新 ContainerTemplateSpec（不影响绑定行）。
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.ContainerTemplateSpec  true  "请求参数"
// @Success      200      {object}  types.ContainerTemplateSpec
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template-spec/update [post]
func (h *ContainerHandler) UpdateContainerTemplateSpec(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.ContainerTemplateSpec
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.ID == 0 {
		c.Error(errors.NewValidationError("id is required"))
		return
	}

	if err := h.containerService.UpdateContainerTemplateSpec(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to update container template spec")
		return
	}

	item, err := h.containerService.GetContainerTemplateSpecByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get container template spec")
		return
	}

	c.JSON(http.StatusOK, item)
}

// DeleteContainerTemplateSpec godoc
// @Summary      删除容器运行配置
// @Description  按主键删除 ContainerTemplateSpec；仍被绑定行引用时返回 409。
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      idBody  true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      409      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template-spec/delete [post]
func (h *ContainerHandler) DeleteContainerTemplateSpec(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.containerService.DeleteContainerTemplateSpec(c.Request.Context(), req.ID); err != nil {
		handleDataError(c, err, "failed to delete container template spec")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "container template spec deleted successfully"})
}

// ListContainerTemplateSpec godoc
// @Summary      容器运行配置列表
// @Description  查询全部 ContainerTemplateSpec
// @Tags         容器管理
// @Produce      json
// @Success      200      {array}   types.ContainerTemplateSpec
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template-spec/list [get]
func (h *ContainerHandler) ListContainerTemplateSpec(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	items, err := h.containerService.ListContainerTemplateSpec(c.Request.Context())
	if err != nil {
		handleDataError(c, err, "failed to list container template spec")
		return
	}

	c.JSON(http.StatusOK, items)
}

// PageContainerTemplateSpec godoc
// @Summary      分页查询容器运行配置
// @Description  分页查询 ContainerTemplateSpec 列表
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      containerTemplateSpecPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template-spec/list-by-page [post]
func (h *ContainerHandler) PageContainerTemplateSpec(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req containerTemplateSpecPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	result, err := h.containerService.PageContainerTemplateSpec(c.Request.Context(), &req.Pagination)
	if err != nil {
		handleDataError(c, err, "failed to page container template spec")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      result.Data,
		"total":     result.Total,
		"page":      result.Page,
		"page_size": result.PageSize,
	})
}

// DeleteContainerTemplateDefinition godoc
// @Summary      删除容器模板（绑定行）
// @Description  按绑定行 ID（ContainerTemplateDefinition.ID）删除该镜像绑定；
// @Description  共享运行配置（ContainerTemplateSpec）不受影响。
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
// @Router       /container/template-definition/delete [post]
func (h *ContainerHandler) DeleteContainerTemplateDefinition(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.containerService.DeleteContainerTemplateDefinition(c.Request.Context(), req.ID); err != nil {
		handleDataError(c, err, "failed to delete container template definition")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "container template definition deleted successfully"})
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

// ListContainerTemplateBySpec godoc
// @Summary      按共享配置列出容器模板// @Description  列出同一套 ContainerTemplateSpec 下绑定不同镜像的所有对外模板（一个配置 × 多个镜像）
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      containerTemplateListBySpecRequest  true  "请求参数"
// @Success      200      {array}   types.ContainerTemplate
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template/list-by-spec [post]
func (h *ContainerHandler) ListContainerTemplateBySpec(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req containerTemplateListBySpecRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.SpecID == 0 {
		c.Error(errors.NewValidationError("spec_id is required"))
		return
	}

	items, err := h.containerService.ListContainerTemplateBySpecID(c.Request.Context(), req.SpecID)
	if err != nil {
		handleDataError(c, err, "failed to list container template by spec")
		return
	}

	c.JSON(http.StatusOK, items)
}

// ExportContainerTemplate godoc
// @Summary      导出容器模板
// @Description  导出单条容器模板为统一的平铺格式（与 script.json / workflow.json 完全相同）：
// @Description  container_template_specs / container_template_definitions 两个列表，
// @Description  两者通过主键互相引用（definition.spec_id），且都不含时间字段。
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      idBody  true  "请求参数"
// @Success      200      {object}  types.ContainerTemplateBundle
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template/export [post]
func (h *ContainerHandler) ExportContainerTemplate(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.ID == 0 {
		c.Error(errors.NewValidationError("id is required"))
		return
	}

	bundle, err := h.containerService.ExportContainerTemplate(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to export container template")
		return
	}

	c.JSON(http.StatusOK, bundle)
}

// ImportContainerTemplate godoc
// @Summary      导入容器模板
// @Description  导入统一平铺格式（与 /container/template/export、script.json / workflow.json 同构）：
// @Description  container_template_specs / container_template_definitions。
// @Description  两张表都按主键 upsert：id 已存在则更新，不存在则按同一 id 新增；安装顺序固定为
// @Description  「运行配置 → 绑定行」。
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      types.ContainerTemplateBundle  true  "请求参数"
// @Success      200      {object}  types.ContainerTemplateBundle
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template/import [post]
func (h *ContainerHandler) ImportContainerTemplate(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.ContainerTemplateBundle
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if len(req.ContainerTemplateSpecs) == 0 && len(req.ContainerTemplateDefinitions) == 0 {
		c.Error(errors.NewValidationError("container_template_specs / container_template_definitions are all empty"))
		return
	}

	bundle, err := h.containerService.ImportContainerTemplate(c.Request.Context(), &req)
	if err != nil {
		handleDataError(c, err, "failed to import container template")
		return
	}

	c.JSON(http.StatusOK, bundle)
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

	if items, ok := result.Data.([]*types.ContainerInstance); ok {
		snapshot := containerruntime.RuntimeMonitoringSnapshot()
		dtos := make([]*containerInstancePageItem, 0, len(items))
		for _, inst := range items {
			if inst == nil {
				continue
			}
			runtimeID := strings.TrimSpace(inst.RuntimeID)
			dtos = append(dtos, &containerInstancePageItem{
				ContainerInstance:    inst,
				InMonitoringRegistry: snapshot[runtimeID] > 0,
				RefCount:             snapshot[runtimeID],
			})
		}
		result.Data = dtos
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
// func (h *ContainerHandler) PageContainerEvent(c *gin.Context) {
// 	if _, ok := getCurrentUserID(c); !ok {
// 		return
// 	}

// 	var req containerEventPageRequest
// 	if err := c.ShouldBindJSON(&req); err != nil {
// 		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
// 		return
// 	}

// 	result, err := h.containerService.PageContainerEvent(c.Request.Context(), &req.Pagination)
// 	if err != nil {
// 		handleDataError(c, err, "failed to page container event")
// 		return
// 	}

// 	c.JSON(http.StatusOK, gin.H{
// 		"data":      result.Data,
// 		"total":     result.Total,
// 		"page":      result.Page,
// 		"page_size": result.PageSize,
// 	})
// }

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

// DescribeContainerInstance godoc
// @Summary      容器实例详情
// @Description  按 ContainerInstance ID 从运行时获取 inspect/describe 详情（兼容 docker 与 k8s）
// @Tags         容器管理
// @Produce      json
// @Param        id  query  int64  true  "容器实例 ID"
// @Success      200  {object}  containerruntime.RuntimeDescription
// @Failure      401  {object}  errors.AppError
// @Failure      500  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/instance/describe [get]
func (h *ContainerHandler) DescribeContainerInstance(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	id, err := strconv.ParseInt(c.Query("id"), 10, 64)
	if err != nil || id <= 0 {
		c.Error(errors.NewValidationError("invalid container instance id").WithDetails(err.Error()))
		return
	}

	if h.containerManager == nil {
		c.Error(errors.NewInternalServerError("container manager is not available"))
		return
	}

	desc, err := h.containerManager.Describe(c.Request.Context(), id)
	if err != nil {
		handleDataError(c, err, "failed to describe container instance")
		return
	}

	c.JSON(http.StatusOK, desc)
}

type cleanupAllContainersRequest struct {
	// Confirm 必须为 true：该接口会删除全部容器实例、会话与网关路由，属于不可逆操作。
	Confirm bool `json:"confirm"`
}

// CleanupAllContainers godoc
// @Summary      清理全部容器资源
// @Description  删除系统中的全部容器资源（运行中 + 已停止）：
// @Description  清空网关路由、同步删除所有 ContainerInstance 及其 docker / k8s 运行时容器，
// @Description  并删除全部 AppSession（Web 应用会话 + 终端会话）。必须显式传 confirm=true。
// @Description  清理是尽力而为的，单条失败不中断，失败原因汇总在返回的 errors 里。
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body      cleanupAllContainersRequest  true  "确认参数"
// @Success      200      {object}  types.ContainerCleanupResult
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /container/instance/cleanup-all [post]
func (h *ContainerHandler) CleanupAllContainers(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req cleanupAllContainersRequest
	// 允许空 body：缺省的 confirm=false 会直接拒绝，避免误触删除全部。
	if c.Request.ContentLength > 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
			return
		}
	}
	if !req.Confirm {
		c.Error(errors.NewBadRequestError("cleanup all containers requires confirm=true"))
		return
	}

	// 先关闭进程内的终端会话（PTY），避免容器删除后仍残留 shell 句柄。
	if h.terminalRegistry != nil {
		if closed := h.terminalRegistry.CloseAll(); closed > 0 {
			logger.Infof(c.Request.Context(), "[ContainerHandler] cleanup-all closed %d terminal session(s)", closed)
		}
	}

	result, err := h.containerService.DeleteAllContainerResources(c.Request.Context())
	if err != nil {
		// 容器侧是尽力而为的；这里只会在 AppSession 整批删除失败时返回错误，
		// 调用方可以放心重试（重复执行是幂等的）。
		handleDataError(c, err, "failed to cleanup all containers")
		return
	}

	c.JSON(http.StatusOK, result)
}

// ListRuntimeMonitoringSnapshot godoc
// @Summary      运行时监控快照
// @Description  返回全局运行时监控表快照（runtime_id 与引用计数）
// @Tags         容器管理
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Failure      401  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/runtime/monitoring/list [get]
func (h *ContainerHandler) ListRuntimeMonitoringSnapshot(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	raw := containerruntime.RuntimeMonitoringSnapshot()
	items := make([]*runtimeMonitoringSnapshotItem, 0, len(raw))
	for runtimeID, refCount := range raw {
		if strings.TrimSpace(runtimeID) == "" {
			continue
		}
		if refCount <= 0 {
			continue
		}
		items = append(items, &runtimeMonitoringSnapshotItem{RuntimeID: runtimeID, RefCount: refCount})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].RefCount == items[j].RefCount {
			return items[i].RuntimeID < items[j].RuntimeID
		}
		return items[i].RefCount > items[j].RefCount
	})

	c.JSON(http.StatusOK, gin.H{
		"data":  items,
		"total": len(items),
	})
}

// GetQueueStatus godoc
// @Summary      容器队列状态
// @Description  返回队列的运行中/等待中/最大并发/最大排队数量
// @Tags         容器管理
// @Produce      json
// @Success      200  {object}  map[string]interface{}
// @Failure      401  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/queue/status [get]
func (h *ContainerHandler) GetQueueStatus(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	if h.containerManager == nil {
		c.JSON(http.StatusOK, gin.H{
			"active_count":    0,
			"pending_count":   0,
			"max_concurrency": 0,
			"max_pending":     0,
			"queue_enabled":   false,
		})
		return
	}

	status, err := h.containerManager.QueueStatus(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"active_count":    -1,
			"pending_count":   -1,
			"max_concurrency": h.containerManager.GetMaxConcurrency(),
			"max_pending":     h.containerManager.CreateQueueMaxPending(),
			"queue_enabled":   true,
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"active_count":    status.ActiveCount,
		"pending_count":   status.PendingCount,
		"max_concurrency": status.MaxConcurrency,
		"max_pending":     status.MaxPending,
		"queue_enabled":   true,
	})
}

// runtimeHealthCheckTimeout 是运行时依赖探测的超时上限，避免 docker / apiserver
// 不可达时检测接口长时间挂住。
const runtimeHealthCheckTimeout = 5 * time.Second

// GetContainerRuntimeInfo godoc
// @Summary      容器运行时环境信息
// @Description  返回默认运行时信息：运行时名称由容器实际执行时同一份逻辑解析
// @Description  （manager.ContainerLifecycle.ResolveRuntimeName：container.default_runtime →
// @Description  container.runtimes[0] → 唯一已注册运行时 → docker），并探测其依赖
// @Description  （docker daemon Ping、k8s apiserver ServerVersion）。
// @Description  同时返回宿主操作系统 / 架构、实际连接地址（已脱敏）与对端版本，
// @Description  供前端在页面上展示运行环境。
// @Tags         容器管理
// @Produce      json
// @Success      200  {object}  containerruntime.RuntimeHealth
// @Failure      401  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/runtime/info [get]
func (h *ContainerHandler) GetContainerRuntimeInfo(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	// 与容器实际执行完全同源：直接调用 ContainerLifecycle.GetRuntime，
	// 名称解析（ResolveRuntimeName）与「注册表中查不到」的报错都保持一字不差，
	// 便于用户按接口返回定位真实创建失败的原因。
	if h.containerManager == nil {
		c.Error(errors.NewInternalServerError("container manager is not available"))
		return
	}

	result := containerruntime.RuntimeHealth{
		HostOS:   runtime.GOOS,
		HostArch: runtime.GOARCH,
		// 运行时预置开关与 bash 执行位置属于「配置态」信息，与运行时是否可用无关，
		// 因此无论探测成功与否都返回，前端据此决定是否展示安装向导入口。
		ProvisionEnabled:        config.RuntimeProvisionEnabled(h.cfg),
		BashExecutionMode:       config.BashExecutionModeHost,
		BashContainerTemplateID: config.DefaultBashContainerTemplateID,
	}
	if h.cfg != nil && h.cfg.Container != nil {
		if mode, err := config.NormalizeBashExecutionMode(h.cfg.Container.BashExecutionMode); err == nil {
			result.BashExecutionMode = mode
		}
		if h.cfg.Container.BashContainerTemplateID > 0 {
			result.BashContainerTemplateID = h.cfg.Container.BashContainerTemplateID
		}
	}

	rt, err := h.containerManager.GetRuntime()
	if rt == nil || err != nil {
		msg := "container runtime is not available"
		if err != nil {
			msg = err.Error()
		}
		result.Name = h.containerManager.ResolveRuntimeName()
		result.Error = msg
		c.JSON(http.StatusOK, result)
		return
	}

	result.Name = rt.Name()
	result.Registered = true
	result.Healthy = true

	ctx, cancel := context.WithTimeout(c.Request.Context(), runtimeHealthCheckTimeout)
	defer cancel()

	if checker, ok := rt.(containerruntime.RuntimeHealthChecker); ok {
		result.Checkable = true
		if err := checker.HealthCheck(ctx); err != nil {
			result.Healthy = false
			result.Error = err.Error()
		}
	}

	// 连接地址与对端版本属于「附加信息」：获取失败不影响健康结论。
	if reporter, ok := rt.(containerruntime.RuntimeEndpointReporter); ok {
		result.Endpoint, result.EndpointSource = reporter.Endpoint()
	}
	if reporter, ok := rt.(containerruntime.RuntimeVersionReporter); ok {
		if version, verr := reporter.ServerVersion(ctx); verr == nil {
			result.ServerVersion = version
		}
	}

	c.JSON(http.StatusOK, result)
}
