package handler

import (
	"net/http"

	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// AISummaryHandler 处理 AI 摘要相关接口。
type AISummaryHandler struct {
	aiSummaryService interfaces.AISummaryService
}

func NewAISummaryHandler(aiSummaryService interfaces.AISummaryService) *AISummaryHandler {
	return &AISummaryHandler{aiSummaryService: aiSummaryService}
}

type createAISummaryRequest struct {
	OwnerID   int64                  `json:"owner_id,string" binding:"required"`
	OwnerType types.SummaryOwnerType `json:"owner_type" binding:"required"`
}

type listAISummaryRequest struct {
	OwnerID   int64                  `form:"owner_id" binding:"required"`
	OwnerType types.SummaryOwnerType `form:"owner_type" binding:"required"`
}

type aiSummaryInputRequest struct {
	OwnerID   int64                  `form:"owner_id" binding:"required"`
	OwnerType types.SummaryOwnerType `form:"owner_type" binding:"required"`
}

// CreateAISummary godoc
// @Summary      创建 AI 摘要
// @Description  根据 OwnerID/OwnerType 创建摘要记录，并异步触发 LLM 生成摘要
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

	summary, err := h.aiSummaryService.CreateAISummary(c.Request.Context(), req.OwnerType, req.OwnerID)
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
