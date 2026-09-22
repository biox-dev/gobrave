package handler

import (
	stderrs "errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/exportcodec"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 「生成导出文件并提交」接口：
//
//	POST /workflow/save-script-files    → SaveScriptFiles
//	POST /workflow/save-workflow-files  → SaveWorkflowFiles
//
// 背景：保存组件（SaveScript / SaveWorkflow）会落库，并把 script.json / workflow.json
// 重新生成落盘（脚本目录的 io_schema.json / 脚本主文件也随保存写入），但不提交 git。
// 「把本地目录改动提交为一个 commit」被提取成本文件的两个接口，
// 由调用方按需触发（前端在 git_state 显示「本地有变化」时才展示按钮），
// 并可自带 commit_message（为空时使用默认文案）。
//
// 与发布/安装的关系：
//   - PublishScript / PublishWorkflow 仍会在 push 前自行生成导出文件并提交
//     （发布必须产出完整版本产物），因此这里只是把「保存时隐式提交」变成「显式提交」；
//   - 生产出的 script.json / workflow.json 就是 InstallScript / InstallWorkflow 读回的文件，
//     两个接口都复用同一个写侧 Codec（exportCodecForWrite），格式与发布路径完全一致。
//
// 目录布局与提交身份均由 Codec 决定/注入（见 internal/exportcodec），本层只负责
// 解析入参、定位脚本/工作流目录并转错误码，不做任何 switch version。

// saveScriptFilesRequest 是 SaveScriptFiles 的入参。
type saveScriptFilesRequest struct {
	// ScriptID 是 script 表主键（int64），与其它接口一致按字符串编码（json:"...,string"）。
	ScriptID int64ID `json:"script_id,string"`
	// CommitMessage 可选：本次提交使用的 message，为空时使用默认文案（save script <scriptID>）。
	CommitMessage string `json:"commit_message"`
}

// SaveScriptFiles godoc
// @Summary      生成脚本导出文件并提交
// @Description  按主键重新生成 script.json 落盘到脚本目录，并把脚本目录的本地改动提交为一个 git commit（本地目录不存在则初始化）；commit_message 可选，为空时使用默认文案 save script &lt;scriptID&gt;
// @Tags         工作流
// @Accept       json
// @Produce      json
// @Param        request  body      handler.saveScriptFilesRequest  true  "请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /workflow/save-script-files [post]
func (h *WorkflowHandler) SaveScriptFiles(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req saveScriptFilesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	scriptID := int64(req.ScriptID)
	if scriptID <= 0 {
		c.Error(errors.NewValidationError("script_id must be a valid integer"))
		return
	}

	baseDir := h.storageBaseDir()
	if baseDir == "" {
		c.Error(errors.NewInternalServerError("storage base dir is not configured"))
		return
	}

	script, err := h.workflowService.GetScriptByID(c.Request.Context(), scriptID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("script not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get script").WithDetails(err.Error()))
		return
	}

	// 脚本目录用 project.project_id（字符串）推导，不能用 script.ProjectID（int64 外键）。
	project, err := h.projectService.GetProjectByID(c.Request.Context(), script.ProjectID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get project").WithDetails(err.Error()))
		return
	}

	commitMessage := strings.TrimSpace(req.CommitMessage)
	if commitMessage == "" {
		commitMessage = fmt.Sprintf("save script %s", script.ScriptID)
	}

	codec, err := h.exportCodecForWrite()
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to resolve export codec").WithDetails(err.Error()))
		return
	}

	scriptDir := utils.GetScriptFileDir(baseDir, project.ProjectID, script.ScriptID)
	if _, err := codec.WriteCommitScriptFiles(c.Request.Context(), exportcodec.ScriptWriteRequest{
		ScriptPK:      script.ID,
		ScriptDir:     scriptDir,
		CommitMessage: commitMessage,
	}); err != nil {
		c.Error(errors.NewInternalServerError("failed to persist script files").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":        "script files generated and committed successfully",
		"script_id":      script.ScriptID,
		"script_path":    scriptDir,
		"commit_message": commitMessage,
	})
}

// saveWorkflowFilesRequest 是 SaveWorkflowFiles 的入参。
type saveWorkflowFilesRequest struct {
	// WorkflowID 是 workflow 表主键（int64），与其它接口一致按字符串编码（json:"...,string"）。
	WorkflowID int64ID `json:"workflow_id,string"`
	// CommitMessage 可选：本次提交使用的 message，为空时使用默认文案（save workflow <workflowID>）。
	CommitMessage string `json:"commit_message"`
}

// SaveWorkflowFiles godoc
// @Summary      生成工作流导出文件并提交
// @Description  按主键重新生成 workflow.json（含引用的脚本目录快照）落盘到 workflow 目录，并把该目录的本地改动提交为一个 git commit（本地目录不存在则初始化）；commit_message 可选，为空时使用默认文案 save workflow &lt;workflowID&gt;
// @Tags         工作流
// @Accept       json
// @Produce      json
// @Param        request  body      handler.saveWorkflowFilesRequest  true  "请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /workflow/save-workflow-files [post]
func (h *WorkflowHandler) SaveWorkflowFiles(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req saveWorkflowFilesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	workflowID := int64(req.WorkflowID)
	if workflowID <= 0 {
		c.Error(errors.NewValidationError("workflow_id must be a valid integer"))
		return
	}

	baseDir := h.storageBaseDir()
	if baseDir == "" {
		c.Error(errors.NewInternalServerError("storage base dir is not configured"))
		return
	}

	workflow, err := h.workflowService.GetWorkflowByID(c.Request.Context(), workflowID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("workflow not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get workflow").WithDetails(err.Error()))
		return
	}

	project, err := h.projectService.GetProjectByID(c.Request.Context(), workflow.ProjectID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get project").WithDetails(err.Error()))
		return
	}

	commitMessage := strings.TrimSpace(req.CommitMessage)
	if commitMessage == "" {
		commitMessage = fmt.Sprintf("save workflow %s", workflow.WorkflowID)
	}

	codec, err := h.exportCodecForWrite()
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to resolve export codec").WithDetails(err.Error()))
		return
	}

	workflowDir := utils.GetWorkflowFileDir(baseDir, project.ProjectID, workflow.WorkflowID)
	if _, err := codec.WriteCommitWorkflowFiles(c.Request.Context(), exportcodec.WorkflowWriteRequest{
		WorkflowPK:    workflow.ID,
		ProjectID:     project.ProjectID,
		BaseDir:       baseDir,
		WorkflowDir:   workflowDir,
		CommitMessage: commitMessage,
	}); err != nil {
		c.Error(errors.NewInternalServerError("failed to persist workflow files").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":        "workflow files generated and committed successfully",
		"workflow_id":    workflow.WorkflowID,
		"workflow_path":  workflowDir,
		"commit_message": commitMessage,
	})
}
