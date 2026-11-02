package handler

import (
	"net/http"
	"strconv"
	"strings"

	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// AnalysisNodeTerminal 通过 WebSocket 把 xterm.js 连接到运行中的节点容器。
//
// 等价于 `kubectl exec -it <pod> -- /bin/bash` / `docker exec -it <container> /bin/bash`：
// 只在容器 running 时可连接；节点结束后连接会被拒绝。节点与容器的关系沿用 DAG 节点约定
// （OwnerType=dag_node, OwnerID=node.ID），因此命令型节点与脚本节点都适用。
func (h *AnalysisHandler) AnalysisNodeTerminal(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}
	nodeID, err := strconv.ParseInt(strings.TrimSpace(c.Param("analysisNodeId")), 10, 64)
	if err != nil || nodeID <= 0 {
		c.Error(errors.NewValidationError("invalid analysis node id"))
		return
	}

	node, err := h.analysisRepo.GetAnalysisNodeByID(c.Request.Context(), nodeID)
	if err != nil {
		c.Error(errors.NewNotFoundError("analysis node not found").WithDetails(err.Error()))
		return
	}
	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil || project == nil || node.ProjectID != project.ID {
		c.Error(errors.NewNotFoundError("analysis node not found in the active project"))
		return
	}
	if h.containerRepo == nil || h.containerManager == nil {
		c.Error(errors.NewInternalServerError("container terminal is not configured"))
		return
	}

	inst, err := h.containerRepo.GetContainerInstanceByOwner(c.Request.Context(), types.ContainerOwnerDagNode, node.ID)
	if err != nil || inst == nil {
		c.Error(errors.NewConflictError("analysis node has no container instance yet").WithDetails(errStringOrEmpty(err)))
		return
	}
	if inst.Status != types.ContainerRunning {
		c.Error(errors.NewConflictError("container is not running").WithDetails("status: " + string(inst.Status)))
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

	session, err := h.containerManager.OpenTerminal(c.Request.Context(), inst.ID, containerruntime.TerminalOptions{
		Command: []string{"/bin/bash", "-l"},
		TTY:     true,
		Rows:    24,
		Cols:    80,
	})
	if err != nil {
		_ = conn.WriteMessage(websocket.TextMessage, []byte("failed to open terminal: "+err.Error()+"\r\n"))
		return
	}
	defer session.Close()

	bridgeTerminalWebSocket(conn, session)
}

func errStringOrEmpty(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
