package handler

import (
	"context"
	stderrs "errors"
	"net/http"
	"path/filepath"
	"strconv"
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

	skills     *skill.Registry
	skillStore *skill.Store

	runtimeCtx *RuntimeContextResolver

	mu   sync.Mutex
	subs map[int64]func() // taskID → 取消订阅函数
}

// NewAgentHandler 创建 AgentHandler。
func NewAgentHandler(svc *agent.AgentService, conv *agent.ConversationService, hub *realtime.Hub, runtimeCtx *RuntimeContextResolver, skills *skill.Registry, skillStore *skill.Store) *AgentHandler {
	return &AgentHandler{
		svc:        svc,
		conv:       conv,
		hub:        hub,
		skills:     skills,
		skillStore: skillStore,
		runtimeCtx: runtimeCtx,
		subs:       make(map[int64]func()),
	}
}

// agentWSEvent 是推送到前端 WS 的事件信封。
type agentWSEvent struct {
	Type   string           `json:"type"` // 固定 "agent.event"
	TaskID int64            `json:"task_id,string"`
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
	ProfileID      int64          `json:"profile_id,string"` // AgentProfile ID（为空取默认；实际以当前用户配置为准）
	WorkingDir     string         `json:"working_dir"`
	Env            map[string]any `json:"env"` // 业务上下文 {id,type}，用于解析系统提示词模板变量与工作目录
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

// agentQuestionsQuery 是查询任务待作答提问的查询参数。
type agentQuestionsQuery struct {
	TaskID string `form:"task_id" binding:"required"`
}

// questionAnswerBody 是用户作答请求体。
type questionAnswerBody struct {
	ID     string              `json:"id" binding:"required"`
	Answer agent.AskUserAnswer `json:"answer"`
}

// agentQuestionPageRequest 是提问分页查询请求；task_id 为空表示全部任务。
type agentQuestionPageRequest struct {
	types.Pagination
	TaskID   string                 `json:"task_id"`
	Statuses []agent.QuestionStatus `json:"statuses"`
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

// profileSaveRequest 是创建 / 更新 AgentProfile 的请求体；UserID 由当前登录用户注入。
type profileSaveRequest struct {
	ID           string              `json:"id"` // 为空则新建，否则更新
	Name         string              `json:"name" binding:"required"`
	Description  string              `json:"description"`
	SystemPrompt string              `json:"system_prompt"`
	Model        string              `json:"model"`
	Skills       []string            `json:"skills"`
	Context      agent.ContextConfig `json:"context"`
	IsDefault    bool                `json:"is_default"`
}

// parseAgentID 把字符串 ID 解析为 int64（任务 / 会话 / 权限 ID 统一为雪花 ID）。
func parseAgentID(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}

// parseOptionalAgentID 解析可选的字符串 ID；为空时返回 0（表示“全部 / 不限定”）。
func parseOptionalAgentID(s string) (int64, error) {
	if strings.TrimSpace(s) == "" {
		return 0, nil
	}
	return parseAgentID(s)
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

	// UserID 以当前登录用户为准，供权限策略按用户读取许可配置。
	req.UserID = userID

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
	user, ok := getCurrentUser(c)
	if !ok {
		return
	}
	userID := user.ID

	var req chatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	// Profile 只能来自当前登录用户，忽略请求体中的 profile。
	profileID := user.AgentConfig.ProfileID

	// 解析业务上下文（llmEnv）→ 系统提示词模板变量 + 工作目录。
	// 系统提示词本身不在这里拼装：它由 AgentService 依据 Profile 模板 + 这些变量渲染。
	rc, rcErr := h.runtimeCtx.Resolve(c.Request.Context(), userID, req.Env)
	if rcErr != nil {
		handleAgentError(c, rcErr, "failed to resolve runtime context")
		return
	}
	workingDir := rc.WorkingDir

	// 1) 准备一轮：追加 user 消息、组装历史、创建任务（复用 AgentService 状态机）。
	// 新的对话 ConversationID 可能为空字符串，表示新建会话（TurnInput.ConversationID == 0）。
	var conversationID int64
	if strings.TrimSpace(req.ConversationID) != "" {
		var err error
		conversationID, err = parseAgentID(req.ConversationID)
		if err != nil {
			c.Error(errors.NewValidationError("invalid conversation_id").WithDetails(err.Error()))
			return
		}
	}
	task, conv, err := h.conv.CreateTurn(c.Request.Context(), agent.TurnInput{
		UserID:         userID,
		ConversationID: conversationID,
		Message:        req.Message,
		Provider:       req.Provider,
		Model:          req.Model,
		RuntimeContext: rc,
		ProfileID:      profileID,
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
		"task_id":         strconv.FormatInt(task.ID, 10),
		"conversation_id": strconv.FormatInt(conv.ID, 10),
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
	user, ok := getCurrentUser(c)
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

	// 用当前用户选中的 Profile 解析系统提示词模板，返回渲染后的真实提示词。
	profileID := user.AgentConfig.ProfileID
	if profileID == 0 {
		profileID = agent.BuiltinDefaultProfileID
	}
	info, err := h.runtimeCtx.Describe(c.Request.Context(), user.ID, profileID, req.Env)
	if err != nil {
		handleAgentError(c, err, "failed to describe runtime env")
		return
	}

	c.JSON(http.StatusOK, info)
}

// ListSkills godoc
// @Summary      查看 Agent 技能
// @Description  返回当前可用的全部技能（内置 + 用户自定义），包含名称、描述与 SKILL.md 路径
// @Tags         Agent
// @Produce      json
// @Success      200  {array}   skill.Skill
// @Failure      401  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/skill/list [get]
func (h *AgentHandler) ListSkills(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	if h.skills == nil {
		c.JSON(http.StatusOK, []*skill.Skill{})
		return
	}
	c.JSON(http.StatusOK, h.skills.List())
}

// skillSaveRequest 是创建 / 更新技能的请求体。
// ID 为原技能名（为空表示新建）；Name 为技能名，同时作为存放目录名。
type skillSaveRequest struct {
	ID                     string `json:"id"`
	Name                   string `json:"name" binding:"required"`
	Description            string `json:"description" binding:"required"`
	UserInvocable          bool   `json:"user_invocable"`
	DisableModelInvocation bool   `json:"disable_model_invocation"`
	Instructions           string `json:"instructions"`
}

// GetSkill godoc
// @Summary      查看单个 Agent 技能
// @Description  按名称返回单个技能（含指令正文），供前端编辑回填
// @Tags         Agent
// @Produce      json
// @Param        id   query     string  true  "技能名"
// @Success      200  {object}  skill.Skill
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/skill/get [get]
func (h *AgentHandler) GetSkill(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var q taskIDQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	if h.skills == nil {
		c.Error(errors.NewNotFoundError("record not found"))
		return
	}
	s, ok := h.skills.Get(q.ID)
	if !ok {
		c.Error(errors.NewNotFoundError("record not found"))
		return
	}
	c.JSON(http.StatusOK, s)
}

// SaveSkill godoc
// @Summary      创建或更新 Agent 技能
// @Description  把技能写回 $BaseDir/.skills/<name>/SKILL.md 并热更新内存注册表，程序运行中即时生效
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.skillSaveRequest  true  "技能内容"
// @Success      200      {object}  skill.Skill
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/skill/save [post]
func (h *AgentHandler) SaveSkill(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req skillSaveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	sk := &skill.Skill{
		Name:                   strings.TrimSpace(req.Name),
		Description:            strings.TrimSpace(req.Description),
		UserInvocable:          req.UserInvocable,
		DisableModelInvocation: req.DisableModelInvocation,
		Instructions:           req.Instructions,
	}
	// Path 参与校验（name 需与目录名一致）；此处先按目标目录名设定，再落盘。
	if h.skillStore == nil || !h.skillStore.Available() {
		c.Error(errors.NewInternalServerError("skill store is not configured"))
		return
	}
	sk.Path = h.skillStore.SkillDir(sk.Name)
	if err := sk.Validate(); err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	// 改名：先删除旧技能目录，避免残留同内容不同目录的副本。
	original := strings.TrimSpace(req.ID)
	if original != "" && original != sk.Name {
		if existing, ok := h.skills.Get(original); ok && h.skillStore.Contains(existing.SkillFilePath) {
			_ = h.skillStore.Delete(original)
		}
		h.skills.Unregister(original)
	}

	if err := h.skillStore.Save(sk); err != nil {
		c.Error(errors.NewInternalServerError("failed to save agent skill").WithDetails(err.Error()))
		return
	}

	// 回填真实路径并热更新注册表。
	sk.Path = h.skillStore.SkillDir(sk.Name)
	sk.SkillFilePath = filepath.Join(sk.Path, skill.SkillFileName)
	h.skills.Register(sk)

	c.JSON(http.StatusOK, sk)
}

// DeleteSkill godoc
// @Summary      删除 Agent 技能
// @Description  删除 $BaseDir/.skills 下的技能目录并从内存注册表移除；仅允许删除用户技能目录内的技能
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      taskIDBody  true  "技能名"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/skill/delete [post]
func (h *AgentHandler) DeleteSkill(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var body taskIDBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	name := strings.TrimSpace(body.ID)
	if name == "" {
		c.Error(errors.NewValidationError("id is required"))
		return
	}

	if h.skills == nil {
		c.Error(errors.NewNotFoundError("record not found"))
		return
	}
	existing, ok := h.skills.Get(name)
	if !ok {
		c.Error(errors.NewNotFoundError("record not found"))
		return
	}
	// 只允许删除用户技能目录内的技能，避免误删内置 / 外部技能。
	if h.skillStore == nil || !h.skillStore.Contains(existing.SkillFilePath) {
		c.Error(errors.NewValidationError("skill is not user-managed and cannot be deleted"))
		return
	}

	if err := h.skillStore.Delete(name); err != nil {
		c.Error(errors.NewInternalServerError("failed to delete agent skill").WithDetails(err.Error()))
		return
	}
	h.skills.Unregister(name)

	c.JSON(http.StatusOK, gin.H{"ok": true, "id": name})
}

// ListProfiles godoc
// @Summary      查看 Agent Profile
// @Description  返回全部 Profile（全局可见），包含系统提示词、技能与上下文注入开关
// @Tags         Agent
// @Produce      json
// @Success      200  {array}   agent.Profile
// @Failure      401  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/profile/list [get]
func (h *AgentHandler) ListProfiles(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	profiles, err := h.svc.ListProfiles(c.Request.Context())
	if err != nil {
		handleAgentError(c, err, "failed to list agent profiles")
		return
	}
	c.JSON(http.StatusOK, profiles)
}

// SaveProfile godoc
// @Summary      创建或更新 Agent Profile
// @Description  ID 为空时新建，否则更新；内置 Profile 可编辑（不可删除）
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.profileSaveRequest  true  "Profile 内容"
// @Success      200      {object}  agent.Profile
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/profile/save [post]
func (h *AgentHandler) SaveProfile(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req profileSaveRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	var profile *agent.Profile
	if req.ID != "" {
		id, err := parseAgentID(req.ID)
		if err != nil {
			c.Error(errors.NewValidationError("invalid profile id").WithDetails(err.Error()))
			return
		}
		// 更新：先加载既有记录，保留只读字段。
		existing, err := h.svc.GetProfile(c.Request.Context(), id)
		if err != nil {
			handleAgentError(c, err, "failed to get agent profile")
			return
		}
		existing.Name = req.Name
		existing.Description = req.Description
		existing.SystemPrompt = req.SystemPrompt
		existing.Model = req.Model
		existing.Skills = req.Skills
		existing.Context = req.Context
		existing.IsDefault = req.IsDefault
		profile = existing
	} else {
		profile = &agent.Profile{
			Name:         req.Name,
			Description:  req.Description,
			SystemPrompt: req.SystemPrompt,
			Model:        req.Model,
			Skills:       req.Skills,
			Context:      req.Context,
			IsDefault:    req.IsDefault,
		}
	}

	if err := h.svc.SaveProfile(c.Request.Context(), profile); err != nil {
		handleAgentError(c, err, "failed to save agent profile")
		return
	}
	c.JSON(http.StatusOK, profile)
}

// GetProfile godoc
// @Summary      查看单个 Agent Profile
// @Description  按 ID 返回单个 Profile（全局可见）
// @Tags         Agent
// @Produce      json
// @Param        id  query     string  true  "Profile ID"
// @Success      200  {object}  agent.Profile
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/profile/get [get]
func (h *AgentHandler) GetProfile(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var query taskIDQuery
	if err := c.ShouldBindQuery(&query); err != nil {
		c.Error(errors.NewValidationError("invalid query").WithDetails(err.Error()))
		return
	}

	id, err := parseAgentID(query.ID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid profile id").WithDetails(err.Error()))
		return
	}

	profile, err := h.svc.GetProfile(c.Request.Context(), id)
	if err != nil {
		handleAgentError(c, err, "failed to get agent profile")
		return
	}
	c.JSON(http.StatusOK, profile)
}

// DeleteProfile godoc
// @Summary      删除 Agent Profile
// @Description  仅能删除自定义 Profile；内置 Profile 不可删除
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      taskIDBody  true  "Profile ID"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/profile/delete [post]
func (h *AgentHandler) DeleteProfile(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var body taskIDBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	id, err := parseAgentID(body.ID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid profile id").WithDetails(err.Error()))
		return
	}
	if err := h.svc.DeleteProfile(c.Request.Context(), id); err != nil {
		handleAgentError(c, err, "failed to delete agent profile")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": body.ID})
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

	id, err := parseAgentID(q.ID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid id").WithDetails(err.Error()))
		return
	}
	task, err := h.svc.GetTask(c.Request.Context(), id)
	if err != nil {
		handleAgentError(c, err, "failed to get agent task")
		return
	}
	c.JSON(http.StatusOK, task)
}

// GetTaskRequest godoc
// @Summary      获取任务实际发送给 LLM 的请求内容
// @Description  按任务 ID 加载任务，应用 Agent Profile（系统提示词合并、技能选择、记忆注入），返回最终 Request
// @Tags         Agent
// @Produce      json
// @Param        id   query     string  true  "任务 ID"
// @Success      200  {object}  agent.Request
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/task/request [get]
func (h *AgentHandler) GetTaskRequest(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var q taskIDQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	id, err := parseAgentID(q.ID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid id").WithDetails(err.Error()))
		return
	}
	req, err := h.svc.GetTaskRequest(c.Request.Context(), id)
	if err != nil {
		handleAgentError(c, err, "failed to get agent task request")
		return
	}
	c.JSON(http.StatusOK, req)
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

	taskID, err := parseAgentID(q.TaskID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid task_id").WithDetails(err.Error()))
		return
	}
	events, err := h.svc.GetEvents(c.Request.Context(), taskID, q.After)
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

	taskID, err := parseAgentID(q.TaskID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid task_id").WithDetails(err.Error()))
		return
	}
	perms, err := h.svc.GetPendingPermissions(c.Request.Context(), taskID)
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

	id, err := parseAgentID(q.ID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid id").WithDetails(err.Error()))
		return
	}
	conv, err := h.conv.GetConversation(c.Request.Context(), id)
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

	// task_id 可选：为空表示查询全部任务（taskID == 0）。
	taskID, err := parseOptionalAgentID(req.TaskID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid task_id").WithDetails(err.Error()))
		return
	}
	items, total, err := h.svc.PagePermissions(c.Request.Context(), req.Offset(), req.Limit(), taskID, req.Statuses...)
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

	// task_id 可选：为空表示查询全部任务（taskID == 0）。
	taskID, err := parseOptionalAgentID(req.TaskID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid task_id").WithDetails(err.Error()))
		return
	}
	items, total, err := h.svc.PageEvents(c.Request.Context(), req.Offset(), req.Limit(), taskID)
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

	id, err := parseAgentID(body.ID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid id").WithDetails(err.Error()))
		return
	}
	if approve {
		err = h.svc.ApprovePermission(c.Request.Context(), id, userID)
	} else {
		err = h.svc.DenyPermission(c.Request.Context(), id, userID)
	}
	if err != nil {
		handleAgentError(c, err, "failed to resolve permission")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": body.ID})
}

// GetPendingQuestions godoc
// @Summary      获取任务待作答提问
// @Tags         Agent
// @Produce      json
// @Param        task_id  query     string  true  "任务 ID"
// @Success      200      {array}   agent.QuestionRequest
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/task/questions [get]
func (h *AgentHandler) GetPendingQuestions(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var q agentQuestionsQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	taskID, err := parseAgentID(q.TaskID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid task_id").WithDetails(err.Error()))
		return
	}
	questions, err := h.svc.GetPendingQuestions(c.Request.Context(), taskID)
	if err != nil {
		handleAgentError(c, err, "failed to get pending questions")
		return
	}
	c.JSON(http.StatusOK, questions)
}

// AnswerQuestion godoc
// @Summary      提交用户对提问的作答
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.questionAnswerBody  true  "作答内容"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      409      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/question/answer [post]
func (h *AgentHandler) AnswerQuestion(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var body questionAnswerBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	id, err := parseAgentID(body.ID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid id").WithDetails(err.Error()))
		return
	}
	if err := h.svc.AnswerQuestion(c.Request.Context(), id, body.Answer, userID); err != nil {
		handleAgentError(c, err, "failed to answer question")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": body.ID})
}

// PageQuestions godoc
// @Summary      分页查询提问请求
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.agentQuestionPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/question/page [post]
func (h *AgentHandler) PageQuestions(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req agentQuestionPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	// task_id 可选：为空表示查询全部任务（taskID == 0）。
	taskID, err := parseOptionalAgentID(req.TaskID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid task_id").WithDetails(err.Error()))
		return
	}
	items, total, err := h.svc.PageQuestions(c.Request.Context(), req.Offset(), req.Limit(), taskID, req.Statuses...)
	if err != nil {
		handleAgentError(c, err, "failed to page agent questions")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      items,
		"total":     total,
		"page":      req.GetPage(),
		"page_size": req.GetPageSize(),
	})
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

	id, err := parseAgentID(body.ID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid id").WithDetails(err.Error()))
		return
	}
	if err := h.svc.CancelTask(c.Request.Context(), id); err != nil {
		handleAgentError(c, err, "failed to cancel agent task")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": body.ID})
}

// attachTaskStream 订阅任务事件并推送到指定用户的 WS。
//
// 事件通过 AgentService.Subscribe（EventBus）分发，WS 只是通知层；
// 历史恢复请使用 GetTaskEvents。任务到达终态时自动取消订阅，避免泄漏。
func (h *AgentHandler) attachTaskStream(userID int64, taskID int64) {
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

// getCurrentUser 从 gin 上下文读取当前登录用户（由认证中间件注入）。
func getCurrentUser(c *gin.Context) (*types.User, bool) {
	userVal, exists := c.Get(types.UserContextKey.String())
	if !exists {
		c.Error(errors.NewUnauthorizedError("missing user identity"))
		return nil, false
	}
	user, ok := userVal.(*types.User)
	if !ok || user == nil {
		c.Error(errors.NewUnauthorizedError("invalid user identity"))
		return nil, false
	}
	return user, true
}

// handleAgentError 把 Agent 域错误映射为对应的 HTTP 状态码。
func handleAgentError(c *gin.Context, err error, internalMsg string) {
	switch {
	case stderrs.Is(err, agent.ErrTaskNotFound), stderrs.Is(err, agent.ErrPermissionNotFound),
		stderrs.Is(err, agent.ErrQuestionNotFound),
		stderrs.Is(err, agent.ErrConversationNotFound), stderrs.Is(err, agent.ErrMemoryNotFound):
		c.Error(errors.NewNotFoundError("record not found"))
	case stderrs.Is(err, agent.ErrPermissionNotPending),
		stderrs.Is(err, agent.ErrQuestionNotPending),
		stderrs.Is(err, agent.ErrInvalidPermissionTransition),
		stderrs.Is(err, agent.ErrInvalidQuestionTransition),
		stderrs.Is(err, agent.ErrTaskAlreadyRunning):
		c.Error(errors.NewConflictError(err.Error()))
	default:
		c.Error(errors.NewInternalServerError(internalMsg).WithDetails(err.Error()))
	}
}
