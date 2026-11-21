package handler

import (
	"context"
	stderrs "errors"
	"net/http"
	"strconv"
	"time"

	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 计划（plan）HTTP 层。
//
// 计划是 agent 的通用产物（按树组织的步骤，可引用会话资源 ID），不是报告的一部分：
// agent 通过 create/list/get/update/delete_plan 工具读写，前端通过这组接口渲染
// 可拖拽的节点树。计划不保存资源详情，节点只带 source + source_id。

type planDetailRequest struct {
	ID int64 `form:"id" binding:"required"`
}

type savePlanRequest struct {
	ID    int64                 `json:"id,string"`
	Title string                `json:"title" binding:"required"`
	Goal  string                `json:"goal"`
	Items []agenttypes.PlanItem `json:"items"`
}

type deletePlanRequest struct {
	ID int64 `json:"id,string" binding:"required"`
}

// planSummary 是计划列表项：只带表格需要的字段，不包含节点树。
//
// 节点树可能很大，列表页并不展示它（展开某一行时才按 ID 请求详情），
// 因此这里只回传节点数量，避免每次打开列表都传输全部 items。
type planSummary struct {
	ID        int64     `json:"id,string"`
	ProjectID int64     `json:"project_id,string"`
	Title     string    `json:"title"`
	Goal      string    `json:"goal"`
	ItemCount int       `json:"item_count"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ListPlan godoc
// @Summary      查询项目计划列表
// @Description  返回当前激活项目下的全部计划（只含标题 / 目标 / 节点数，不含节点树）
// @Tags         项目
// @Produce      json
// @Success      200  {array}  planSummary
// @Security     Bearer
// @Router       /project/list-plan [get]
func (h *ProjectHandler) ListPlan(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	project, err := h.projectService.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		h.respondPlanError(c, err)
		return
	}

	plans, err := h.planService.ListByProject(ctx, project.ID)
	if err != nil {
		h.respondPlanError(c, err)
		return
	}

	// 始终返回数组（无计划时为 []），前端可以直接当表格数据源使用。
	out := make([]planSummary, 0, len(plans))
	for _, plan := range plans {
		if plan == nil {
			continue
		}
		out = append(out, planSummary{
			ID:        plan.ID,
			ProjectID: plan.ProjectID,
			Title:     plan.Title,
			Goal:      plan.Goal,
			ItemCount: len(plan.Items),
			CreatedAt: plan.CreatedAt,
			UpdatedAt: plan.UpdatedAt,
		})
	}
	c.JSON(http.StatusOK, out)
}

// GetPlanDetail godoc
// @Summary      查询单个计划
// @Tags         项目
// @Produce      json
// @Param        id  query  int64  true  "计划ID"
// @Success      200  {object}  types.AgentPlan
// @Security     Bearer
// @Router       /project/plan-detail [get]
func (h *ProjectHandler) GetPlanDetail(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req planDetailRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	plan, err := h.loadOwnedPlan(ctx, userID, req.ID)
	if err != nil {
		h.respondPlanError(c, err)
		return
	}
	c.JSON(http.StatusOK, plan)
}

// SavePlan godoc
// @Summary      新建 / 更新项目计划
// @Description  id 为空时新建，否则整体覆盖标题 / 目标 / 节点树
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body  savePlanRequest  true  "计划"
// @Success      200  {object}  types.AgentPlan
// @Security     Bearer
// @Router       /project/save-plan [post]
func (h *ProjectHandler) SavePlan(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req savePlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	project, err := h.projectService.GetActiveProjectByUserID(ctx, userID)
	if err != nil {
		h.respondPlanError(c, err)
		return
	}

	plan, err := h.planService.Save(ctx, &agenttypes.AgentPlan{
		ID:        req.ID,
		ProjectID: project.ID,
		Title:     req.Title,
		Goal:      req.Goal,
		Items:     req.Items,
	})
	if err != nil {
		h.respondPlanError(c, err)
		return
	}
	c.JSON(http.StatusOK, plan)
}

// DeletePlan godoc
// @Summary      删除项目计划
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body  deletePlanRequest  true  "计划ID"
// @Success      200  {object}  map[string]string
// @Security     Bearer
// @Router       /project/delete-plan [post]
func (h *ProjectHandler) DeletePlan(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req deletePlanRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if _, err := h.loadOwnedPlan(ctx, userID, req.ID); err != nil {
		h.respondPlanError(c, err)
		return
	}
	if err := h.planService.Delete(ctx, req.ID); err != nil {
		h.respondPlanError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "plan deleted successfully", "id": strconv.FormatInt(req.ID, 10)})
}

// loadOwnedPlan 读取计划并校验其所属项目已绑定当前用户（越权统一按 not found 处理）。
func (h *ProjectHandler) loadOwnedPlan(ctx context.Context, userID int64, planID int64) (*agenttypes.AgentPlan, error) {
	plan, err := h.planService.Get(ctx, planID)
	if err != nil {
		return nil, err
	}
	if _, err := h.projectService.GetProjectByID(ctx, plan.ProjectID); err != nil {
		return nil, err
	}
	// 通过「当前用户在该项目下是否有绑定」判断可见性：未绑定的项目一律视为不存在。
	bound, err := h.projectService.ListProjectUserIDs(ctx, plan.ProjectID)
	if err != nil {
		return nil, err
	}
	for _, id := range bound {
		if id == userID {
			return plan, nil
		}
	}
	return nil, gorm.ErrRecordNotFound
}

// respondPlanError 把计划接口的通用错误映射为 HTTP 错误。
func (h *ProjectHandler) respondPlanError(c *gin.Context, err error) {
	if stderrs.Is(err, gorm.ErrRecordNotFound) {
		c.Error(errors.NewNotFoundError("plan not found"))
		return
	}
	c.Error(errors.NewInternalServerError("plan operation failed").WithDetails(err.Error()))
}
