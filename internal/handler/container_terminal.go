package handler

import (
	"net/http"
	"strconv"
	"strings"

	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	apperrors "github.com/biox-dev/gobrave/internal/errors"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// ContainerTemplateTerminal 通过 WebSocket 打开一个临时容器终端，用于手动测试容器模板。
//
// 生命周期：
//  1. 按当前用户激活项目 + 容器模板创建一次性探针容器（长期 sleep，等待 exec）；
//  2. 在容器内执行 /bin/bash -l，把输入输出桥接到 xterm.js；
//  3. 浏览器关闭 WebSocket / 关闭弹窗后，删除该临时容器（不写 container_instance）。
//
// 这与 Agent 工具 probe_container_env 的设备口径一致（同一 buildProbePlan），
// 区别只是这里保持容器存活并打开交互式 shell。
//
// @Summary      容器模板临时终端
// @Description  用容器模板创建一次性容器并打开交互式终端；WebSocket 关闭时自动删除容器。
// @Tags         容器管理
// @Param        templateId  path  int  true  "容器模板 ID"
// @Success      101
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Failure      500  {object}  errors.AppError
// @Security     Bearer
// @Router       /container/template/{templateId}/terminal [get]
func (h *ContainerHandler) ContainerTemplateTerminal(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	templateID, err := strconv.ParseInt(strings.TrimSpace(c.Param("templateId")), 10, 64)
	if err != nil || templateID <= 0 {
		c.Error(apperrors.NewValidationError("invalid container template id"))
		return
	}

	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil || project == nil {
		c.Error(apperrors.NewNotFoundError("no active project for container terminal").WithDetails(errStringOrEmpty(err)))
		return
	}
	if h.probe == nil {
		c.Error(apperrors.NewInternalServerError("container probe is not configured"))
		return
	}
	if _, err := h.containerService.GetContainerTemplateByID(c.Request.Context(), templateID); err != nil {
		c.Error(apperrors.NewNotFoundError("container template not found").WithDetails(err.Error()))
		return
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

	_ = conn.WriteMessage(websocket.TextMessage, []byte("\x1b[90mpreparing temporary container...\x1b[0m\r\n"))

	session, cleanup, err := h.probe.OpenProbeTerminal(c.Request.Context(), project.ID, templateID, containerruntime.TerminalOptions{
		Command: []string{"/bin/bash", "-l"},
		TTY:     true,
		Rows:    24,
		Cols:    80,
	})
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("\x1b[31mfailed to open terminal: "+err.Error()+"\x1b[0m\r\n"))
		return
	}
	defer cleanup()
	defer session.Close()

	_ = conn.WriteMessage(websocket.TextMessage, []byte("\x1b[90mcontainer ready.\x1b[0m\r\n"))
	bridgeTerminalWebSocket(conn, session)
}
