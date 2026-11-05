package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	apperrors "github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// 本文件实现「容器模板终端会话」的 HTTP / WebSocket 接口。
//
// 核心约定：终端会话就是「应用会话」（types.AppSession，表 go_container_app_session），
// 会话的对外标识就是 AppSession.ID；容器是 owner_type=app_session、owner_id=会话 ID 的
// 普通 ContainerInstance（启动命令为 terminalKeepAliveCommand，真正的 shell 由 exec 打开）。
//
//   - POST /container/terminal/session/create：按容器模板新建一条终端会话，
//     创建时即拉起容器与 shell，返回会话 ID（= AppSession.ID）；
//   - GET  /container/terminal/session/{id}/terminal：用会话 ID attach 到该 PTY
//     （只建立 WebSocket，不再按模板创建容器）；
//   - POST /container/terminal/session/{id}/terminal/exec：向该会话写命令；
//   - GET  /container/terminal/session/{id}/status：查询会话状态；
//   - DEL  /container/terminal/session/{id}：关闭会话（关 shell + 删容器 + 删会话）；
//   - GET  /container/app-session/list-by-page：终端会话列表（复用应用会话分页接口）。
//
// 一个容器模板可以有多条会话：UI 每点一次「新建」都新建一条独立会话；
// Agent 通过 terminal_exec 创建的会话按 (templateID, agentSessionID) 唯一。
//
// 会话一旦建立即为「持久」：断开 WebSocket（刷新 / 关标签 / 断网）不会删除容器，
// 只有空闲 TTL（container.terminal_session_ttl_minutes）到期或显式 DELETE 才回收。

// ContainerTerminalSessionCreateRequest 是「新建终端会话」的入参。
type ContainerTerminalSessionCreateRequest struct {
	// ContainerTemplateID 是必填的容器模板 ID（go_container_template_definition 主键）。
	ContainerTemplateID int64 `json:"container_template_id,string" binding:"required"`
	// Rows / Cols 是前端 xterm 的尺寸：新建会话直接用它建 PTY，
	// 保证历史输出的换行与前端渲染一致。为 0 时使用默认 24x80。
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

// ContainerTerminalSessionCreate 按容器模板新建一条终端会话并立即启动容器。
//
// @Summary      新建容器终端会话
// @Description  按容器模板创建终端会话（创建时即启动容器与 shell），返回会话 ID。
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        request  body  handler.ContainerTerminalSessionCreateRequest  true  "创建参数"
// @Success      200  {object}  manager.TerminalSessionStatus
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Failure      409  {object}  errors.AppError
// @Failure      500  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/terminal/session/create [post]
func (h *ContainerHandler) ContainerTerminalSessionCreate(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}
	if h.terminalRegistry == nil {
		c.Error(apperrors.NewInternalServerError("container terminal registry is not configured"))
		return
	}

	var req ContainerTerminalSessionCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	if req.ContainerTemplateID <= 0 {
		c.Error(apperrors.NewValidationError("container_template_id is required"))
		return
	}

	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil || project == nil {
		c.Error(apperrors.NewNotFoundError("no active project for container terminal").WithDetails(errStringOrEmpty(err)))
		return
	}

	session, rec, err := h.terminalRegistry.CreateForTemplate(
		c.Request.Context(), userID, project.ID, req.ContainerTemplateID, req.Rows, req.Cols,
	)
	if err != nil {
		switch {
		case errors.Is(err, manager.ErrTerminalSessionQuotaExceeded):
			c.Error(apperrors.NewConflictError("terminal session quota exceeded").WithDetails(err.Error()))
		default:
			c.Error(apperrors.NewInternalServerError("failed to create terminal session").WithDetails(err.Error()))
		}
		return
	}

	status := h.terminalRegistry.Status(session.Key())
	// 展示名由服务端在落库时自动生成（模板展示名 → 模板名 → 模板 ID），
	// 这里直接从持久化记录（AppSession）里带回去，前端不用再取一次列表 / 自己拼。
	if rec != nil {
		status.Name = strings.TrimSpace(rec.Name)
	}
	c.JSON(http.StatusOK, status)
}

// ContainerTemplateTerminal 通过 WebSocket 连接「已有的终端会话」。
//
// 只按会话 ID attach，不再支持按 templateId 建立连接（容器由 create 接口创建）。
//
// 协议：
//   - 浏览器 → 后端：二进制/文本都是按键输入；文本 {"type":"resize",...} 是尺寸控制帧；
//   - 后端 → 浏览器：二进制为 PTY 输出；文本为 JSON 控制事件（session / exec / exit）
//     或纯文本提示。
//
// @Summary      容器终端会话 WebSocket
// @Description  用终端会话 ID 接入交互式终端；会话不在内存时会按数据库记录自动认领原容器并重建 shell。
// @Tags         容器管理
// @Param        id     path   int  true   "终端会话 ID"
// @Param        since  query  int  false  "从指定输出序号重放（缺省回放全部缓存）"
// @Param        rows   query  int  false  "PTY 行数（浏览器 xterm 尺寸，可选）"
// @Param        cols   query  int  false  "PTY 列数（浏览器 xterm 尺寸，可选）"
// @Success      101
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Failure      500  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/terminal/session/{id}/terminal [get]
func (h *ContainerHandler) ContainerTemplateTerminal(c *gin.Context) {
	rows, cols := terminalSizeFromQuery(c)
	session, ok := h.resolveTerminalSession(c, rows, cols)
	if !ok {
		return
	}

	sinceSeq := int64(0)
	if raw := strings.TrimSpace(c.Query("since")); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
			sinceSeq = parsed
		}
	}

	upgrader := websocket.Upgrader{
		ReadBufferSize:  4096,
		WriteBufferSize: 4096,
		CheckOrigin:     func(_ *http.Request) bool { return true },
	}
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	_ = conn.WriteMessage(websocket.TextMessage, []byte("\x1b[90mpreparing container terminal...\x1b[0m\r\n"))

	sub, replay := session.AttachWithInfo(sinceSeq)
	// 前端接入即把会话升级为持久会话（标记只增不减）：这样 Agent 用一次性会话
	// 跑的长命令，用户点开终端后即使命令结束 / 刷新页面也不会被回收，可以继续查看输出。
	// 必须在 Detach 之前完成（一次性会话在最后一个订阅者离开时会被立即回收）；
	// 不跟随请求 context：WS 升级后它可能已被取消，而这里是一次 DB 写。
	h.terminalRegistry.MarkPersistent(context.Background(), session.Key())
	defer session.Detach(sub)

	// 先下发回放元信息，再下发回放字节：前端据此决定是否清屏（reset）、
	// 下次重连从哪个序号续传（seq），以及用哪个宽度渲染回放（rows/cols）。
	ptyRows, ptyCols := session.Size()
	if meta, err := json.Marshal(map[string]any{
		"type":         "session",
		"session_id":   session.ID(),
		"status":       "ready",
		"seq":          replay.To,
		"replay_from":  replay.From,
		"replay_bytes": len(replay.Data),
		"reset":        replay.Reset,
		"truncated":    replay.Truncated,
		"rows":         ptyRows,
		"cols":         ptyCols,
	}); err == nil {
		if err := conn.WriteMessage(websocket.TextMessage, meta); err != nil {
			return
		}
	}

	if len(replay.Data) > 0 {
		if err := conn.WriteMessage(websocket.BinaryMessage, replay.Data); err != nil {
			return
		}
	} else {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("\x1b[90mcontainer ready.\x1b[0m\r\n"))
	}

	bridgeManagedTerminalWebSocket(conn, session, sub)
}

type containerTerminalExecRequest struct {
	Command     string `json:"command" binding:"required"`
	WaitSeconds int    `json:"wait_seconds"`
}

type containerTerminalExecResponse struct {
	SessionID       string  `json:"session_id"`
	Accepted        bool    `json:"accepted"`
	Running         bool    `json:"running,omitempty"`
	ExitCode        int     `json:"exit_code"`
	TimedOut        bool    `json:"timed_out,omitempty"`
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	Output          string  `json:"output,omitempty"`
	OutputTruncated bool    `json:"output_truncated,omitempty"`
}

// ContainerTemplateTerminalExec 向「指定的终端会话」写入一条命令。
//
// 语义：
//   - 只能写入已存在的会话（容器由 create 接口创建，这里不再自动创建）；
//   - wait_seconds=0：只投递命令，立即返回 accepted=true / running=true，
//     命令输出通过该会话的 WebSocket 实时回传；
//   - wait_seconds>0：等待命令结束（或超时），返回退出码与输出尾部。
//
// 同一会话的命令串行执行；Agent 的 terminal_exec 工具复用同一底层实现。
//
// @Summary      容器终端会话执行命令
// @Description  向指定终端会话写入命令；wait_seconds=0 时为异步投递。
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        id       path  int  true  "终端会话 ID"
// @Param        request  body  handler.containerTerminalExecRequest  true  "命令"
// @Success      200  {object}  handler.containerTerminalExecResponse
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Failure      409  {object}  errors.AppError
// @Failure      500  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/terminal/session/{id}/terminal/exec [post]
func (h *ContainerHandler) ContainerTemplateTerminalExec(c *gin.Context) {
	session, ok := h.resolveTerminalSession(c, 0, 0)
	if !ok {
		return
	}

	var req containerTerminalExecRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(apperrors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	command := strings.TrimSpace(req.Command)
	if command == "" {
		c.Error(apperrors.NewValidationError("command is required"))
		return
	}
	if req.WaitSeconds < 0 {
		req.WaitSeconds = 0
	}

	result, err := h.terminalRegistry.Execute(c.Request.Context(), session.Key(), manager.TerminalCommand{
		Command: command,
		Wait:    req.WaitSeconds > 0,
		Timeout: time.Duration(req.WaitSeconds) * time.Second,
		Source:  "ui",
		// 会话由 create 接口建立且已持久；这里只投递，不自动创建。
		CreateIfMissing: false,
	})
	if err != nil {
		switch {
		case err == manager.ErrTerminalSessionNotFound:
			c.Error(apperrors.NewNotFoundError("terminal session does not exist").WithDetails(err.Error()))
		case err == manager.ErrTerminalBusy:
			c.Error(apperrors.NewConflictError("terminal session is busy").WithDetails(err.Error()))
		case err == manager.ErrTerminalSessionClosed:
			c.Error(apperrors.NewConflictError("terminal session is closed").WithDetails(err.Error()))
		case errors.Is(err, manager.ErrTerminalSessionQuotaExceeded):
			c.Error(apperrors.NewConflictError("terminal session quota exceeded").WithDetails(err.Error()))
		default:
			c.Error(apperrors.NewInternalServerError("failed to execute terminal command").WithDetails(err.Error()))
		}
		return
	}

	c.JSON(http.StatusOK, containerTerminalExecResponse{
		SessionID:       result.SessionID,
		Accepted:        result.Accepted,
		Running:         result.Running,
		ExitCode:        result.ExitCode,
		TimedOut:        result.TimedOut,
		DurationSeconds: result.DurationSeconds,
		Output:          result.Output,
		OutputTruncated: result.OutputTruncated,
	})
}

// ContainerTemplateTerminalStatus 返回会话状态（用于前端判断是否需要重放 / 重连）。
//
// @Summary      容器终端会话状态
// @Description  返回会话是否存在、存活、订阅者数量与当前输出序号。
// @Tags         容器管理
// @Param        id  path  int  true  "终端会话 ID"
// @Success      200  {object}  manager.TerminalSessionStatus
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/terminal/session/{id}/status [get]
func (h *ContainerHandler) ContainerTemplateTerminalStatus(c *gin.Context) {
	id, ok := h.resolveTerminalSessionID(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, h.terminalRegistry.Status(manager.TerminalSessionKey{ID: id}))
}

// ContainerTemplateTerminalClose 主动结束会话并删除容器。
//
// @Summary      关闭容器终端会话
// @Description  结束会话（关闭 shell 并删除容器）。
// @Tags         容器管理
// @Param        id  path  int  true  "终端会话 ID"
// @Success      200  {object}  map[string]bool
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/terminal/session/{id} [delete]
func (h *ContainerHandler) ContainerTemplateTerminalClose(c *gin.Context) {
	if h.terminalRegistry == nil {
		c.Error(apperrors.NewInternalServerError("container terminal registry is not configured"))
		return
	}
	id, ok := h.resolveTerminalSessionID(c)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{"closed": h.terminalRegistry.CloseSession(manager.TerminalSessionKey{ID: id})})
}

// resolveTerminalSessionID 校验并解析路径里的终端会话 ID（= AppSession.ID）。
//
// 同时校验会话归属：只能操作当前用户自己创建的会话（防止越权操作他人容器）。
func (h *ContainerHandler) resolveTerminalSessionID(c *gin.Context) (int64, bool) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return 0, false
	}
	if h.terminalRegistry == nil {
		c.Error(apperrors.NewInternalServerError("container terminal registry is not configured"))
		return 0, false
	}
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param("id")), 10, 64)
	if err != nil || id <= 0 {
		c.Error(apperrors.NewValidationError("invalid terminal session id"))
		return 0, false
	}
	// 内存里有会话时直接校验归属；没有时先看数据库（进程重启后会话仍在）。
	if sess := h.terminalRegistry.LookupByID(id); sess != nil {
		if sess.Key().UserID != userID {
			c.Error(apperrors.NewNotFoundError("terminal session does not exist"))
			return 0, false
		}
		return id, true
	}
	if rec, err := h.terminalRegistry.SessionRecord(c.Request.Context(), id); err != nil || rec == nil || rec.UserID != userID {
		c.Error(apperrors.NewNotFoundError("terminal session does not exist"))
		return 0, false
	}
	return id, true
}

// resolveTerminalSession 解析并校验路径里的终端会话 ID，返回托管会话。
//
// 内存里没有时会自动「重建」：按数据库记录认领原容器并重新 exec 一个 shell
// （见 manager.TerminalSessionRegistry.ReviveByID）。这样进程重启后前端用同一个
// 会话 ID 重连即可继续使用，而不会因会话不在内存里报 404。
//
// rows/cols 是前端 xterm 的尺寸（可为 0）：重建的 PTY 直接用它，保证宽度与前端一致。
func (h *ContainerHandler) resolveTerminalSession(c *gin.Context, rows, cols uint16) (*manager.ManagedTerminalSession, bool) {
	id, ok := h.resolveTerminalSessionID(c)
	if !ok {
		return nil, false
	}
	sess, err := h.terminalRegistry.ReviveByID(c.Request.Context(), id, rows, cols)
	if err != nil || sess == nil {
		c.Error(apperrors.NewNotFoundError("terminal session does not exist").WithDetails(errStringOrEmpty(err)))
		return nil, false
	}
	return sess, true
}

// terminalSizeFromQuery 读取前端上报的 xterm 行列（?rows=&cols=）。
//
// 缺省 / 非法 / 只给了其中一项时返回 0：交给会话层保持原尺寸 / 使用默认值处理。
func terminalSizeFromQuery(c *gin.Context) (uint16, uint16) {
	parse := func(name string) uint16 {
		raw := strings.TrimSpace(c.Query(name))
		if raw == "" {
			return 0
		}
		parsed, err := strconv.ParseUint(raw, 10, 16)
		if err != nil {
			return 0
		}
		return uint16(parsed)
	}
	rows, cols := parse("rows"), parse("cols")
	if rows == 0 || cols == 0 {
		return 0, 0
	}
	return rows, cols
}
