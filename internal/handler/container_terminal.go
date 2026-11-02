package handler

import (
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

// ContainerTemplateTerminal 通过 WebSocket 连接「容器模板临时终端」。
//
// 与历史实现的区别：容器不再随连接创建 / 销毁，而是收敛到进程内的会话注册表
// （manager.TerminalSessionRegistry），key = (userID, projectID, templateID)。
//
//   - 同一个模板重复打开只会 attach 到同一个 PTY，不会重复创建容器；
//   - 一旦前端接入，会话即被标记为「持久」：断开连接（刷新 / 关标签 / 断网）都不会删除容器，
//     只有空闲 TTL（container.terminal_session_ttl_minutes）到期或前端关闭标签调用
//     DELETE /container/template/{id}/terminal 时才删除；
//   - 输出环形缓冲带序号，连接时可用 ?since=<seq> 重放（缺省回放全部缓存）。
//
// 协议：
//   - 浏览器 → 后端：二进制/文本都是按键输入；文本 {"type":"resize",...} 是尺寸控制帧；
//   - 后端 → 浏览器：二进制为 PTY 输出；文本为 JSON 控制事件（session / exec / exit）
//     或纯文本提示。
//
// @Summary      容器模板终端
// @Description  用容器模板创建（或复用）会话并打开交互式终端；断开后按配置立即删除或保留一段时间。
// @Tags         容器管理
// @Param        templateId  path   int  true   "容器模板 ID"
// @Param        since       query  int  false  "从指定输出序号重放（缺省回放全部缓存）"
// @Success      101
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Failure      500  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template/{templateId}/terminal [get]
func (h *ContainerHandler) ContainerTemplateTerminal(c *gin.Context) {
	key, ok := h.resolveTerminalSessionKey(c)
	if !ok {
		return
	}
	if h.terminalRegistry == nil {
		c.Error(apperrors.NewInternalServerError("container terminal registry is not configured"))
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

	session, _, err := h.terminalRegistry.EnsurePersistent(c.Request.Context(), key)
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("\x1b[31mfailed to open terminal: "+err.Error()+"\x1b[0m\r\n"))
		return
	}

	sub, replay := session.Attach(sinceSeq)
	defer session.Detach(sub)

	if len(replay) > 0 {
		if err := conn.WriteMessage(websocket.BinaryMessage, replay); err != nil {
			return
		}
	} else {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("\x1b[90mcontainer ready.\x1b[0m\r\n"))
	}
	session.SendEvent(map[string]any{
		"type":       "session",
		"session_id": session.ID(),
		"status":     "ready",
	})

	bridgeManagedTerminalWebSocket(conn, session, sub)
}

type containerTerminalExecRequest struct {
	Command         string `json:"command" binding:"required"`
	WaitSeconds     int    `json:"wait_seconds"`
	CreateIfMissing *bool  `json:"create_if_missing"`
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

// ContainerTemplateTerminalExec 向「当前用户的容器模板终端」写入一条命令。
//
// 语义：
//   - 会话不存在时默认自动创建（create_if_missing=false 可关闭）；
//   - wait_seconds=0：只投递命令，立即返回 accepted=true / running=true，
//     命令输出通过该终端的 WebSocket 实时回传；
//   - wait_seconds>0：等待命令结束（或超时），返回退出码与输出尾部。
//
// 同一会话的命令串行执行；Agent 的 terminal_exec 工具复用同一实现。
//
// @Summary      容器模板终端执行命令
// @Description  向当前用户该模板的终端会话写入命令；wait_seconds=0 时为异步投递。
// @Tags         容器管理
// @Accept       json
// @Produce      json
// @Param        templateId  path  int  true  "容器模板 ID"
// @Param        request     body  handler.containerTerminalExecRequest  true  "命令"
// @Success      200  {object}  handler.containerTerminalExecResponse
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Failure      409  {object}  errors.AppError
// @Failure      500  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template/{templateId}/terminal/exec [post]
func (h *ContainerHandler) ContainerTemplateTerminalExec(c *gin.Context) {
	key, ok := h.resolveTerminalSessionKey(c)
	if !ok {
		return
	}
	if h.terminalRegistry == nil {
		c.Error(apperrors.NewInternalServerError("container terminal registry is not configured"))
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

	createIfMissing := true
	if req.CreateIfMissing != nil {
		createIfMissing = *req.CreateIfMissing
	}

	timeout := time.Duration(req.WaitSeconds) * time.Second
	result, err := h.terminalRegistry.Execute(c.Request.Context(), key, manager.TerminalCommand{
		Command:         command,
		Wait:            req.WaitSeconds > 0,
		Timeout:         timeout,
		Source:          "ui",
		NotifyUI:        false,
		CreateIfMissing: createIfMissing,
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
// @Summary      容器模板终端状态
// @Description  返回会话是否存在、存活、订阅者数量与当前输出序号。
// @Tags         容器管理
// @Param        templateId  path  int  true  "容器模板 ID"
// @Success      200  {object}  manager.TerminalSessionStatus
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template/{templateId}/terminal/status [get]
func (h *ContainerHandler) ContainerTemplateTerminalStatus(c *gin.Context) {
	key, ok := h.resolveTerminalSessionKey(c)
	if !ok {
		return
	}
	if h.terminalRegistry == nil {
		c.Error(apperrors.NewInternalServerError("container terminal registry is not configured"))
		return
	}
	c.JSON(http.StatusOK, h.terminalRegistry.Status(key))
}

// ContainerTemplateTerminalClose 主动结束会话并删除容器。
//
// @Summary      关闭容器模板终端
// @Description  结束会话（关闭 shell 并删除临时容器）。
// @Tags         容器管理
// @Param        templateId  path  int  true  "容器模板 ID"
// @Success      200  {object}  map[string]bool
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template/{templateId}/terminal [delete]
func (h *ContainerHandler) ContainerTemplateTerminalClose(c *gin.Context) {
	key, ok := h.resolveTerminalSessionKey(c)
	if !ok {
		return
	}
	if h.terminalRegistry == nil {
		c.Error(apperrors.NewInternalServerError("container terminal registry is not configured"))
		return
	}
	c.JSON(http.StatusOK, gin.H{"closed": h.terminalRegistry.CloseSession(key)})
}

// resolveTerminalSessionKey 校验用户 / 激活项目 / 模板并返回会话 key。
func (h *ContainerHandler) resolveTerminalSessionKey(c *gin.Context) (manager.TerminalSessionKey, bool) {
	var key manager.TerminalSessionKey

	userID, ok := getCurrentUserID(c)
	if !ok {
		return key, false
	}

	templateID, err := strconv.ParseInt(strings.TrimSpace(c.Param("templateId")), 10, 64)
	if err != nil || templateID <= 0 {
		c.Error(apperrors.NewValidationError("invalid container template id"))
		return key, false
	}

	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil || project == nil {
		c.Error(apperrors.NewNotFoundError("no active project for container terminal").WithDetails(errStringOrEmpty(err)))
		return key, false
	}
	if _, err := h.containerService.GetContainerTemplateByID(c.Request.Context(), templateID); err != nil {
		c.Error(apperrors.NewNotFoundError("container template not found").WithDetails(err.Error()))
		return key, false
	}

	return manager.TerminalSessionKey{
		UserID:     userID,
		ProjectID:  project.ID,
		TemplateID: templateID,
	}, true
}
