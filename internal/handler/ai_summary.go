package handler

import (
	stderrs "errors"
	"net/http"
	"strconv"

	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AISummaryHandler 处理 AI 摘要相关接口。
type AISummaryHandler struct {
	aiSummaryService interfaces.AISummaryService
	// projectService 用于把当前登录用户解析为其激活项目，供按项目查询摘要使用。
	projectService interfaces.ProjectService
}

func NewAISummaryHandler(aiSummaryService interfaces.AISummaryService, projectService interfaces.ProjectService) *AISummaryHandler {
	return &AISummaryHandler{
		aiSummaryService: aiSummaryService,
		projectService:   projectService,
	}
}

type createAISummaryRequest struct {
	OwnerID   int64                  `json:"owner_id,string" binding:"required"`
	OwnerType types.SummaryOwnerType `json:"owner_type" binding:"required"`
	// Profile 生成摘要使用的 Agent Profile 名称（为空则使用内置 summary Profile）。
	Profile string `json:"profile"`
}

type listAISummaryRequest struct {
	OwnerID   int64                  `form:"owner_id" binding:"required"`
	OwnerType types.SummaryOwnerType `form:"owner_type" binding:"required"`
}

type aiSummaryByProjectPageRequest struct {
	types.Pagination
}

// aiSummaryListItem 是 AI 摘要分页列表项：刻意不包含 content（正文可能很大），
// 正文与 Prefix 由详情接口（GetAISummary）按 ID 返回。
type aiSummaryListItem struct {
	ID        string                 `json:"id"`
	OwnerID   string                 `json:"owner_id"`
	OwnerType types.SummaryOwnerType `json:"owner_type"`
	ProjectID string                 `json:"project_id"`
	Title     string                 `json:"title"`
	Status    types.SummaryStatus    `json:"status"`
	Profile   string                 `json:"profile"`
	TaskID    string                 `json:"task_id"`
	CreatedAt string                 `json:"created_at"`
	UpdatedAt string                 `json:"updated_at"`
}

type aiSummaryInputRequest struct {
	OwnerID   int64                  `form:"owner_id" binding:"required"`
	OwnerType types.SummaryOwnerType `form:"owner_type" binding:"required"`
}

type updateAISummaryRequest struct {
	ID      int64   `json:"id,string" binding:"required"`
	Title   *string `json:"title"`
	Content *string `json:"content"`
	// Profile 生成摘要使用的 Agent Profile 名称；传入空串表示改回内置 summary Profile。
	Profile *string `json:"profile"`
}

// CreateAISummary godoc
// @Summary      创建 AI 摘要
// @Description  根据 OwnerID/OwnerType 创建摘要记录（可指定 Agent Profile），并异步触发 LLM 生成摘要
// @Tags         AI摘要
// @Accept       json
// @Produce      json
// @Param        request  body      createAISummaryRequest  true  "请求参数"
// @Success      200      {object}  types.AISummary
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /ai-summary/create [post]
func (h *AISummaryHandler) CreateAISummary(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req createAISummaryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	summary, err := h.aiSummaryService.CreateAISummary(c.Request.Context(), req.OwnerType, req.OwnerID, req.Profile)
	if err != nil {
		handleDataError(c, err, "failed to create ai summary")
		return
	}

	c.JSON(http.StatusOK, summary)
}

// RegenerateAISummary godoc
// @Summary      重新生成 AI 摘要
// @Description  按摘要 ID 重置状态并异步重新触发 LLM 生成摘要
// @Tags         AI摘要
// @Accept       json
// @Produce      json
// @Param        request  body      idBody  true  "请求参数"
// @Success      200      {object}  types.AISummary
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /ai-summary/regenerate [post]
func (h *AISummaryHandler) RegenerateAISummary(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	summary, err := h.aiSummaryService.RegenerateAISummary(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to regenerate ai summary")
		return
	}

	c.JSON(http.StatusOK, summary)
}

// GetAISummary godoc
// @Summary      获取 AI 摘要
// @Description  按 ID 查询 AI 摘要详情
// @Tags         AI摘要
// @Produce      json
// @Param        id       query     integer  true  "主键 ID"
// @Success      200      {object}  types.AISummary
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /ai-summary/get [get]
func (h *AISummaryHandler) GetAISummary(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	summary, err := h.aiSummaryService.GetAISummaryByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get ai summary")
		return
	}

	c.JSON(http.StatusOK, summary)
}

// ListAISummary godoc
// @Summary      按所属对象查询 AI 摘要列表
// @Description  根据 OwnerID/OwnerType 查询 AI 摘要列表
// @Tags         AI摘要
// @Produce      json
// @Param        owner_id    query     integer  true  "所属对象 ID"
// @Param        owner_type  query     string   true  "所属对象类型：analysis 或 analysis_node"
// @Success      200         {array}   types.AISummary
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /ai-summary/list [get]
func (h *AISummaryHandler) ListAISummary(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req listAISummaryRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	summaries, err := h.aiSummaryService.ListAISummariesByOwner(c.Request.Context(), req.OwnerType, req.OwnerID)
	if err != nil {
		handleDataError(c, err, "failed to list ai summaries")
		return
	}

	c.JSON(http.StatusOK, summaries)
}

// PageAISummaryByActiveProject godoc
// @Summary      按当前用户激活项目分页查询 AI 摘要列表
// @Description  解析当前登录用户的激活项目，分页返回该项目（project_id）下的 AI 摘要，不返回 content
// @Tags         AI摘要
// @Accept       json
// @Produce      json
// @Param        request  body      handler.aiSummaryByProjectPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /ai-summary/list-by-project-page [post]
func (h *AISummaryHandler) PageAISummaryByActiveProject(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req aiSummaryByProjectPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("active project not found"))
			return
		}
		handleDataError(c, err, "failed to get active project")
		return
	}

	summaries, total, err := h.aiSummaryService.PageAISummariesByProjectID(c.Request.Context(), &req.Pagination, project.ID)
	if err != nil {
		handleDataError(c, err, "failed to page ai summaries")
		return
	}

	result := make([]aiSummaryListItem, 0, len(summaries))
	for _, summary := range summaries {
		if summary == nil {
			continue
		}
		result = append(result, aiSummaryListItem{
			ID:        strconv.FormatInt(summary.ID, 10),
			OwnerID:   strconv.FormatInt(summary.OwnerID, 10),
			OwnerType: summary.OwnerType,
			ProjectID: strconv.FormatInt(summary.ProjectID, 10),
			Title:     summary.Title,
			Status:    summary.Status,
			Profile:   summary.Profile,
			TaskID:    strconv.FormatInt(summary.TaskID, 10),
			CreatedAt: summary.CreatedAt.Format("2006-01-02 15:04:05"),
			UpdatedAt: summary.UpdatedAt.Format("2006-01-02 15:04:05"),
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"data":       result,
		"total":      total,
		"page":       req.GetPage(),
		"page_size":  req.GetPageSize(),
		"project_id": project.ID,
	})
}

// UpdateAISummary godoc
// @Summary      更新 AI 摘要
// @Description  按摘要 ID 更新标题、内容与 Agent Profile，字段不传则保持原值
// @Tags         AI摘要
// @Accept       json
// @Produce      json
// @Param        request  body      updateAISummaryRequest  true  "请求参数"
// @Success      200      {object}  types.AISummary
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /ai-summary/update [post]
func (h *AISummaryHandler) UpdateAISummary(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req updateAISummaryRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	summary, err := h.aiSummaryService.UpdateAISummary(c.Request.Context(), req.ID, req.Title, req.Content, req.Profile)
	if err != nil {
		handleDataError(c, err, "failed to update ai summary")
		return
	}

	c.JSON(http.StatusOK, summary)
}

// DeleteAISummary godoc
// @Summary      删除 AI 摘要
// @Description  按摘要 ID 删除 AI 摘要记录
// @Tags         AI摘要
// @Accept       json
// @Produce      json
// @Param        request  body      idBody  true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /ai-summary/delete [post]
func (h *AISummaryHandler) DeleteAISummary(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.aiSummaryService.DeleteAISummary(c.Request.Context(), req.ID); err != nil {
		handleDataError(c, err, "failed to delete ai summary")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "ai summary deleted successfully"})
}

// GetAISummaryInput godoc
// @Summary      获取 AI 摘要的 LLM 输入信息
// @Description  根据 OwnerID/OwnerType 解析生成摘要时交给 LLM 的系统提示词、工作目录与原始内容
// @Tags         AI摘要
// @Produce      json
// @Param        owner_id    query     integer  true  "所属对象 ID"
// @Param        owner_type  query     string   true  "所属对象类型：analysis 或 analysis_node"
// @Success      200         {object}  types.AISummaryInput
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /ai-summary/input [get]
func (h *AISummaryHandler) GetAISummaryInput(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req aiSummaryInputRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	input, err := h.aiSummaryService.GetAISummaryInput(c.Request.Context(), req.OwnerType, req.OwnerID)
	if err != nil {
		handleDataError(c, err, "failed to get ai summary input")
		return
	}

	c.JSON(http.StatusOK, input)
}
