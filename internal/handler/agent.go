package handler

import (
	"context"
	stderrs "errors"
	"net/http"
	"sync"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/realtime"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/gin-gonic/gin"
)

// AgentHandler 处理 Agent 任务与权限相关接口。
//
// 职责边界（对应 design.md）：
//   - HTTP 是 command（创建任务 / 批准 / 拒绝）；
//   - WS 是 notification（通过 AgentService.Subscribe 把事件推送给创建任务的用户）；
//   - AgentService / Repository 是真正的 state。
type AgentHandler struct {
	svc *agent.AgentService
	hub *realtime.Hub

	mu   sync.Mutex
	subs map[string]func() // taskID → 取消订阅函数
}

// NewAgentHandler 创建 AgentHandler。
func NewAgentHandler(svc *agent.AgentService, hub *realtime.Hub) *AgentHandler {
	return &AgentHandler{
		svc:  svc,
		hub:  hub,
		subs: make(map[string]func()),
	}
}

// agentWSEvent 是推送到前端 WS 的事件信封。
type agentWSEvent struct {
	Type   string           `json:"type"` // 固定 "agent.event"
	TaskID string           `json:"task_id"`
	Event  agent.AgentEvent `json:"event"`
}

// ---- 请求 / 响应结构 ----

type taskIDQuery struct {
	ID string `form:"id" binding:"required"`
}

type taskIDBody struct {
	ID string `json:"id" binding:"required"`
}

type permissionIDBody struct {
	ID string `json:"id" binding:"required"`
}

type agentEventsQuery struct {
	TaskID string `form:"task_id" binding:"required"`
	After  int64  `form:"after"`
}

// ---- 分页请求结构（沿用 types.Pagination，与 Analysis 的分页接口保持一致） ----

type agentTaskPageRequest struct {
	types.Pagination
	Statuses []agent.TaskStatus `json:"statuses"`
}

type agentPermissionPageRequest struct {
	types.Pagination
	TaskID   string                   `json:"task_id"`
	Statuses []agent.PermissionStatus `json:"statuses"`
}

type agentEventPageRequest struct {
	types.Pagination
	TaskID string `json:"task_id"`
}

// CreateTask godoc
// @Summary      创建 Agent 任务
// @Description  创建任务、订阅事件流到 WS，并异步启动执行
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      agent.Request  true  "请求参数"
// @Success      200      {object}  agent.Task
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/task/create [post]
func (h *AgentHandler) CreateTask(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req agent.Request
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	// 1) 先创建任务拿到 taskID（此时仅发布 task.created）。
	task, err := h.svc.CreateTask(c.Request.Context(), req)
	if err != nil {
		handleAgentError(c, err, "failed to create agent task")
		return
	}

	// 2) 在启动执行前订阅该任务事件，避免错过早期事件。
	h.attachTaskStream(userID, task.ID)

	// 3) 启动异步执行。
	if err := h.svc.StartTask(c.Request.Context(), task.ID, nil); err != nil {
		handleAgentError(c, err, "failed to start agent task")
		return
	}

	c.JSON(http.StatusOK, task)
}

// GetTask godoc
// @Summary      获取 Agent 任务
// @Tags         Agent
// @Produce      json
// @Param        id   query     string  true  "任务 ID"
// @Success      200  {object}  agent.Task
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/task/get [get]
func (h *AgentHandler) GetTask(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var q taskIDQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	task, err := h.svc.GetTask(c.Request.Context(), q.ID)
	if err != nil {
		handleAgentError(c, err, "failed to get agent task")
		return
	}
	c.JSON(http.StatusOK, task)
}

// GetTaskEvents godoc
// @Summary      增量拉取任务事件
// @Description  返回 sequence 大于 after 的事件；浏览器刷新后用此接口恢复历史
// @Tags         Agent
// @Produce      json
// @Param        task_id  query     string  true  "任务 ID"
// @Param        after    query     int     false "上次收到的最大 sequence"
// @Success      200      {array}   agent.AgentEvent
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/task/events [get]
func (h *AgentHandler) GetTaskEvents(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var q agentEventsQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	events, err := h.svc.GetEvents(c.Request.Context(), q.TaskID, q.After)
	if err != nil {
		handleAgentError(c, err, "failed to get agent task events")
		return
	}
	c.JSON(http.StatusOK, events)
}

// GetPendingPermissions godoc
// @Summary      获取任务待确认权限
// @Tags         Agent
// @Produce      json
// @Param        task_id  query     string  true  "任务 ID"
// @Success      200      {array}   agent.PermissionRequest
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/task/permissions [get]
func (h *AgentHandler) GetPendingPermissions(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var q agentEventsQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	perms, err := h.svc.GetPendingPermissions(c.Request.Context(), q.TaskID)
	if err != nil {
		handleAgentError(c, err, "failed to get pending permissions")
		return
	}
	c.JSON(http.StatusOK, perms)
}

// PageTasks godoc
// @Summary      分页查询 Agent 任务
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.agentTaskPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/task/page [post]
func (h *AgentHandler) PageTasks(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req agentTaskPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	items, total, err := h.svc.PageTasks(c.Request.Context(), req.Offset(), req.Limit(), req.Statuses...)
	if err != nil {
		handleAgentError(c, err, "failed to page agent tasks")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      items,
		"total":     total,
		"page":      req.GetPage(),
		"page_size": req.GetPageSize(),
	})
}

// PagePermissions godoc
// @Summary      分页查询权限请求
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.agentPermissionPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/permission/page [post]
func (h *AgentHandler) PagePermissions(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req agentPermissionPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	items, total, err := h.svc.PagePermissions(c.Request.Context(), req.Offset(), req.Limit(), req.TaskID, req.Statuses...)
	if err != nil {
		handleAgentError(c, err, "failed to page agent permissions")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      items,
		"total":     total,
		"page":      req.GetPage(),
		"page_size": req.GetPageSize(),
	})
}

// PageEvents godoc
// @Summary      分页查询任务事件
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.agentEventPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/event/page [post]
func (h *AgentHandler) PageEvents(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req agentEventPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	items, total, err := h.svc.PageEvents(c.Request.Context(), req.Offset(), req.Limit(), req.TaskID)
	if err != nil {
		handleAgentError(c, err, "failed to page agent events")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      items,
		"total":     total,
		"page":      req.GetPage(),
		"page_size": req.GetPageSize(),
	})
}

// ApprovePermission godoc
// @Summary      批准权限请求
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      permissionIDBody  true  "权限 ID"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      409      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/permission/approve [post]
func (h *AgentHandler) ApprovePermission(c *gin.Context) {
	h.resolvePermission(c, true)
}

// DenyPermission godoc
// @Summary      拒绝权限请求
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      permissionIDBody  true  "权限 ID"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      409      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/permission/deny [post]
func (h *AgentHandler) DenyPermission(c *gin.Context) {
	h.resolvePermission(c, false)
}

func (h *AgentHandler) resolvePermission(c *gin.Context, approve bool) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var body permissionIDBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	var err error
	if approve {
		err = h.svc.ApprovePermission(c.Request.Context(), body.ID, userID)
	} else {
		err = h.svc.DenyPermission(c.Request.Context(), body.ID, userID)
	}
	if err != nil {
		handleAgentError(c, err, "failed to resolve permission")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": body.ID})
}

// CancelTask godoc
// @Summary      取消 Agent 任务
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      taskIDBody  true  "任务 ID"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/task/cancel [post]
func (h *AgentHandler) CancelTask(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var body taskIDBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	if err := h.svc.CancelTask(c.Request.Context(), body.ID); err != nil {
		handleAgentError(c, err, "failed to cancel agent task")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": body.ID})
}

// attachTaskStream 订阅任务事件并推送到指定用户的 WS。
//
// 事件通过 AgentService.Subscribe（EventBus）分发，WS 只是通知层；
// 历史恢复请使用 GetTaskEvents。任务到达终态时自动取消订阅，避免泄漏。
func (h *AgentHandler) attachTaskStream(userID, taskID string) {
	if h.svc == nil || h.hub == nil {
		return
	}

	h.mu.Lock()
	if _, exists := h.subs[taskID]; exists {
		h.mu.Unlock()
		return
	}
	h.mu.Unlock()

	var once sync.Once
	var unsub func()

	unsub = h.svc.Subscribe(taskID, func(_ context.Context, ev agent.AgentEvent) error {
		switch ev.Type {
		case agent.EventTaskCompleted, agent.EventTaskFailed, agent.EventTaskCanceled:
			once.Do(func() {
				h.mu.Lock()
				delete(h.subs, taskID)
				h.mu.Unlock()
				if unsub != nil {
					unsub()
				}
			})
		}

		_ = h.hub.PushMessage(userID, agentWSEvent{
			Type:   "agent.event",
			TaskID: taskID,
			Event:  ev,
		})
		return nil
	})

	h.mu.Lock()
	if _, exists := h.subs[taskID]; exists {
		// 并发创建时出现重复订阅，取消后保留第一个。
		h.mu.Unlock()
		unsub()
		return
	}
	h.subs[taskID] = unsub
	h.mu.Unlock()
}

// handleAgentError 把 Agent 域错误映射为对应的 HTTP 状态码。
func handleAgentError(c *gin.Context, err error, internalMsg string) {
	switch {
	case stderrs.Is(err, agent.ErrTaskNotFound), stderrs.Is(err, agent.ErrPermissionNotFound):
		c.Error(errors.NewNotFoundError("record not found"))
	case stderrs.Is(err, agent.ErrPermissionNotPending),
		stderrs.Is(err, agent.ErrInvalidPermissionTransition),
		stderrs.Is(err, agent.ErrTaskAlreadyRunning):
		c.Error(errors.NewConflictError(err.Error()))
	default:
		c.Error(errors.NewInternalServerError(internalMsg).WithDetails(err.Error()))
	}
}
