package handler

import (
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"strings"

	containerruntime "github.com/biox-dev/gobrave/internal/container_runtime"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// AgentAnalysisHandler 暴露 AgentAnalysis 的 CRUD 与交互式终端。
type AgentAnalysisHandler struct {
	service          interfaces.AgentAnalysisService
	projectService   interfaces.ProjectService
	containerRepo    interfaces.ContainerRepository
	containerManager *manager.ContainerManager
}

func NewAgentAnalysisHandler(
	service interfaces.AgentAnalysisService,
	projectService interfaces.ProjectService,
	containerRepo interfaces.ContainerRepository,
	containerManager *manager.ContainerManager,
) *AgentAnalysisHandler {
	return &AgentAnalysisHandler{
		service:          service,
		projectService:   projectService,
		containerRepo:    containerRepo,
		containerManager: containerManager,
	}
}

type agentAnalysisCreateRequest struct {
	Name         string `json:"name" binding:"required"`
	TemplateID   string `json:"template_id" binding:"required"`
	Command      string `json:"command" binding:"required"`
	WorkspaceDir string `json:"workspace_dir"`
}

type agentAnalysisUpdateRequest struct {
	ID           int64   `json:"id,string" binding:"required"`
	Name         *string `json:"name"`
	TemplateID   *string `json:"template_id"`
	Command      *string `json:"command"`
	WorkspaceDir *string `json:"workspace_dir"`
}

// CreateAgentAnalysis 创建并提交一个 AgentAnalysis 容器任务（不等待完成）。
func (h *AgentAnalysisHandler) CreateAgentAnalysis(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}
	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil || project == nil {
		c.Error(errors.NewValidationError("project not found").WithDetails(errString(err)))
		return
	}

	var req agentAnalysisCreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	templateID, err := parseTemplateID(req.TemplateID)
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	item, err := h.service.CreateAgentAnalysis(c.Request.Context(), interfaces.AgentAnalysisCreateInput{
		ProjectID:    project.ID,
		Name:         req.Name,
		TemplateID:   templateID,
		Command:      req.Command,
		WorkspaceDir: req.WorkspaceDir,
	})
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}
	c.JSON(http.StatusOK, item)
}

// ListAgentAnalysis 列出当前项目下的 AgentAnalysis，最新的在前。
func (h *AgentAnalysisHandler) ListAgentAnalysis(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}
	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil || project == nil {
		c.Error(errors.NewValidationError("project not found").WithDetails(errString(err)))
		return
	}

	items, err := h.service.ListAgentAnalysisByProjectID(c.Request.Context(), project.ID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to list agent analysis").WithDetails(err.Error()))
		return
	}
	c.JSON(http.StatusOK, items)
}

// GetAgentAnalysis 按 ID 查询当前项目的 AgentAnalysis。
func (h *AgentAnalysisHandler) GetAgentAnalysis(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Query("id")), 10, 64)
	if err != nil || id <= 0 {
		c.Error(errors.NewValidationError("invalid agent analysis id"))
		return
	}
	item, ok := h.getOwnedAgentAnalysis(c, id)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, item)
}

// AgentAnalysisLog 返回任务的命令日志。
//
// 日志文件由 runner 通过 `bash ./run.sh 2>&1 | tee command.log` 生成，
// 路径即 types.AgentAnalysis.LogPath（WorkspaceDir/command.log）。
func (h *AgentAnalysisHandler) AgentAnalysisLog(c *gin.Context) {
	id, err := strconv.ParseInt(strings.TrimSpace(c.Query("id")), 10, 64)
	if err != nil || id <= 0 {
		c.Error(errors.NewValidationError("invalid agent analysis id"))
		return
	}
	item, ok := h.getOwnedAgentAnalysis(c, id)
	if !ok {
		return
	}

	logPath := strings.TrimSpace(item.LogPath)
	if logPath == "" {
		c.Error(errors.NewNotFoundError("log_path is empty"))
		return
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			c.Error(errors.NewNotFoundError("log file not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to read log file").WithDetails(err.Error()))
		return
	}

	c.Data(http.StatusOK, "text/plain; charset=utf-8", logBytes)
}

// UpdateAgentAnalysis 更新 Name / TemplateID / Command / WorkspaceDir。
func (h *AgentAnalysisHandler) UpdateAgentAnalysis(c *gin.Context) {
	var req agentAnalysisUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if _, ok := h.getOwnedAgentAnalysis(c, req.ID); !ok {
		return
	}

	input := interfaces.AgentAnalysisUpdateInput{
		Name:         req.Name,
		Command:      req.Command,
		WorkspaceDir: req.WorkspaceDir,
	}
	if req.TemplateID != nil {
		templateID, err := parseTemplateID(*req.TemplateID)
		if err != nil {
			c.Error(errors.NewValidationError(err.Error()))
			return
		}
		input.TemplateID = &templateID
	}

	item, err := h.service.UpdateAgentAnalysis(c.Request.Context(), req.ID, input)
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}
	c.JSON(http.StatusOK, item)
}

// RerunAgentAnalysis 用当前模板与命令重新创建一个容器实例。
// 只有已结束（done / failed / timeout / canceled）的任务可以重跑。
func (h *AgentAnalysisHandler) RerunAgentAnalysis(c *gin.Context) {
	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if _, ok := h.getOwnedAgentAnalysis(c, req.ID); !ok {
		return
	}

	item, err := h.service.RerunAgentAnalysis(c.Request.Context(), req.ID)
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}
	c.JSON(http.StatusOK, item)
}

// StopAgentAnalysis 停止正在运行的容器，并把任务标记为 canceled。
func (h *AgentAnalysisHandler) StopAgentAnalysis(c *gin.Context) {
	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if _, ok := h.getOwnedAgentAnalysis(c, req.ID); !ok {
		return
	}

	item, err := h.service.StopAgentAnalysis(c.Request.Context(), req.ID)
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}
	c.JSON(http.StatusOK, item)
}

// DeleteAgentAnalysis 删除任务并提交容器删除请求。
func (h *AgentAnalysisHandler) DeleteAgentAnalysis(c *gin.Context) {
	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if _, ok := h.getOwnedAgentAnalysis(c, req.ID); !ok {
		return
	}

	if err := h.service.DeleteAgentAnalysis(c.Request.Context(), req.ID); err != nil {
		c.Error(errors.NewInternalServerError("failed to delete agent analysis").WithDetails(err.Error()))
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "agent analysis deleted successfully"})
}

// AgentAnalysisTerminal 通过 WebSocket 把 xterm.js 连接到运行中的容器。
//
// 等价于 `kubectl exec -it <pod> -- /bin/bash` / `docker exec -it <container> /bin/bash`：
// 只在容器 running 时可连接；任务结束后连接会被拒绝。
func (h *AgentAnalysisHandler) AgentAnalysisTerminal(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(c.Query("id")), 10, 64)
	if err != nil || id <= 0 {
		c.Error(errors.NewValidationError("invalid agent analysis id"))
		return
	}

	item, err := h.service.GetAgentAnalysis(c.Request.Context(), id)
	if err != nil {
		c.Error(errors.NewNotFoundError("agent analysis not found").WithDetails(err.Error()))
		return
	}
	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil || project == nil || item.ProjectID != project.ID {
		c.Error(errors.NewNotFoundError("agent analysis not found in the active project"))
		return
	}
	if item.ContainerInstanceID <= 0 {
		c.Error(errors.NewConflictError("agent analysis has no container instance yet"))
		return
	}

	inst, err := h.containerRepo.GetContainerInstanceByID(c.Request.Context(), item.ContainerInstanceID)
	if err != nil {
		c.Error(errors.NewNotFoundError("container instance not found").WithDetails(err.Error()))
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

	// 容器输出 -> 浏览器（唯一的 WebSocket 写协程）。
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, readErr := session.Read(buf)
			if n > 0 {
				if writeErr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); writeErr != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()

	// 浏览器输入 -> 容器；文本消息支持 resize 控制帧。
	for {
		messageType, data, readErr := conn.ReadMessage()
		if readErr != nil {
			return
		}
		if messageType == websocket.TextMessage {
			var control struct {
				Type string `json:"type"`
				Rows uint16 `json:"rows"`
				Cols uint16 `json:"cols"`
			}
			if json.Unmarshal(data, &control) == nil && control.Type == "resize" {
				_ = session.Resize(control.Rows, control.Cols)
				continue
			}
		}
		if _, writeErr := session.Write(data); writeErr != nil {
			return
		}
	}
}

func (h *AgentAnalysisHandler) getOwnedAgentAnalysis(c *gin.Context, id int64) (*types.AgentAnalysis, bool) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return nil, false
	}
	item, err := h.service.GetAgentAnalysis(c.Request.Context(), id)
	if err != nil {
		c.Error(errors.NewNotFoundError("agent analysis not found").WithDetails(err.Error()))
		return nil, false
	}
	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil || project == nil || item.ProjectID != project.ID {
		c.Error(errors.NewNotFoundError("agent analysis not found in the active project"))
		return nil, false
	}
	return item, true
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func parseTemplateID(raw string) (int64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, errors.NewValidationError("template_id is required")
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.NewValidationError("template_id must be a positive integer")
	}
	return id, nil
}
