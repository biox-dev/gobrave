package handler

import (
	"context"
	stderrs "errors"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/skill"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/realtime"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

// AgentHandler 处理 Agent 任务与权限相关接口。
//
// 职责边界（对应 design.md）：
//   - HTTP 是 command（创建任务 / 批准 / 拒绝 / 订阅会话）；
//   - WS 是 notification（会话订阅后按 AgentEvent.SessionID 推送）；
//   - AgentService / Repository 是真正的 state。
//
// 实时流按「会话」订阅而不是按用户全局推送：进入会话页时登记订阅，离开时撤销，
// 项目内所有成员看到的同一会话事件流完全一致。
type AgentHandler struct {
	svc  *agentservice.AgentService
	chat *agentservice.ChatService
	hub  *realtime.Hub

	skills     *skill.Registry
	skillStore *skill.Store
	// tools 是进程内工具注册表（只读）：供工具目录接口列出 / 查看内置工具定义。
	tools *tool.Registry

	runtimeCtx *RuntimeContextResolver
	// projects 用于会话可见性判断：会话按项目隔离，项目成员都能查看与订阅。
	projects interfaces.ProjectService
	// cfg 提供 agent.providers 模型配置表，用于 Profile 编辑时的模型下拉选项。
	cfg *config.Config

	// stream 是会话级实时事件路由（SessionID → 订阅用户集合）。
	stream *sessionEventBridge

	mu sync.Mutex
	// unsub 是全局事件订阅的取消函数（由 StartEventStream 注册）。
	unsub func()
	// presenceUnsub 是连接在线状态订阅的取消函数（由 StartEventStream 注册）。
	presenceUnsub func()
}

// NewAgentHandler 创建 AgentHandler。
func NewAgentHandler(svc *agentservice.AgentService, chat *agentservice.ChatService, hub *realtime.Hub, runtimeCtx *RuntimeContextResolver, skills *skill.Registry, skillStore *skill.Store, tools *tool.Registry, projects interfaces.ProjectService, cfg *config.Config) *AgentHandler {
	return &AgentHandler{
		svc:        svc,
		chat:       chat,
		hub:        hub,
		skills:     skills,
		skillStore: skillStore,
		tools:      tools,
		runtimeCtx: runtimeCtx,
		projects:   projects,
		cfg:        cfg,
		stream:     newSessionEventBridge(hub),
	}
}

// agentWSEvent 是推送到前端 WS 的事件信封。
type agentWSEvent struct {
	Type   string                `json:"type"` // 固定 "agent.event"
	TaskID int64                 `json:"task_id,string"`
	Event  agenttypes.AgentEvent `json:"event"`
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

// taskIDQueryParams 是仅需 task_id 的查询参数（权限 / 提问等接口复用）。
type taskIDQueryParams struct {
	TaskID string `form:"task_id" binding:"required"`
}

type chatRequest struct {
	// SessionID 对应前端的 conversation_id（保持 API 兼容），为空则新建会话。
	SessionID  string `json:"conversation_id"`
	Message    string `json:"message" binding:"required"` // 本轮用户输入
	Provider   string `json:"provider"`
	Model      string `json:"model"`
	ProfileID  int64  `json:"profile_id,string"` // AgentProfile ID（为空取默认；实际以当前用户配置为准）
	WorkingDir string `json:"working_dir"`
}

// ---- 分页请求结构（沿用 types.Pagination，与 Analysis 的分页接口保持一致） ----

type agentTaskPageRequest struct {
	types.Pagination
	Statuses []agenttypes.TaskStatus `json:"statuses"`
}

type agentPermissionPageRequest struct {
	types.Pagination
	TaskID   string                        `json:"task_id"`
	Statuses []agenttypes.PermissionStatus `json:"statuses"`
}

// agentQuestionsQuery 是查询任务待作答提问的查询参数。
type agentQuestionsQuery struct {
	TaskID string `form:"task_id" binding:"required"`
}

// questionAnswerBody 是用户作答请求体。
type questionAnswerBody struct {
	ID     string                   `json:"id" binding:"required"`
	Answer agenttypes.AskUserAnswer `json:"answer"`
}

// agentQuestionPageRequest 是提问分页查询请求；task_id 为空表示全部任务。
type agentQuestionPageRequest struct {
	types.Pagination
	TaskID   string                      `json:"task_id"`
	Statuses []agenttypes.QuestionStatus `json:"statuses"`
}

// agentMemoryPageRequest 是记忆分页查询请求；kinds 为空表示全部类别。
type agentMemoryPageRequest struct {
	types.Pagination
	Kinds []agenttypes.MemoryKind `json:"kinds"`
}

// memoryIDQuery 是单条记忆的查询参数；记忆 ID 是雪花 ID（int64）。
type memoryIDQuery struct {
	ID int64 `form:"id,string" binding:"required"`
}

// memoryIDBody 是单条记忆的删除请求体；记忆 ID 是雪花 ID（int64）。
type memoryIDBody struct {
	ID int64 `json:"id,string" binding:"required"`
}

// memorySaveRequest 是创建 / 更新记忆的请求体；UserID 由当前登录用户注入。
type memorySaveRequest struct {
	ID         int64                 `json:"id,string"`         // 为 0 则新建，否则更新
	ProjectID  int64                 `json:"project_id,string"` // 可选：关联项目
	SessionID  int64                 `json:"session_id,string"` // 可选：关联会话
	Kind       agenttypes.MemoryKind `json:"kind" binding:"required"`
	Content    string                `json:"content" binding:"required"`
	Importance int                   `json:"importance"`
	Metadata   map[string]any        `json:"metadata"`
}

// memoryRetrieveRequest 是记忆检索请求体。
type memoryRetrieveRequest struct {
	Query string `json:"query" binding:"required"`
	Limit int    `json:"limit"`
}

// profileSaveRequest 是创建 / 更新 AgentProfile 的请求体；UserID 由当前登录用户注入。
type profileSaveRequest struct {
	ID           string                   `json:"id"` // 为空则新建，否则更新
	Name         string                   `json:"name" binding:"required"`
	Description  string                   `json:"description"`
	SystemPrompt string                   `json:"system_prompt"`
	Model        string                   `json:"model"`
	Skills       []string                 `json:"skills"`
	Tools        []string                 `json:"tools"` // 工具名白名单；为空表示注入全部工具
	Context      agenttypes.ContextConfig `json:"context"`
	IsDefault    bool                     `json:"is_default"`
	// ReasoningEffort 思考模式与推理强度：none 关闭思考，low / high / max 开启；为空表示不指定。
	ReasoningEffort string `json:"reasoning_effort"`
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
// @Description  创建任务并异步启动执行；实时事件推送给订阅了该任务会话的用户（见 /agent/session/subscribe）
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      agenttypes.Request  true  "请求参数"
// @Success      200      {object}  agenttypes.Task
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

	var body struct {
		agenttypes.Request
		SessionID  int64  `json:"session_id,string"`
		ProjectID  int64  `json:"project_id,string"`
		WorkingDir string `json:"working_dir"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	// 执行事实（用户 / 会话 / 项目 / 工作目录）由 Task 承载：用户取当前登录用户，
	// 其余可由调用方指定，缺省时回退到当前激活项目。
	facts := agenttypes.TaskFacts{
		UserID:     userID,
		SessionID:  body.SessionID,
		ProjectID:  body.ProjectID,
		WorkingDir: body.WorkingDir,
	}
	if (facts.ProjectID == 0 || facts.WorkingDir == "") && h.runtimeCtx != nil {
		if workingDir, project, err := h.runtimeCtx.Resolve(c.Request.Context(), userID); err == nil {
			if facts.ProjectID == 0 && project != nil {
				facts.ProjectID = project.ID
			}
			if facts.WorkingDir == "" {
				facts.WorkingDir = workingDir
			}
		}
	}

	// 1) 先创建任务拿到 taskID（此时仅发布 task.created）。
	task, err := h.svc.CreateTask(c.Request.Context(), body.Request, facts)
	if err != nil {
		handleAgentError(c, err, "failed to create agent task")
		return
	}

	// 2) 启动异步执行（实时事件只推给订阅了该会话的用户，无需逐任务订阅）。
	if err := h.svc.StartTask(c.Request.Context(), task.ID); err != nil {
		handleAgentError(c, err, "failed to start agent task")
		return
	}

	c.JSON(http.StatusOK, task)
}

// Chat godoc
// @Summary      多轮对话
// @Description  发送一条消息：新建会话或续接已有会话；返回本轮任务 ID，实时内容走 WS 推送
// @Description  新建会话时先用首条问题写入占位标题，再异步调用 Agent 生成更贴切的标题（不阻塞本轮）
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.chatRequest  true  "请求参数"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      403      {object}  errors.AppError
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

	// 1) 准备一轮：追加 user 消息、组装历史、创建任务（复用 AgentService 状态机）。
	// 新的对话 conversation_id 可能为空字符串，表示新建会话（TurnInput.SessionID == 0）。
	var sessionID int64
	if strings.TrimSpace(req.SessionID) != "" {
		var err error
		sessionID, err = parseAgentID(req.SessionID)
		if err != nil {
			c.Error(errors.NewValidationError("invalid conversation_id").WithDetails(err.Error()))
			return
		}
		// 续接已有会话时校验可见性：会话按项目共享，只有项目成员才能继续对话。
		existing, err := h.chat.GetSessionMeta(c.Request.Context(), sessionID)
		if err != nil {
			handleAgentError(c, err, "failed to get session")
			return
		}
		if err := ensureProjectAccessible(c.Request.Context(), h.projects, existing.ProjectID, userID); err != nil {
			c.Error(err)
			return
		}
	}

	// 新建会话需要归属项目：由当前用户激活项目决定（续接会话沿用会话自身的项目）。
	var projectID int64
	if sessionID == 0 {
		var err error
		projectID, err = h.activeProjectID(c.Request.Context(), userID)
		if err != nil {
			handleAgentError(c, err, "failed to resolve active project")
			return
		}
	}

	task, sess, err := h.chat.CreateTurn(c.Request.Context(), agentservice.TurnInput{
		UserID:    userID,
		ProjectID: projectID,
		SessionID: sessionID,
		Message:   req.Message,
		Provider:  req.Provider,
		Model:     req.Model,
		ProfileID: profileID,
	})
	if err != nil {
		handleAgentError(c, err, "failed to create conversation turn")
		return
	}

	// 发起人隐式订阅该会话：新建会话时页面还没有会话 ID，无法自行订阅；
	// 若页面已订阅，这里是幂等的。必须在 StartTurn 之前登记，避免错过早期事件。
	h.subscribeSession(sess.ID, userID)

	// 2) 启动执行（异步，assistant 回复在轮次结束时自动写回会话历史）。
	// 实时事件只推给订阅了该会话的用户。
	if err := h.chat.StartTurn(c.Request.Context(), task.ID); err != nil {
		handleAgentError(c, err, "failed to start conversation turn")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"task_id":         strconv.FormatInt(task.ID, 10),
		"conversation_id": strconv.FormatInt(sess.ID, 10),
	})
}

// DescribeEnv godoc
// @Summary      解析当前对话的上下文
// @Description  返回当前激活项目的默认工作目录与按 Profile 渲染后的系统提示词，供前端展示当前对话环境
// @Tags         Agent
// @Accept       json
// @Produce      json
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

	if h.runtimeCtx == nil {
		c.Error(errors.NewInternalServerError("runtime context resolver is not configured"))
		return
	}

	// 用当前用户选中的 Profile 解析系统提示词模板，返回渲染后的真实提示词。
	profileID := user.AgentConfig.ProfileID
	if profileID == 0 {
		profileID = agenttypes.BuiltinDefaultProfileID
	}
	// 工作目录固定为当前激活项目的默认目录；系统提示词按当前 Profile 渲染。
	info, err := h.runtimeCtx.Describe(c.Request.Context(), user.ID, profileID)
	if err != nil {
		handleAgentError(c, err, "failed to describe runtime context")
		return
	}

	c.JSON(http.StatusOK, info)
}

// agentToolInfo 是工具目录接口的响应体。
//
// InputSchema 是工具入参的 JSON Schema（object），前端据此渲染参数说明。
type agentToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

// toAgentToolInfo 把工具定义转换为响应体。
func toAgentToolInfo(def tool.Definition) agentToolInfo {
	return agentToolInfo{
		Name:        def.Name,
		Description: def.Description,
		InputSchema: def.InputSchema,
	}
}

// ListTools godoc
// @Summary      查看 Agent 工具
// @Description  返回当前工具注册表中的全部工具（内置 + 编码类 + 外部 MCP）的元信息；详情请调用 /agent/tool/get
// @Tags         Agent
// @Produce      json
// @Success      200  {array}   handler.agentToolInfo
// @Failure      401  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/tool/list [get]
func (h *AgentHandler) ListTools(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	if h.tools == nil {
		c.JSON(http.StatusOK, []agentToolInfo{})
		return
	}

	defs := h.tools.List()
	out := make([]agentToolInfo, 0, len(defs))
	for _, def := range defs {
		out = append(out, toAgentToolInfo(def))
	}
	c.JSON(http.StatusOK, out)
}

// GetTool godoc
// @Summary      查看单个 Agent 工具
// @Description  按工具名返回工具定义（描述 + 参数 JSON Schema + 来源），供前端展示工具详情
// @Tags         Agent
// @Produce      json
// @Param        id   query     string  true  "工具名"
// @Success      200  {object}  handler.agentToolInfo
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/tool/get [get]
func (h *AgentHandler) GetTool(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var q taskIDQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	if h.tools == nil {
		c.Error(errors.NewNotFoundError("record not found"))
		return
	}
	def, ok := h.tools.Definition(q.ID)
	if !ok {
		c.Error(errors.NewNotFoundError("record not found"))
		return
	}
	c.JSON(http.StatusOK, toAgentToolInfo(def))
}

// ListSkills godoc
// @Summary      查看 Agent 技能
// @Description  返回当前可用的全部技能（内置 + 用户自定义）的元信息，不包含指令正文；正文请调用 /agent/skill/get
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

	// 列表只返回元信息：指令正文可能较大，按需由前端展开时调用 GetSkill 拉取。
	// 注意 Registry.List 返回的是注册表中的指针，这里必须复制一份再清空，
	// 否则会污染内存中的技能注册表。
	list := h.skills.List()
	out := make([]*skill.Skill, 0, len(list))
	for _, s := range list {
		if s == nil {
			continue
		}
		summary := *s
		summary.Instructions = ""
		out = append(out, &summary)
	}
	c.JSON(http.StatusOK, out)
}

// skillSaveRequest 是创建 / 更新技能的请求体。
// ID 为原技能名（为空表示新建）；Name 为技能名，同时作为存放目录名。
type skillSaveRequest struct {
	ID                     string `json:"id"`
	Name                   string `json:"name" binding:"required"`
	Description            string `json:"description" binding:"required"`
	DisableModelInvocation bool   `json:"disable_model_invocation"`
	// DisableRender 为 true 时，技能文本（指令正文 / 附属资源）不做 pongo2 模板渲染。
	DisableRender bool   `json:"disable_render"`
	Instructions  string `json:"instructions"`
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

// ResolveSkill godoc
// @Summary      按名称解析 Agent 技能（含模板渲染）
// @Description  与 get_skill 工具同一实现：按名称解析技能，并用当前用户业务上下文（llmEnv）解析出的变量渲染指令正文
// @Tags         Agent
// @Produce      json
// @Param        id   query     string  true  "技能名"
// @Success      200  {object}  skill.AgentSkillDetail
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/skill/resolve [get]
func (h *AgentHandler) ResolveSkill(c *gin.Context) {
	user, ok := getCurrentUser(c)
	if !ok {
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

	// 技能正文的 pongo2 模板用默认工作目录变量渲染（与 get_skill 工具执行期同一实现）。
	ctx := c.Request.Context()
	if h.runtimeCtx != nil {
		workingDir, _, err := h.runtimeCtx.Resolve(ctx, user.ID)
		if err != nil {
			handleAgentError(c, err, "failed to resolve working directory")
			return
		}
		ctx = runtimecontext.WithRuntimeContext(ctx, &runtimecontext.RuntimeContext{
			SystemPromptVars: map[string]any{"workspace_directory": workingDir},
		})
	}

	// version 留空：HTTP 接口使用当前已安装版本（与 get_skill 不传 version 时一致）。
	// raw=false：接口返回渲染后的指令正文（前端详情弹窗展示用），原文模式仅供 agent 编辑技能。
	out, err := skill.ResolveSkillOutput(ctx, h.skills.Resolver(), q.ID, "", false)
	if err != nil {
		handleAgentError(c, err, "failed to resolve agent skill")
		return
	}
	// 工具出参不含磁盘路径（对模型无用且可能诱导绕过 get_skill 读文件），
	// 但前端详情弹窗需要展示技能所在目录，因此在 HTTP 层补上 skill_path。
	resp := skill.AgentSkillDetail{GetSkillOutput: out}
	if s, ok := h.skills.Get(q.ID); ok {
		resp.SkillPath = s.SkillInfo().SkillPath
	}
	c.JSON(http.StatusOK, resp)
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
		DisableModelInvocation: req.DisableModelInvocation,
		DisableRender:          req.DisableRender,
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

// agentProfileModelOption 是 Profile 编辑弹窗的模型下拉选项。
type agentProfileModelOption struct {
	// Name 是 agent.providers 的 key，写入 Profile.Model。
	Name string `json:"name"`
	// Label 是展示名（配置的 name，为空回退 api_model / key）。
	Label string `json:"label"`
	// Provider 是模型厂商标识（openai / deepseek / anthropic …）。
	Provider string `json:"provider,omitempty"`
	// CanReason 表示模型支持推理 / 思考过程。
	CanReason bool `json:"can_reason"`
	// ReasoningEffort 是该模型配置的默认推理强度（为空表示未指定）。
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

// agentProfileOptionsResponse 是 Profile 编辑选项接口的响应。
//
// 前端据此渲染「模型下拉框 → 推理强度下拉框」，模型列表来自当前生效的
// agent.providers 配置（与 Profile.Model 的取值一一对应）。
type agentProfileOptionsResponse struct {
	// Models 是当前已配置的模型列表（按 key 升序）。
	Models []agentProfileModelOption `json:"models"`
	// ReasoningEfforts 是 ReasoningEffort 的可选值（none / low / medium / high / max）。
	ReasoningEfforts []string `json:"reasoning_efforts"`
}

// GetProfileOptions godoc
// @Summary      查看 Agent Profile 编辑选项
// @Description  返回当前 agent.providers 中已配置的模型列表（含是否支持推理与默认推理强度），以及推理强度可选值
// @Tags         Agent
// @Produce      json
// @Success      200  {object}  handler.agentProfileOptionsResponse
// @Failure      401  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/profile/options [get]
func (h *AgentHandler) GetProfileOptions(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	models := make([]agentProfileModelOption, 0)
	if h.cfg != nil && h.cfg.Agent != nil {
		keys := make([]string, 0, len(h.cfg.Agent.Providers))
		for key := range h.cfg.Agent.Providers {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			pc := h.cfg.Agent.Providers[key]
			label := strings.TrimSpace(pc.Name)
			if label == "" {
				label = strings.TrimSpace(pc.APIModel)
			}
			if label == "" {
				label = key
			}
			models = append(models, agentProfileModelOption{
				Name:            key,
				Label:           label,
				Provider:        strings.TrimSpace(pc.Provider),
				CanReason:       pc.CanReason,
				ReasoningEffort: strings.ToLower(strings.TrimSpace(pc.ReasoningEffort)),
			})
		}
	}

	c.JSON(http.StatusOK, agentProfileOptionsResponse{
		Models:           models,
		ReasoningEfforts: agenttypes.ReasoningEfforts,
	})
}

// ListProfiles godoc
// @Summary      查看 Agent Profile
// @Description  返回全部 Profile（全局可见），包含系统提示词、技能与上下文注入开关
// @Tags         Agent
// @Produce      json
// @Success      200  {array}   agenttypes.Profile
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
// @Success      200      {object}  agenttypes.Profile
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

	var profile *agenttypes.Profile
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
		existing.Tools = req.Tools
		existing.Context = req.Context
		existing.IsDefault = req.IsDefault
		existing.ReasoningEffort = strings.ToLower(strings.TrimSpace(req.ReasoningEffort))
		profile = existing
	} else {
		profile = &agenttypes.Profile{
			Name:            req.Name,
			Description:     req.Description,
			SystemPrompt:    req.SystemPrompt,
			Model:           req.Model,
			Skills:          req.Skills,
			Tools:           req.Tools,
			Context:         req.Context,
			IsDefault:       req.IsDefault,
			ReasoningEffort: strings.ToLower(strings.TrimSpace(req.ReasoningEffort)),
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
// @Success      200  {object}  agenttypes.Profile
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
// @Success      200  {object}  agenttypes.Task
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
// @Success      200  {object}  agenttypes.Request
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

// GetTaskTimeline godoc
// @Summary      获取任务的执行时间线
// @Description  返回该任务已落库的时间线消息（reasoning / assistant / tool_call / tool_result / error），
// @Description  用于前端刷新 / 切换会话后回填「事实流」：增量事件（text / reasoning_delta）只广播
// @Description  不落库，完整块事件由 TimelineRecorder 即时写入 agent_messages。
// @Description  访问控制与会话详情一致：会话所属项目的成员都可以读取该会话下任务的时间线；
// @Description  不属于任何会话的一次性任务仍然只允许任务归属者读取（不可访问时返回 404）。
// @Tags         Agent
// @Produce      json
// @Param        task_id  query     string  true  "任务 ID"
// @Success      200      {array}   agenttypes.Message
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/task/timeline [get]
func (h *AgentHandler) GetTaskTimeline(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var q taskIDQueryParams
	if err := c.ShouldBindQuery(&q); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	taskID, err := parseAgentID(q.TaskID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid task_id").WithDetails(err.Error()))
		return
	}
	msgs, err := h.chat.GetTaskTimeline(c.Request.Context(), taskID, userID)
	if err != nil {
		handleAgentError(c, err, "failed to get agent task timeline")
		return
	}
	c.JSON(http.StatusOK, msgs)
}

// GetPendingPermissions godoc
// @Summary      获取任务待确认权限
// @Tags         Agent
// @Produce      json
// @Param        task_id  query     string  true  "任务 ID"
// @Success      200      {array}   agenttypes.PermissionRequest
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/task/permissions [get]
func (h *AgentHandler) GetPendingPermissions(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var q taskIDQueryParams
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

// sessionPageRequest 是会话列表分页请求。
//
// 关键字搜索复用 types.Pagination 的 keyword 字段（project 内所有会话按标题 /
// 发起人用户名模糊搜索），因此这里不再单独声明。
type sessionPageRequest struct {
	types.Pagination
}

// sessionGetQuery 是获取会话详情的查询参数。
//
// before_seq 是向上翻页游标：只返回 seq 更小的消息；0 表示取最新一页。
// limit 是本页最大条数（后端有默认值和上限），首页不传即可。
type sessionGetQuery struct {
	ID        string `form:"id" binding:"required"`
	BeforeSeq int    `form:"before_seq"`
	Limit     int    `form:"limit"`
}

// PageSessions godoc
// @Summary      分页查询当前项目下的所有会话
// @Description  返回当前激活项目下的所有会话（不限定发起人），带 username；按 updated_at 倒序，支持 keyword 标题/用户名搜索
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.sessionPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/session/page [post]
func (h *AgentHandler) PageSessions(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req sessionPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	projectID, err := h.activeProjectID(c.Request.Context(), userID)
	if err != nil {
		handleAgentError(c, err, "failed to resolve active project")
		return
	}

	sessions, total, err := h.chat.ListSessions(c.Request.Context(), projectID, req.GetKeyword(), req.Offset(), req.Limit())
	if err != nil {
		handleAgentError(c, err, "failed to page sessions")
		return
	}

	// 会话列表只返回会话本体（含 Title），不再附带历史消息：
	// 消息体积大且列表不使用，拉取全部会话消息会造成明显的额外开销。
	// 需要消息时由前端按会话 ID 调用 GetSession 分页获取。
	c.JSON(http.StatusOK, gin.H{
		"data":      sessions,
		"total":     total,
		"page":      req.GetPage(),
		"page_size": req.GetPageSize(),
	})
}

// GetSession godoc
// @Summary      获取会话（含一页历史消息）
// @Description  默认只返回最新一页消息；向上滚动时把响应的 next_cursor 作为 before_seq 继续加载更早的消息
// @Tags         Agent
// @Produce      json
// @Param        id          query     string  true   "会话 ID"
// @Param        before_seq  query     int     false  "向上翻页游标（上一页的 next_cursor），0 表示最新一页"
// @Param        limit       query     int     false  "本页最大条数（默认 30，最大 200）"
// @Success      200  {object}  agentservice.SessionDetail
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      403  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/session/get [get]
func (h *AgentHandler) GetSession(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var q sessionGetQuery
	if err := c.ShouldBindQuery(&q); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	id, err := parseAgentID(q.ID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid id").WithDetails(err.Error()))
		return
	}
	detail, err := h.chat.GetSession(c.Request.Context(), id, agentservice.SessionMessageQuery{
		BeforeSeq: q.BeforeSeq,
		Limit:     q.Limit,
	})
	if err != nil {
		handleAgentError(c, err, "failed to get session")
		return
	}
	// 可见性校验：会话按项目共享，项目成员都能查看；非项目成员返回 403。
	if err := ensureProjectAccessible(c.Request.Context(), h.projects, detail.ProjectID, userID); err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, detail)
}

// sessionForkRequest 是 fork 会话的请求体。
type sessionForkRequest struct {
	ID    string `json:"id" binding:"required"` // 源会话 ID
	Title string `json:"title"`                 // 为空则沿用原标题 + " (fork)"
	// UpToSeq 只复制 seq <= up_to_seq 的历史（含该条）；未传则复制到末尾。
	// 用指针区分「未传」与「传 0（只复制第一条）」。
	UpToSeq   *int `json:"up_to_seq"`
	CopyFiles bool `json:"copy_files"` // 是否同时复制文件历史最新版本
}

// ForkSession godoc
// @Summary      Fork 会话（复制历史到新的独立会话）
// @Description  把源会话的消息复制成一个新的顶层会话并返回其详情（含首屏消息），源会话不变；
// @Description  up_to_seq 可指定只复制到某条消息为止的历史（从该消息处分叉）。
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.sessionForkRequest  true  "fork 参数"
// @Success      200      {object}  agentservice.SessionDetail
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/session/fork [post]
func (h *AgentHandler) ForkSession(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req sessionForkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	id, err := parseAgentID(req.ID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid id").WithDetails(err.Error()))
		return
	}

	detail, err := h.chat.ForkSession(c.Request.Context(), agenttypes.SessionForkParams{
		SourceSessionID: id,
		UserID:          userID,
		Title:           strings.TrimSpace(req.Title),
		UpToSeq:         req.UpToSeq,
		CopyFiles:       req.CopyFiles,
	})
	if err != nil {
		handleAgentError(c, err, "failed to fork session")
		return
	}
	c.JSON(http.StatusOK, detail)
}

// DeleteSession godoc
// @Summary      删除 Agent 会话
// @Description  删除会话（含子会话）并级联清理消息、文件历史、任务、权限 / 提问请求、
// @Description  会话资源关联与归属该会话的记忆；资源关联只删除关联行，不删除脚本 / 工作流等资源本身；
// @Description  会话按项目共享，项目成员均可删除；仍有正在执行的一轮任务时返回 409，需先取消任务或等待本轮结束
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      taskIDBody  true  "会话 ID"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      403      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      409      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/session/delete [post]
func (h *AgentHandler) DeleteSession(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
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

	// 与 GetSession 一致：会话按项目共享，项目成员都能删除。
	sess, err := h.chat.GetSessionMeta(c.Request.Context(), id)
	if err != nil {
		handleAgentError(c, err, "failed to get session")
		return
	}
	if err := ensureProjectAccessible(c.Request.Context(), h.projects, sess.ProjectID, userID); err != nil {
		c.Error(err)
		return
	}

	if err := h.chat.DeleteSession(c.Request.Context(), id); err != nil {
		handleAgentError(c, err, "failed to delete session")
		return
	}
	// 会话已不存在：清掉所有用户（含其他项目成员）在该会话上的实时订阅，
	// 避免事件桥残留永远不会再有事件的记录。
	h.dropSessionSubscribers(id)
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": body.ID})
}

// resolveTurnSession 校验任务存在、取所属会话，并校验当前用户对该会话的项目可见性。
func (h *AgentHandler) resolveTurnSession(c *gin.Context, taskID string, userID int64) (int64, bool) {
	id, err := parseAgentID(taskID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid task_id").WithDetails(err.Error()))
		return 0, false
	}
	sessionID, err := h.chat.GetTaskTimelineOwner(c.Request.Context(), id)
	if err != nil {
		handleAgentError(c, err, "failed to load task")
		return 0, false
	}
	sess, err := h.chat.GetSessionMeta(c.Request.Context(), sessionID)
	if err != nil {
		handleAgentError(c, err, "failed to get session")
		return 0, false
	}
	if err := ensureProjectAccessible(c.Request.Context(), h.projects, sess.ProjectID, userID); err != nil {
		c.Error(err)
		return 0, false
	}
	return sessionID, true
}

// DeleteTurn godoc
// @Summary      按任务删除一轮对话的消息
// @Description  删除某个任务关联的全部消息（含该轮的 user 消息与时间线）；不删除任务记录本身；
// @Description  任务仍在执行时返回 409
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      taskIDBody  true  "任务 ID"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      403      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      409      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/task/delete [post]
func (h *AgentHandler) DeleteTurn(c *gin.Context) {
	user, ok := getCurrentUser(c)
	if !ok {
		return
	}
	var body taskIDBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	if _, ok := h.resolveTurnSession(c, body.ID, user.ID); !ok {
		return
	}

	taskID, err := parseAgentID(body.ID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid id").WithDetails(err.Error()))
		return
	}
	if err := h.chat.DeleteTaskMessages(c.Request.Context(), taskID); err != nil {
		handleAgentError(c, err, "failed to delete turn messages")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": body.ID})
}

// RetryTurn godoc
// @Summary      重试某一轮
// @Description  删除该任务关联的消息（含 user 消息），用该轮原用户输入重新发起一轮，由 Agent 重新生成；
// @Description  返回新任务与会话 ID；任务仍在执行时返回 409
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      taskIDBody  true  "任务 ID"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      403      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      409      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/task/retry [post]
func (h *AgentHandler) RetryTurn(c *gin.Context) {
	user, ok := getCurrentUser(c)
	if !ok {
		return
	}
	var body taskIDBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	sessionID, ok := h.resolveTurnSession(c, body.ID, user.ID)
	if !ok {
		return
	}
	taskID, err := parseAgentID(body.ID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid id").WithDetails(err.Error()))
		return
	}

	// 与 /agent/chat 完全一致的上下文解析（项目 / 工作目录由 ChatService 统一解析）。
	task, sess, err := h.chat.RetryTurn(c.Request.Context(), agentservice.TurnInput{
		UserID:    user.ID,
		SessionID: sessionID,
		ProfileID: user.AgentConfig.ProfileID,
	}, taskID)
	if err != nil {
		handleAgentError(c, err, "failed to retry conversation turn")
		return
	}

	h.subscribeSession(sess.ID, user.ID)
	if err := h.chat.StartTurn(c.Request.Context(), task.ID); err != nil {
		handleAgentError(c, err, "failed to start conversation turn")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"task_id":         strconv.FormatInt(task.ID, 10),
		"conversation_id": strconv.FormatInt(sess.ID, 10),
	})
}

// sessionForkTurnRequest 是「从某一轮 fork 并重试」的请求体。
type sessionForkTurnRequest struct {
	// ID 是源会话 ID。
	ID string `json:"id" binding:"required"`
	// UpToSeq 只复制 seq <= up_to_seq 的历史（含该条）；0 / 未传表示复制到末尾。
	UpToSeq *int `json:"up_to_seq"`
	// CopyFiles 是否同时复制文件历史最新版本。
	CopyFiles bool `json:"copy_files"`
	// Message 覆盖该轮的原用户输入（为空时取截止历史里的最后一条用户消息）。
	Message  string `json:"message"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// ForkTurn godoc
// @Summary      从某一轮 fork 并重试
// @Description  以源会话截止到指定消息的历史创建新会话（消息归属到新任务），并把该轮的
// @Description  用户输入重新交给 Agent 生成；源会话不变；返回新任务与新会话 ID
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.sessionForkTurnRequest  true  "fork 并重试参数"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      403      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/session/fork-retry [post]
func (h *AgentHandler) ForkTurn(c *gin.Context) {
	user, ok := getCurrentUser(c)
	if !ok {
		return
	}
	var req sessionForkTurnRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	sessionID, err := parseAgentID(req.ID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid id").WithDetails(err.Error()))
		return
	}
	sess, err := h.chat.GetSessionMeta(c.Request.Context(), sessionID)
	if err != nil {
		handleAgentError(c, err, "failed to get session")
		return
	}
	if err := ensureProjectAccessible(c.Request.Context(), h.projects, sess.ProjectID, user.ID); err != nil {
		c.Error(err)
		return
	}

	upToSeq := int64(0)
	if req.UpToSeq != nil {
		upToSeq = int64(*req.UpToSeq)
	}

	task, forkedSess, err := h.chat.ForkTurn(c.Request.Context(), agentservice.TurnInput{
		UserID:    user.ID,
		SessionID: sessionID,
		Message:   req.Message,
		Provider:  req.Provider,
		Model:     req.Model,
		ProfileID: user.AgentConfig.ProfileID,
	}, upToSeq, req.CopyFiles)
	if err != nil {
		handleAgentError(c, err, "failed to fork and retry turn")
		return
	}

	h.subscribeSession(forkedSess.ID, user.ID)
	if err := h.chat.StartTurn(c.Request.Context(), task.ID); err != nil {
		handleAgentError(c, err, "failed to start conversation turn")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"task_id":         strconv.FormatInt(task.ID, 10),
		"conversation_id": strconv.FormatInt(forkedSess.ID, 10),
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

// SaveMemory godoc
// @Summary      创建或更新记忆
// @Description  ID 为空时新建，否则更新；UserID 由当前登录用户注入
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.memorySaveRequest  true  "记忆内容"
// @Success      200      {object}  agenttypes.Memory
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

	var memory *agenttypes.Memory
	if req.ID != 0 {
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
		existing.ProjectID = req.ProjectID
		existing.SessionID = req.SessionID
		existing.Kind = req.Kind
		existing.Content = req.Content
		existing.Importance = req.Importance
		existing.Metadata = req.Metadata
		memory = existing
	} else {
		memory = &agenttypes.Memory{
			UserID:     userID,
			ProjectID:  req.ProjectID,
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
// @Success      200  {object}  agenttypes.Memory
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

	var q memoryIDQuery
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
// @Param        request  body      memoryIDBody  true  "记忆 ID"
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

	var body memoryIDBody
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
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": strconv.FormatInt(body.ID, 10)})
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
// @Success      200      {array}   agenttypes.Memory
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
// @Success      200      {array}   agenttypes.QuestionRequest
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

// ResumeTask godoc
// @Summary      从持久化检查点继续执行 Agent 任务
// @Description  用于后端重启 / 进程崩溃导致执行循环退出、但任务仍有可恢复检查点的场景。
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      taskIDBody  true  "任务 ID"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      409      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/task/resume [post]
func (h *AgentHandler) ResumeTask(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
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
	// 续跑也会产生实时事件：把发起人登记为订阅者（幂等；需先拿到 SessionID）。
	if task, taskErr := h.svc.GetTask(c.Request.Context(), id); taskErr == nil && task != nil {
		h.subscribeSession(task.SessionID, userID)
	}
	if err := h.svc.ResumeTask(c.Request.Context(), id); err != nil {
		handleAgentError(c, err, "failed to resume agent task")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "id": body.ID})
}

// StartEventStream 订阅进程内 Agent 事件，并交给 sessionEventBridge 按会话路由。
//
// 这里仍然只向 EventBus 订阅一次（而不是每个页面订阅一次）：订阅关系保存在
// sessionEventBridge 的注册表里，事件到达时按 SessionID 查表决定推给谁；
// 页面只调用 /agent/session/subscribe 登记、/agent/session/unsubscribe 撤销。
//
// 不在推送时按 UserID 直推，因此：
//   - 同一会话 / 项目内的多个用户可以同时看到同一份实时流；
//   - 没有打开会话页的用户不会收到任何 Agent 事件。
//
// 未绑定会话的一次性任务（SessionID == 0）仍按事件归属用户直推（见 dispatch）。
//
// 同时订阅 realtime.Hub 的连接在线状态：用户最后一条连接断开超过宽限期后，
// 清理该用户的全部会话订阅——这是显式退订之外唯一的兜底，且不会因某一轮任务
// 结束而把仍在会话页的其他用户踢掉。
func (h *AgentHandler) StartEventStream() {
	if h.svc == nil || h.hub == nil || h.stream == nil {
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.unsub != nil {
		return
	}

	h.unsub = h.svc.Subscribe(func(ctx context.Context, ev agenttypes.AgentEvent) error {
		h.stream.dispatch(ctx, ev)
		return nil
	})
	h.presenceUnsub = h.hub.SubscribePresence(h.stream.setOnline)
}

// StopEventStream 取消全局事件订阅（供容器关闭 / 测试清理使用）。
func (h *AgentHandler) StopEventStream() {
	h.mu.Lock()
	unsub := h.unsub
	presenceUnsub := h.presenceUnsub
	h.unsub = nil
	h.presenceUnsub = nil
	h.mu.Unlock()
	if unsub != nil {
		unsub()
	}
	if presenceUnsub != nil {
		presenceUnsub()
	}
	h.stream.stop()
}

// activeProjectID 返回当前用户激活项目 ID（新建会话 / 会话列表按项目隔离时使用）。
//
// 这里只暴露「userID → projectID」这一层能力：会话工作目录由
// Session.EnsureWorkingDir 从存储根目录统一派生，不在 HTTP 层拼接路径。
func (h *AgentHandler) activeProjectID(ctx context.Context, userID int64) (int64, error) {
	if h.runtimeCtx == nil {
		return 0, stderrs.New("runtime context resolver is not configured")
	}
	_, project, err := h.runtimeCtx.Resolve(ctx, userID)
	if err != nil {
		return 0, err
	}
	if project == nil || project.ID == 0 {
		return 0, stderrs.New("active project is empty")
	}
	return project.ID, nil
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
	case stderrs.Is(err, agenttypes.ErrTaskNotFound), stderrs.Is(err, agenttypes.ErrPermissionNotFound),
		stderrs.Is(err, agenttypes.ErrQuestionNotFound),
		stderrs.Is(err, agenttypes.ErrSessionNotFound), stderrs.Is(err, agenttypes.ErrMemoryNotFound):
		c.Error(errors.NewNotFoundError("record not found"))
	case stderrs.Is(err, agenttypes.ErrPermissionNotPending),
		stderrs.Is(err, agenttypes.ErrQuestionNotPending),
		stderrs.Is(err, agenttypes.ErrInvalidPermissionTransition),
		stderrs.Is(err, agenttypes.ErrInvalidQuestionTransition),
		stderrs.Is(err, agenttypes.ErrUnknownQuestionID),
		stderrs.Is(err, agenttypes.ErrTaskAlreadyRunning),
		stderrs.Is(err, agenttypes.ErrTaskNotResumable),
		stderrs.Is(err, agenttypes.ErrSessionBusy),
		stderrs.Is(err, agentservice.ErrTurnRunning):
		c.Error(errors.NewConflictError(err.Error()))
	default:
		c.Error(errors.NewInternalServerError(internalMsg).WithDetails(err.Error()))
	}
}
