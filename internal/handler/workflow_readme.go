package handler

import (
	stderrs "errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// readmeResponse 是 README.md 的读取 / 保存响应。
// content 为空字符串表示该组件还没有 README.md（不视为错误，便于前端直接编辑新建）。
type readmeResponse struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// saveReadmeRequest 是保存 README.md 的入参。
type saveReadmeRequest struct {
	Content string `json:"content"`
	// CommitMessage 可选：覆盖 README.md 后 git 提交使用的 message（为空时使用默认文案）。
	CommitMessage string `json:"commit_message"`
}

// workflowWithProject 按主键读取 workflow，并解析其所属项目。
// 工作流目录用的是 project.project_id（字符串）而非 workflow.ProjectID（int64 外键），
// 因此读取工作流目录文件（README.md）前都需要这一步。
func (h *WorkflowHandler) workflowWithProject(c *gin.Context, workflowID int64) (*types.Workflow, *types.Project, error) {
	workflow, err := h.workflowService.GetWorkflowByID(c.Request.Context(), workflowID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errors.NewNotFoundError("workflow not found")
		}
		return nil, nil, errors.NewInternalServerError("failed to get workflow").WithDetails(err.Error())
	}

	project, err := h.projectService.GetProjectByID(c.Request.Context(), workflow.ProjectID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errors.NewNotFoundError("project not found")
		}
		return nil, nil, errors.NewInternalServerError("failed to get project").WithDetails(err.Error())
	}
	return workflow, project, nil
}

// parseWorkflowIDParam 解析路径参数 workflowId 为 int64 主键。
func parseWorkflowIDParam(c *gin.Context) (int64, error) {
	raw := strings.TrimSpace(c.Param("workflowId"))
	if raw == "" {
		return 0, fmt.Errorf("workflowId is required")
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("workflowId must be a valid integer")
	}
	return id, nil
}

// commitDirReadme 提交 dir 目录（脚本 / 工作流导出目录）的 git 改动。
// README.md 与 script.json / workflow.json 共用同一个仓库，因此保存后立即提交，
// 保证目录不长期处于 dirty 状态，发布（publish）时能一并推送。
func (h *WorkflowHandler) commitDirReadme(dir, commitMessage string) error {
	gitUser, gitEmail := config.ResolveGitIdentity(h.cfg)
	return utils.CommitDirChanges(dir, commitMessage, utils.GitIdentity{Name: gitUser, Email: gitEmail})
}

// GetScriptReadme godoc
// @Summary      获取脚本 README
// @Description  读取脚本目录（GetScriptFileDir）下的 README.md；文件不存在时返回空 content
// @Tags         工作流
// @Produce      json
// @Param        scriptId  path      string                 true  "脚本主键 ID"
// @Success      200       {object}  handler.readmeResponse
// @Failure      400       {object}  errors.AppError
// @Failure      401       {object}  errors.AppError
// @Failure      404       {object}  errors.AppError
// @Failure      500       {object}  errors.AppError
// @Security     Bearer
// @Router       /script/{scriptId}/readme [get]
func (h *WorkflowHandler) GetScriptReadme(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	scriptID, err := parseScriptIDParam(c)
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	script, project, err := h.scriptWithProject(c, scriptID)
	if err != nil {
		c.Error(err)
		return
	}

	baseDir := h.storageBaseDir()
	content, err := utils.ReadScriptReadme(baseDir, project.ProjectID, script.ScriptID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to read script readme").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, readmeResponse{
		Path:    utils.ScriptReadmePath(baseDir, project.ProjectID, script.ScriptID),
		Content: string(content),
	})
}

// SaveScriptReadme godoc
// @Summary      保存脚本 README
// @Description  覆盖写入脚本目录（GetScriptFileDir）下的 README.md，并提交该目录的 git 改动
// @Tags         工作流
// @Accept       json
// @Produce      json
// @Param        scriptId  path      string                     true  "脚本主键 ID"
// @Param        request   body      handler.saveReadmeRequest  true  "请求参数"
// @Success      200       {object}  handler.readmeResponse
// @Failure      400       {object}  errors.AppError
// @Failure      401       {object}  errors.AppError
// @Failure      404       {object}  errors.AppError
// @Failure      500       {object}  errors.AppError
// @Security     Bearer
// @Router       /script/{scriptId}/readme [post]
func (h *WorkflowHandler) SaveScriptReadme(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	scriptID, err := parseScriptIDParam(c)
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	var req saveReadmeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	script, project, err := h.scriptWithProject(c, scriptID)
	if err != nil {
		c.Error(err)
		return
	}

	baseDir := h.storageBaseDir()
	if err := utils.WriteScriptReadme(baseDir, project.ProjectID, script.ScriptID, req.Content); err != nil {
		c.Error(errors.NewInternalServerError("failed to write script readme").WithDetails(err.Error()))
		return
	}

	scriptDir := utils.GetScriptFileDir(baseDir, project.ProjectID, script.ScriptID)
	if err := h.commitDirReadme(scriptDir, readmeCommitMessage(req.CommitMessage, "script", script.ScriptID)); err != nil {
		c.Error(errors.NewInternalServerError("failed to commit script readme").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, readmeResponse{
		Path:    filepath.Join(scriptDir, utils.ReadmeFileName),
		Content: req.Content,
	})
}

// GetWorkflowReadme godoc
// @Summary      获取工作流 README
// @Description  读取工作流目录（GetWorkflowFileDir）下的 README.md；文件不存在时返回空 content
// @Tags         工作流
// @Produce      json
// @Param        workflowId  path      string                 true  "工作流主键 ID"
// @Success      200         {object}  handler.readmeResponse
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      404         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /workflow/{workflowId}/readme [get]
func (h *WorkflowHandler) GetWorkflowReadme(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	workflowID, err := parseWorkflowIDParam(c)
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	workflow, project, err := h.workflowWithProject(c, workflowID)
	if err != nil {
		c.Error(err)
		return
	}

	baseDir := h.storageBaseDir()
	content, err := utils.ReadWorkflowReadme(baseDir, project.ProjectID, workflow.WorkflowID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to read workflow readme").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, readmeResponse{
		Path:    utils.WorkflowReadmePath(baseDir, project.ProjectID, workflow.WorkflowID),
		Content: string(content),
	})
}

// SaveWorkflowReadme godoc
// @Summary      保存工作流 README
// @Description  覆盖写入工作流目录（GetWorkflowFileDir）下的 README.md，并提交该目录的 git 改动
// @Tags         工作流
// @Accept       json
// @Produce      json
// @Param        workflowId  path      string                     true  "工作流主键 ID"
// @Param        request     body      handler.saveReadmeRequest  true  "请求参数"
// @Success      200         {object}  handler.readmeResponse
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      404         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /workflow/{workflowId}/readme [post]
func (h *WorkflowHandler) SaveWorkflowReadme(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	workflowID, err := parseWorkflowIDParam(c)
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	var req saveReadmeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	workflow, project, err := h.workflowWithProject(c, workflowID)
	if err != nil {
		c.Error(err)
		return
	}

	baseDir := h.storageBaseDir()
	if err := utils.WriteWorkflowReadme(baseDir, project.ProjectID, workflow.WorkflowID, req.Content); err != nil {
		c.Error(errors.NewInternalServerError("failed to write workflow readme").WithDetails(err.Error()))
		return
	}

	workflowDir := utils.GetWorkflowFileDir(baseDir, project.ProjectID, workflow.WorkflowID)
	if err := h.commitDirReadme(workflowDir, readmeCommitMessage(req.CommitMessage, "workflow", workflow.WorkflowID)); err != nil {
		c.Error(errors.NewInternalServerError("failed to commit workflow readme").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, readmeResponse{
		Path:    filepath.Join(workflowDir, utils.ReadmeFileName),
		Content: req.Content,
	})
}

// readmeCommitMessage 返回保存 README 的默认 commit message。
func readmeCommitMessage(message, kind, id string) string {
	if msg := strings.TrimSpace(message); msg != "" {
		return msg
	}
	return fmt.Sprintf("update %s readme %s", kind, id)
}
