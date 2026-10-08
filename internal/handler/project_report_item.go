package handler

import (
	stderrs "errors"
	"net/http"
	"strconv"
	"time"

	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ---------- ProjectReportItem ----------

type projectReportItemDTO struct {
	ID              string    `json:"id"`
	ProjectReportID string    `json:"project_report_id"`
	ParentID        string    `json:"parent_id"`
	OwnerType       string    `json:"owner_type"`
	OwnerID         string    `json:"owner_id"`
	SortOrder       int       `json:"sort_order"`
	Title           string    `json:"title"`
	Content         string    `json:"content"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// projectReportItemNode 是列表接口返回的树节点，携带同层子节点列表。
type projectReportItemNode struct {
	projectReportItemDTO
	Children []*projectReportItemNode `json:"children"`
}

func newProjectReportItemDTO(item *types.ProjectReportItem) projectReportItemDTO {
	if item == nil {
		return projectReportItemDTO{}
	}
	return projectReportItemDTO{
		ID:              strconv.FormatInt(item.ID, 10),
		ProjectReportID: strconv.FormatInt(item.ProjectReportID, 10),
		ParentID:        strconv.FormatInt(item.ParentID, 10),
		OwnerType:       string(item.OwnerType),
		OwnerID:         strconv.FormatInt(item.OwnerID, 10),
		SortOrder:       item.SortOrder,
		Title:           item.Title,
		Content:         item.Content,
		CreatedAt:       item.CreatedAt,
		UpdatedAt:       item.UpdatedAt,
	}
}

// buildProjectReportItemTree 将扁平条目按 ParentID 组装成树并返回根节点列表。
// 入参需已按同级 sort_order 升序排列；父节点缺失的孤立节点会被提升为根节点。
func buildProjectReportItemTree(items []*types.ProjectReportItem) []*projectReportItemNode {
	nodes := make(map[int64]*projectReportItemNode, len(items))
	parents := make(map[int64]int64, len(items))
	order := make([]int64, 0, len(items))
	for _, item := range items {
		nodes[item.ID] = &projectReportItemNode{
			projectReportItemDTO: newProjectReportItemDTO(item),
			Children:             make([]*projectReportItemNode, 0),
		}
		parents[item.ID] = item.ParentID
		order = append(order, item.ID)
	}

	roots := make([]*projectReportItemNode, 0)
	for _, id := range order {
		node := nodes[id]
		if parent, ok := nodes[parents[id]]; parents[id] != 0 && ok {
			parent.Children = append(parent.Children, node)
			continue
		}
		roots = append(roots, node)
	}
	return roots
}

type projectReportItemListRequest struct {
	ReportID int64 `form:"report_id" binding:"required"`
}

// ListProjectReportItem godoc
// @Summary      查询报告条目树
// @Description  查询指定报告下的所有条目，并按其 parent_id 组装成树返回
// @Tags         项目
// @Produce      json
// @Param        report_id  query     int64  true  "报告ID"
// @Success      200        {array}   projectReportItemNode
// @Failure      400        {object}  errors.AppError
// @Failure      401        {object}  errors.AppError
// @Failure      404        {object}  errors.AppError
// @Failure      500        {object}  errors.AppError
// @Security     Bearer
// @Router       /project/list-project-report-item [get]
func (h *ProjectHandler) ListProjectReportItem(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req projectReportItemListRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	items, err := h.projectService.ListProjectReportItemsByReportID(ctx, userID, req.ReportID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project report not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to list project report items").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, buildProjectReportItemTree(items))
}

type addProjectReportItemRequest struct {
	ProjectReportID int64  `json:"project_report_id,string" binding:"required"`
	ParentID        int64  `json:"parent_id,string"`
	OwnerType       string `json:"owner_type" binding:"required"`
	OwnerID         int64  `json:"owner_id,string"`
	SortOrder       int    `json:"sort_order"`
	Title           string `json:"title"`
	Content         string `json:"content"`
}

// AddProjectReportItem godoc
// @Summary      添加报告条目
// @Description  向指定报告添加条目。OwnerType 为 analysis/analysis_node/ai_summary 时需提供 OwnerID；OwnerType 为 custom（章节占位/自定义内容）时只需提供 title。parent_id 为 0 时挂到根节点。
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      addProjectReportItemRequest  true  "请求参数"
// @Success      200      {object}  projectReportItemDTO         "创建成功"
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /project/add-project-report-item [post]
func (h *ProjectHandler) AddProjectReportItem(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req addProjectReportItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	item := &types.ProjectReportItem{
		ProjectReportID: req.ProjectReportID,
		ParentID:        req.ParentID,
		OwnerType:       types.ProjectReportItemOwnerType(req.OwnerType),
		OwnerID:         req.OwnerID,
		SortOrder:       req.SortOrder,
		Title:           req.Title,
		Content:         req.Content,
	}

	if err := h.projectService.AddProjectReportItem(ctx, userID, item); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project report not found"))
			return
		}
		c.Error(errors.NewBadRequestError("failed to add project report item").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, newProjectReportItemDTO(item))
}

type updateProjectReportItemRequest struct {
	ID        int64  `json:"id,string" binding:"required"`
	ParentID  int64  `json:"parent_id,string"`
	OwnerType string `json:"owner_type"`
	OwnerID   int64  `json:"owner_id,string"`
	SortOrder int    `json:"sort_order"`
	Title     string `json:"title"`
	Content   string `json:"content"`
}

// UpdateProjectReportItem godoc
// @Summary      更新报告条目
// @Description  更新条目的排序、父节点或 owner 绑定
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      updateProjectReportItemRequest  true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /project/update-project-report-item [post]
func (h *ProjectHandler) UpdateProjectReportItem(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req updateProjectReportItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	err := h.projectService.UpdateProjectReportItem(ctx, userID, &types.ProjectReportItem{
		ID:        req.ID,
		ParentID:  req.ParentID,
		OwnerType: types.ProjectReportItemOwnerType(req.OwnerType),
		OwnerID:   req.OwnerID,
		SortOrder: req.SortOrder,
		Title:     req.Title,
		Content:   req.Content,
	})
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project report item not found"))
			return
		}
		c.Error(errors.NewBadRequestError("failed to update project report item").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "project report item updated successfully"})
}

type reorderProjectReportItemEntry struct {
	ID        int64 `json:"id,string" binding:"required"`
	ParentID  int64 `json:"parent_id,string"`
	SortOrder int   `json:"sort_order"`
}

type reorderProjectReportItemRequest struct {
	ReportID int64                           `json:"report_id,string" binding:"required"`
	Items    []reorderProjectReportItemEntry `json:"items" binding:"required"`
}

// ReorderProjectReportItem godoc
// @Summary      重排报告条目树
// @Description  按前端拖拽结果批量更新条目的 parent_id 与 sort_order，实现树形结构的移动与排序
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      reorderProjectReportItemRequest  true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /project/reorder-project-report-item [post]
func (h *ProjectHandler) ReorderProjectReportItem(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req reorderProjectReportItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	orders := make([]types.ProjectReportItemOrder, 0, len(req.Items))
	for _, entry := range req.Items {
		orders = append(orders, types.ProjectReportItemOrder{
			ID:        entry.ID,
			ParentID:  entry.ParentID,
			SortOrder: entry.SortOrder,
		})
	}

	if err := h.projectService.ReorderProjectReportItems(ctx, userID, req.ReportID, orders); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project report not found"))
			return
		}
		c.Error(errors.NewBadRequestError("failed to reorder project report items").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "project report items reordered successfully"})
}

type deleteProjectReportItemRequest struct {
	ID int64 `json:"id,string" binding:"required"`
}

// DeleteProjectReportItem godoc
// @Summary      删除报告条目
// @Description  删除指定报告条目（含其所有子孙条目）
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      deleteProjectReportItemRequest  true  "请求参数"
// @Success      200      {object}  map[string]string
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /project/delete-project-report-item [post]
func (h *ProjectHandler) DeleteProjectReportItem(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req deleteProjectReportItemRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.projectService.DeleteProjectReportItem(ctx, userID, req.ID); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project report item not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to delete project report item").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "project report item deleted successfully"})
}

type projectReportItemDetailRequest struct {
	ID int64 `form:"id" binding:"required"`
}

// GetProjectReportItemDetail godoc
// @Summary      查询报告条目详情
// @Description  根据 id 查询条目详情，File 类型会返回文件内容
// @Tags         项目
// @Produce      json
// @Param        id          query     int64  true  "条目ID"
// @Success      200         {object}  projectReportItemDTO
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      404         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /project/project-report-item-detail [get]
func (h *ProjectHandler) GetProjectReportItemDetail(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req projectReportItemDetailRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.projectService.GetProjectReportItemDetailByID(ctx, userID, req.ID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project report item not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get project report item detail").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, newProjectReportItemDTO(item))
}

type projectReportItemContentRequest struct {
	ID int64 `form:"id" binding:"required"`
}

// GetProjectReportItemContent godoc
// @Summary      查询报告条目 markdown 内容
// @Description  入参为 ProjectReportItem ID，返回该条目解析后的 markdown 内容
// @Tags         项目
// @Produce      json
// @Param        id          query     int64  true  "条目ID"
// @Success      200         {object}  map[string]interface{}
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      404         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /project/project-report-item-content [get]
func (h *ProjectHandler) GetProjectReportItemContent(c *gin.Context) {
	ctx := c.Request.Context()

	var req projectReportItemContentRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	section, err := h.projectService.GetProjectReportItemContent(ctx, req.ID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project report item not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get project report item content").WithDetails(err.Error()))
		return
	}

	title := ""
	prefix := ""
	content := ""
	ownerType := ""
	if section != nil {
		title = section.Title
		prefix = section.Prefix
		content = section.Render()
		ownerType = string(section.OwnerType)
	}

	c.JSON(http.StatusOK, gin.H{
		"title":      title,
		"prefix":     prefix,
		"content":    content,
		"owner_type": ownerType,
	})
}
