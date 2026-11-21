package handler

import (
	stderrs "errors"
	"net/http"
	"strconv"

	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 报告大纲（plan）HTTP 层。
//
// 大纲是「报告结构」的事实源：agent 通过 create/list/get/update_report_plan 工具读写，
// 前端通过这组接口渲染可拖拽的章节树并提供给用户编辑。报告正文（output.md）不在
// 这里，由 create_report / update_report 工具与报告文件接口负责。

type projectReportPlanDetailRequest struct {
	ID int64 `form:"id" binding:"required"`
}

type saveProjectReportPlanRequest struct {
	ID       int64                            `json:"id,string"`
	Title    string                           `json:"title" binding:"required"`
	Goal     string                           `json:"goal"`
	Sections []types.ProjectReportPlanSection `json:"sections"`
}

type deleteProjectReportPlanRequest struct {
	ID int64 `json:"id,string" binding:"required"`
}

// ListProjectReportPlan godoc
// @Summary      查询项目报告大纲列表
// @Description  返回当前激活项目下的全部报告大纲（含章节树）
// @Tags         项目
// @Produce      json
// @Success      200  {array}   types.ProjectReportPlan
// @Security     Bearer
// @Router       /project/list-project-report-plan [get]
func (h *ProjectHandler) ListProjectReportPlan(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	project, err := h.projectService.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		h.respondPlanProjectError(c, err)
		return
	}

	plans, err := h.projectService.ListProjectReportPlansByProjectID(ctx, userID, project.ID)
	if err != nil {
		h.respondPlanProjectError(c, err)
		return
	}
	if plans == nil {
		plans = []*types.ProjectReportPlan{}
	}
	c.JSON(http.StatusOK, plans)
}

// GetProjectReportPlanDetail godoc
// @Summary      查询单个报告大纲
// @Tags         项目
// @Produce      json
// @Param        id  query  int64  true  "大纲ID"
// @Success      200  {object}  types.ProjectReportPlan
// @Security     Bearer
// @Router       /project/project-report-plan-detail [get]
func (h *ProjectHandler) GetProjectReportPlanDetail(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req projectReportPlanDetailRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	plan, err := h.projectService.GetProjectReportPlanByID(ctx, userID, req.ID)
	if err != nil {
		h.respondPlanProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, plan)
}

// SaveProjectReportPlan godoc
// @Summary      新建 / 更新项目报告大纲
// @Description  id 为空时新建，否则整体覆盖标题 / 目标 / 章节树
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body  saveProjectReportPlanRequest  true  "大纲"
// @Success      200  {object}  types.ProjectReportPlan
// @Security     Bearer
// @Router       /project/save-project-report-plan [post]
func (h *ProjectHandler) SaveProjectReportPlan(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req saveProjectReportPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	project, err := h.projectService.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		h.respondPlanProjectError(c, err)
		return
	}

	plan, err := h.projectService.SaveProjectReportPlan(ctx, userID, &types.ProjectReportPlan{
		ID:        req.ID,
		ProjectID: project.ID,
		Title:     req.Title,
		Goal:      req.Goal,
		Sections:  req.Sections,
	})
	if err != nil {
		h.respondPlanProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, plan)
}

// DeleteProjectReportPlan godoc
// @Summary      删除项目报告大纲
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body  deleteProjectReportPlanRequest  true  "大纲ID"
// @Success      200  {object}  map[string]string
// @Security     Bearer
// @Router       /project/delete-project-report-plan [post]
func (h *ProjectHandler) DeleteProjectReportPlan(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req deleteProjectReportPlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.projectService.DeleteProjectReportPlan(ctx, userID, req.ID); err != nil {
		h.respondPlanProjectError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "project report plan deleted successfully", "id": strconv.FormatInt(req.ID, 10)})
}

// respondPlanProjectError 把大纲接口的通用错误映射为 HTTP 错误。
func (h *ProjectHandler) respondPlanProjectError(c *gin.Context, err error) {
	if stderrs.Is(err, gorm.ErrRecordNotFound) {
		c.Error(errors.NewNotFoundError("project report plan not found"))
		return
	}
	c.Error(errors.NewInternalServerError("project report plan operation failed").WithDetails(err.Error()))
}
