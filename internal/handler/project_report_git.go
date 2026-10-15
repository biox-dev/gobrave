package handler

import (
	stderrs "errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 报告工作目录（<baseDir>/data/<projectId>/report/<reportId>）的 git 接口。
//
// 与组件目录（见 git.go 的 GitHandler）刻意分开：报告没有「store / 发布」这一侧，
// 只有本地工作区仓库，因此状态、错误码与响应结构都比组件目录更简单；
// 但底层复用同一套 internal/utils 原语（ReadGitDiff / CommitAll / RevertWorktreeFile），
// 前端的 diff 视图与「撤回单文件」交互也和组件目录一致。
//
// 目录布局与 handler.GitHandler 一致：报告目录 = utils.GetProjectReportDir(baseDir, projectID, reportID)，
// 其中 reportID 是 ProjectReport 的数值主键（字符串形式对外）。

// projectReportGitRequest 是报告 git 接口的公共查询参数。
type projectReportGitRequest struct {
	ReportID int64 `form:"report_id" binding:"required"`
}

// projectReportGitCommitRequest 是「提交报告目录改动」的请求体。
type projectReportGitCommitRequest struct {
	ReportID int64  `json:"report_id,string" binding:"required"`
	Message  string `json:"message"`
}

// projectReportGitRevertFileRequest 是「撤回报告目录单个文件改动」的请求体。
type projectReportGitRevertFileRequest struct {
	ReportID int64  `json:"report_id,string" binding:"required"`
	Path     string `json:"path"`
}

// GetProjectReportGitDiff godoc
// @Summary      获取报告工作目录的本地变化
// @Description  按报告ID定位报告工作目录（本地 git 仓库），返回工作区相对 HEAD 的未提交改动（含未跟踪文件）；非 git 仓库时 initialized 为 false 且无变化
// @Tags         项目
// @Produce      json
// @Param        report_id  query     int64  true  "报告ID"
// @Success      200        {object}  types.ProjectReportGitDiff
// @Failure      400        {object}  errors.AppError
// @Failure      401        {object}  errors.AppError
// @Failure      404        {object}  errors.AppError
// @Failure      500        {object}  errors.AppError
// @Security     Bearer
// @Router       /project/project-report-git-diff [get]
func (h *ProjectHandler) GetProjectReportGitDiff(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req projectReportGitRequest
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	diff, err := h.projectService.GetProjectReportGitDiff(ctx, userID, req.ReportID)
	if err != nil {
		c.Error(projectReportGitError(err, "failed to read project report git diff"))
		return
	}

	c.JSON(http.StatusOK, diff)
}

// CommitProjectReport godoc
// @Summary      提交报告工作目录的改动
// @Description  把报告工作目录下的所有改动提交为一个 commit（等同 git add -A + commit）；目录尚不是仓库时先初始化，无改动时不产生空提交
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      handler.projectReportGitCommitRequest  true  "报告ID与可选 commit message"
// @Success      200      {object}  types.ProjectReportCommitResult
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /project/commit-project-report [post]
func (h *ProjectHandler) CommitProjectReport(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req projectReportGitCommitRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	result, err := h.projectService.CommitProjectReport(ctx, userID, req.ReportID, req.Message)
	if err != nil {
		c.Error(projectReportGitError(err, "failed to commit project report"))
		return
	}

	c.JSON(http.StatusOK, result)
}

// RevertProjectReportFile godoc
// @Summary      撤回报告目录单个文件的未提交改动
// @Description  按报告ID定位报告工作目录，把指定文件恢复为本地 HEAD 版本（等同 git checkout HEAD -- <path>）；文件为工作区新增时删除该文件。仅影响该文件，不产生提交
// @Tags         项目
// @Accept       json
// @Produce      json
// @Param        request  body      handler.projectReportGitRevertFileRequest  true  "报告ID与相对报告目录的文件路径"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      409      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /project/revert-project-report-file [post]
func (h *ProjectHandler) RevertProjectReportFile(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req projectReportGitRevertFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	relPath := strings.TrimSpace(req.Path)
	if relPath == "" {
		c.Error(errors.NewValidationError("file path is required"))
		return
	}

	switch err := h.projectService.RevertProjectReportFile(ctx, userID, req.ReportID, relPath); {
	case err == nil:
		c.JSON(http.StatusOK, gin.H{
			"report_id": strconv.FormatInt(req.ReportID, 10),
			"path":      relPath,
			"reverted":  true,
		})
	case stderrs.Is(err, utils.ErrGitPathInvalid):
		c.Error(errors.NewValidationError("invalid file path").WithDetails(err.Error()))
	case stderrs.Is(err, utils.ErrGitRepoNotInitialized):
		c.Error(errors.NewConflictError("report directory is not a git repository"))
	case stderrs.Is(err, utils.ErrGitFileAbsent):
		c.Error(errors.NewNotFoundError("no local change to revert for this file"))
	default:
		c.Error(projectReportGitError(err, "failed to revert project report file"))
	}
}

// projectReportGitError 把服务层错误映射为统一的接口错误（报告不存在 -> 404）。
func projectReportGitError(err error, fallback string) error {
	if stderrs.Is(err, gorm.ErrRecordNotFound) {
		return errors.NewNotFoundError("project report not found")
	}
	return errors.NewInternalServerError(fallback).WithDetails(err.Error())
}
