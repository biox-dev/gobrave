package handler

import (
	stderrs "errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/htmlreport"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GetProjectReportHTML godoc
// @Summary      导出报告为 HTML
// @Description  将当前用户报告聚合为独立 HTML 文档；inline_images=true 时图片以 base64 内嵌（自包含），false 时保留原始 URL
// @Tags         项目
// @Produce      html
// @Param        report_id      query     int64  true   "报告ID"
// @Param        inline_images  query     bool   false  "是否内嵌图片（默认 true）"
// @Success      200            {string}  string "HTML 文档"
// @Failure      400            {object}  errors.AppError "参数错误"
// @Failure      401            {object}  errors.AppError "未认证"
// @Failure      404            {object}  errors.AppError "报告不存在"
// @Failure      500            {object}  errors.AppError "服务器错误"
// @Security     Bearer
// @Router       /project/project-report-html [get]
func (h *ProjectHandler) GetProjectReportHTML(c *gin.Context) {
	ctx := c.Request.Context()

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	reportID, err := strconv.ParseInt(strings.TrimSpace(c.Query("report_id")), 10, 64)
	if err != nil || reportID <= 0 {
		c.Error(errors.NewValidationError("invalid report_id"))
		return
	}

	report, err := h.projectService.GetProjectReportDetailByID(ctx, userID, reportID)
	if err != nil {
		c.Error(errProjectReport(err))
		return
	}

	content, err := h.projectService.GetProjectReportContent(ctx, reportID)
	if err != nil {
		c.Error(errProjectReport(err))
		return
	}

	document, err := h.htmlRenderer.Render(htmlreport.Options{
		Title:        report.Title,
		Markdown:     content,
		InlineImages: queryBoolDefault(c, "inline_images", true),
		BaseDir:      h.storageBaseDir(),
	})
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to render report html").WithDetails(err.Error()))
		return
	}

	c.Data(http.StatusOK, "text/html; charset=utf-8", document)
}

// storageBaseDir is the nil-safe accessor for storage.base_dir.
func (h *ProjectHandler) storageBaseDir() string {
	if h.config == nil || h.config.Storage == nil {
		return ""
	}
	return h.config.Storage.BaseDir
}

// errProjectReport maps a report lookup error to the appropriate AppError.
func errProjectReport(err error) error {
	if stderrs.Is(err, gorm.ErrRecordNotFound) {
		return errors.NewNotFoundError("project report not found")
	}
	return errors.NewInternalServerError("failed to get project report").WithDetails(err.Error())
}

// queryBoolDefault parses an optional boolean query param.
func queryBoolDefault(c *gin.Context, key string, fallback bool) bool {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return fallback
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return value
}
