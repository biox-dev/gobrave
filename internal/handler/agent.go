package handler

import (
	"context"
	stderrs "errors"
	"net/http"
	"strings"
	"sync"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/agent/skill"
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
	svc  *agent.AgentService
	conv *agent.ConversationService
	hub  *realtime.Hub

	skills *skill.Registry

	runtimeCtx *RuntimeContextResolver

	mu   sync.Mutex
	subs map[string]func() // taskID → 取消订阅函数
}

// NewAgentHandler 创建 AgentHandler。
func NewAgentHandler(svc *agent.AgentService, conv *agent.ConversationService, hub *realtime.Hub, runtimeCtx *RuntimeContextResolver, skills *skill.Registry) *AgentHandler {
	return &AgentHandler{
		svc:        svc,
		conv:       conv,
		hub:        hub,
		skills:     skills,
		runtimeCtx: runtimeCtx,
		subs:       make(map[string]func()),
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

type chatRequest struct {
	ConversationID string         `json:"conversation_id"`            // 为空则新建会话
	Message        string         `json:"message" binding:"required"` // 本轮用户输入
	Provider       string         `json:"provider"`
	Model          string         `json:"model"`
	SystemPrompt   string         `json:"system_prompt"`
	WorkingDir     string         `json:"working_dir"`
	Env            map[string]any `json:"env"` // 业务上下文 {id,type}，用于解析系统提示词与工作目录
}

type describeEnvRequest struct {
	Env map[string]any `json:"env"` // 业务上下文 {id,type}
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

// agentMemoryPageRequest 是记忆分页查询请求；kinds 为空表示全部类别。
type agentMemoryPageRequest struct {
	types.Pagination
	Kinds []agent.MemoryKind `json:"kinds"`
}

// memorySaveRequest 是创建 / 更新记忆的请求体；UserID 由当前登录用户注入。
type memorySaveRequest struct {
	ID         string           `json:"id"`         // 为空则新建，否则更新
	SessionID  string           `json:"session_id"` // 可选：关联会话
	Kind       agent.MemoryKind `json:"kind" binding:"required"`
	Content    string           `json:"content" binding:"required"`
	Importance int              `json:"importance"`
	Metadata   map[string]any   `json:"metadata"`
}

// memoryRetrieveRequest 是记忆检索请求体。
type memoryRetrieveRequest struct {
	Query string `json:"query" binding:"required"`
	Limit int    `json:"limit"`
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

// Chat godoc
// @Summary      多轮对话
// @Description  发送一条消息：新建会话或续接已有会话；返回本轮任务 ID，实时内容走 WS 推送
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.chatRequest  true  "请求参数"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/chat [post]
func (h *AgentHandler) Chat(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req chatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	// 解析业务上下文（llmEnv）→ 系统提示词 + 工作目录。
	// 显式传入的 system_prompt / working_dir 优先；未提供时回退到运行时解析结果。
	systemPrompt := strings.TrimSpace(req.SystemPrompt)
	// workingDir := strings.TrimSpace(req.WorkingDir)
	// if h.runtimeCtx != nil {
	rc, rcErr := h.runtimeCtx.Resolve(c.Request.Context(), userID, req.Env)
	if rcErr != nil {
		handleAgentError(c, rcErr, "failed to resolve runtime context")
		return
	}
	if systemPrompt == "" {
		systemPrompt = rc.SystemPrompt
	}
	workingDir := rc.WorkingDir
	// }

	// 1) 准备一轮：追加 user 消息、组装历史、创建任务（复用 AgentService 状态机）。
	task, conv, err := h.conv.CreateTurn(c.Request.Context(), agent.TurnInput{
		UserID:         userID,
		ConversationID: req.ConversationID,
		Message:        req.Message,
		Provider:       req.Provider,
		Model:          req.Model,
		SystemPrompt:   systemPrompt,
		WorkingDir:     workingDir,
	})
	if err != nil {
		handleAgentError(c, err, "failed to create conversation turn")
		return
	}

	// 2) 在启动执行前订阅该任务事件到 WS，避免错过早期事件。
	h.attachTaskStream(userID, task.ID)

	// 3) 启动执行（异步，assistant 回复在轮次结束时自动写回会话历史）。
	if err := h.conv.StartTurn(c.Request.Context(), task.ID); err != nil {
		handleAgentError(c, err, "failed to start conversation turn")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"task_id":         task.ID,
		"conversation_id": conv.ID,
	})
}

// DescribeEnv godoc
// @Summary      解析当前对话的业务上下文
// @Description  根据 env{id,type} 返回人类可读的上下文名称与工作目录，供前端展示当前对话环境
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.describeEnvRequest  true  "业务上下文"
// @Success      200      {object}  RuntimeContextInfo
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/env/describe [post]
func (h *AgentHandler) DescribeEnv(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req describeEnvRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	if h.runtimeCtx == nil {
		c.Error(errors.NewInternalServerError("runtime context resolver is not configured"))
		return
	}

	info, err := h.runtimeCtx.Describe(c.Request.Context(), userID, req.Env)
	if err != nil {
		handleAgentError(c, err, "failed to describe runtime env")
		return
	}

	c.JSON(http.StatusOK, info)
}

// ListSkills godoc
// @Summary      查看 Agent 技能
// @Description  返回当前可用的全部技能（内置 + 用户自定义），包含名称、描述、入参 schema、版本与指令正文
// @Tags         Agent
// @Produce      json
// @Success      200  {array}   skill.Manifest
// @Failure      401  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/skill/list [get]
func (h *AgentHandler) ListSkills(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	if h.skills == nil {
		c.JSON(http.StatusOK, []skill.Manifest{})
		return
	}
	c.JSON(http.StatusOK, h.skills.Manifests())
}

// ProjectContext godoc
// @Summary      查看当前用户的项目上下文
// @Description  返回当前用户激活项目下已注入 Agent 系统提示词的项目上下文文本块（如已完成的分析节点）
// @Tags         Agent
// @Produce      json
// @Success      200  {object}  gin.H
// @Failure      401  {object}  errors.AppError
// @Failure      500  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/project-context [get]
func (h *AgentHandler) ProjectContext(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	text, err := h.svc.ProjectContext(c.Request.Context(), userID)
	if err != nil {
		handleAgentError(c, err, "failed to get agent project context")
		return
	}
	c.JSON(http.StatusOK, gin.H{"project_context": text})
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

type conversationPageRequest struct {
	types.Pagination
}

// PageConversations godoc
// @Summary      分页查询当前用户的会话
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.conversationPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/conversation/page [post]
func (h *AgentHandler) PageConversations(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req conversationPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	items, total, err := h.conv.PageConversations(c.Request.Context(), userID, req.Offset(), req.Limit())
	if err != nil {
		handleAgentError(c, err, "failed to page conversations")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      items,
		"total":     total,
		"page":      req.GetPage(),
		"page_size": req.GetPageSize(),
	})
}

// GetConversation godoc
// @Summary      获取会话（含完整历史消息）
// @Tags         Agent
// @Produce      json
// @Param        id   query     string  true  "会话 ID"
// @Success      200  {object}  agent.Conversation
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/conversation/get [get]
func (h *AgentHandler) GetConversation(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var q taskIDQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	conv, err := h.conv.GetConversation(c.Request.Context(), q.ID)
	if err != nil {
		handleAgentError(c, err, "failed to get conversation")
		return
	}
	// 校验归属，防止越权访问他人会话。
	if conv.UserID != userID {
		c.Error(errors.NewNotFoundError("record not found"))
		return
	}
	c.JSON(http.StatusOK, conv)
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

// SaveMemory godoc
// @Summary      创建或更新记忆
// @Description  ID 为空时新建，否则更新；UserID 由当前登录用户注入
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.memorySaveRequest  true  "记忆内容"
// @Success      200      {object}  agent.Memory
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/memory/save [post]
func (h *AgentHandler) SaveMemory(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req memorySaveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	var memory *agent.Memory
	if req.ID != "" {
		// 更新：先加载既有记录，保留 CreatedAt / LastAccessedAt 等只读字段。
		existing, err := h.svc.GetMemory(c.Request.Context(), req.ID)
		if err != nil {
			handleAgentError(c, err, "failed to get agent memory")
			return
		}
		if existing.UserID != userID {
			c.Error(errors.NewNotFoundError("record not found"))
			return
		}
		existing.SessionID = req.SessionID
		existing.Kind = req.Kind
		existing.Content = req.Content
		existing.Importance = req.Importance
		existing.Metadata = req.Metadata
		memory = existing
	} else {
		memory = &agent.Memory{
			UserID:     userID,
			SessionID:  req.SessionID,
			Kind:       req.Kind,
			Content:    req.Content,
			Importance: req.Importance,
			Metadata:   req.Metadata,
		}
	}
	if err := h.svc.SaveMemory(c.Request.Context(), memory); err != nil {
		handleAgentError(c, err, "failed to save agent memory")
		return
	}
	c.JSON(http.StatusOK, memory)
}

// GetMemory godoc
// @Summary      获取单条记忆
// @Tags         Agent
// @Produce      json
// @Param        id   query     string  true  "记忆 ID"
// @Success      200  {object}  agent.Memory
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/memory/get [get]
func (h *AgentHandler) GetMemory(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var q taskIDQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	mem, err := h.svc.GetMemory(c.Request.Context(), q.ID)
	if err != nil {
		handleAgentError(c, err, "failed to get agent memory")
		return
	}
	// 校验归属，防止越权访问他人记忆。
	if mem.UserID != userID {
		c.Error(errors.NewNotFoundError("record not found"))
		return
	}
	c.JSON(http.StatusOK, mem)
}

// DeleteMemory godoc
// @Summary      删除记忆
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      taskIDBody  true  "记忆 ID"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/memory/delete [post]
func (h *AgentHandler) DeleteMemory(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var body taskIDBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	// 先校验归属，防止越权删除他人记忆。
	mem, err := h.svc.GetMemory(c.Request.Context(), body.ID)
	if err != nil {
		handleAgentError(c, err, "failed to get agent memory")
		return
	}
	if mem.UserID != userID {
		c.Error(errors.NewNotFoundError("record not found"))
		return
	}

	if err := h.svc.DeleteMemory(c.Request.Context(), body.ID); err != nil {
		handleAgentError(c, err, "failed to delete agent memory")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": body.ID})
}

// PageMemory godoc
// @Summary      分页查询当前用户的记忆
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.agentMemoryPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/memory/page [post]
func (h *AgentHandler) PageMemory(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req agentMemoryPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	items, total, err := h.svc.ListMemory(c.Request.Context(), userID, req.Offset(), req.Limit(), req.Kinds...)
	if err != nil {
		handleAgentError(c, err, "failed to page agent memories")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      items,
		"total":     total,
		"page":      req.GetPage(),
		"page_size": req.GetPageSize(),
	})
}

// RetrieveMemory godoc
// @Summary      检索当前用户的相关记忆
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.memoryRetrieveRequest  true  "检索参数"
// @Success      200      {array}   agent.Memory
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/memory/retrieve [post]
func (h *AgentHandler) RetrieveMemory(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req memoryRetrieveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	if req.Limit <= 0 {
		req.Limit = 5
	}
	// TODO
	items, err := h.svc.RetrieveMemory(c.Request.Context(), userID, req.Query, req.Limit)
	if err != nil {
		handleAgentError(c, err, "failed to retrieve agent memories")
		return
	}
	c.JSON(http.StatusOK, items)
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
	case stderrs.Is(err, agent.ErrTaskNotFound), stderrs.Is(err, agent.ErrPermissionNotFound),
		stderrs.Is(err, agent.ErrConversationNotFound), stderrs.Is(err, agent.ErrMemoryNotFound):
		c.Error(errors.NewNotFoundError("record not found"))
	case stderrs.Is(err, agent.ErrPermissionNotPending),
		stderrs.Is(err, agent.ErrInvalidPermissionTransition),
		stderrs.Is(err, agent.ErrTaskAlreadyRunning):
		c.Error(errors.NewConflictError(err.Error()))
	default:
		c.Error(errors.NewInternalServerError(internalMsg).WithDetails(err.Error()))
	}
}
