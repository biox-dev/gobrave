package handler

import (
	"net/http"
	"strings"

	"github.com/biox-dev/gobrave/internal/analysisform"
	"github.com/biox-dev/gobrave/internal/dag/prepare"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/gin-gonic/gin"
)

// createCommandNodeRequest 是「手动创建命令型分析节点」的入参。
//
// 与内置工具 create_agent_analysis 的入参保持一致：节点没有脚本，容器模板与 run.sh
// 正文都由节点自身承载。run=true 时创建后立即提交执行。
type createCommandNodeRequest struct {
	Name                string `json:"name" binding:"required"`
	ContainerTemplateID string `json:"container_template_id" binding:"required"`
	Command             string `json:"command" binding:"required"`
	Run                 bool   `json:"run"`
}

// CreateCommandAnalysisNode 手动创建一个命令型 AnalysisNode（无脚本）。
//
// 它等价于 Agent 工具 create_agent_analysis 的「建节点 + 写 run.sh」，只是由用户在
// 分析节点列表里主动触发；节点创建后与普通分析节点共用运行 / 停止 / 日志链路。
func (h *AnalysisHandler) CreateCommandAnalysisNode(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}
	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil || project == nil {
		c.Error(errors.NewNotFoundError("active project not found"))
		return
	}

	var req createCommandNodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	templateID := parsePositiveInt64(req.ContainerTemplateID)
	if templateID <= 0 {
		c.Error(errors.NewValidationError("container_template_id must be a positive integer"))
		return
	}

	node, err := analysisform.NewAnalysisNodeFromCommand(analysisform.CommandNodeSpec{
		ProjectID:           project.ID,
		ContainerTemplateID: templateID,
		Command:             strings.TrimSpace(req.Command),
		NodeName:            strings.TrimSpace(req.Name),
		BaseDir:             standaloneStorageBaseDir(h.config),
	})
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	if err := h.analysisRepo.CreateAnalysisNodes(c.Request.Context(), []*types.AnalysisNode{node}); err != nil {
		c.Error(errors.NewInternalServerError("failed to create command analysis node").WithDetails(err.Error()))
		return
	}
	if h.preparer != nil {
		if err := h.preparer.Prepare(prepare.WithSkipCleanOutput(c.Request.Context()), node); err != nil {
			// 节点已落库但运行期文件生成失败：回滚记录，避免留下跑不通的空节点。
			_ = h.analysisRepo.DeleteAnalysisNodeByID(c.Request.Context(), node.ID)
			c.Error(errors.NewInternalServerError("failed to prepare command analysis node").WithDetails(err.Error()))
			return
		}
	}
	// 命令不再落库：Prepare 只建目录 / params.json / env.sh，run.sh 由这里直接写入。
	if err := prepare.WriteCommandRunScript(node.CommandPath, req.Command); err != nil {
		_ = h.analysisRepo.DeleteAnalysisNodeByID(c.Request.Context(), node.ID)
		c.Error(errors.NewInternalServerError("failed to write command analysis node run.sh").WithDetails(err.Error()))
		return
	}

	if req.Run && h.nodeOrchestrator != nil {
		if err := h.nodeOrchestrator.StartAsync(c.Request.Context(), node.ID); err != nil {
			c.Error(errors.NewInternalServerError("analysis node created but failed to submit").WithDetails(err.Error()))
			return
		}
	}

	c.JSON(http.StatusOK, node)
}
