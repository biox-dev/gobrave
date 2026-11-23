package handler

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
)

// sessionResourceExportItem 是一条成功导出的资源。
type sessionResourceExportItem struct {
	ResourceID   int64  `json:"resource_id,string"`
	ResourceType string `json:"resource_type"`
	Name         string `json:"name"`
	// Path 是导出后的绝对路径（分析节点为目录，报告为 .md 文件）。
	Path string `json:"path"`
}

// sessionResourceExportFailure 是一条未能导出的资源及原因。
//
// 单个资源失败不影响其它资源：接口仍返回 200，前端据此提示哪些资源没导出。
type sessionResourceExportFailure struct {
	ResourceID   int64  `json:"resource_id,string"`
	ResourceType string `json:"resource_type"`
	Reason       string `json:"reason"`
}

// sessionResourceExportResult 是「导出会话结果」的返回。
type sessionResourceExportResult struct {
	// ReportDir 是本次导出使用的目录：<sessionDir>/report。
	ReportDir string `json:"report_dir"`
	// Exported 是成功导出的资源；Failed 是缺失 / 拷贝失败的资源。
	Exported []sessionResourceExportItem    `json:"exported"`
	Failed   []sessionResourceExportFailure `json:"failed"`
}

// ExportSessionResources godoc
// @Summary      导出会话结果到会话目录
// @Description  在该会话的 Agent 工作目录下创建 report 目录，并把会话关联的分析节点输出（按 NodeName 建子目录）与项目报告（重命名为 <标题>.md）拷贝进去
// @Tags         Agent
// @Produce      json
// @Param        session_id  query     string  true  "会话 ID"
// @Success      200  {object}  map[string]interface{}
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      403  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Failure      500  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/session/resources/export [post]
func (h *SessionResourceHandler) ExportSessionResources(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	sessionID, err := parseAgentID(c.Query("session_id"))
	if err != nil || sessionID <= 0 {
		c.Error(errors.NewValidationError("invalid session_id"))
		return
	}

	ctx := c.Request.Context()
	session, err := h.sessions.Get(ctx, sessionID)
	if err != nil {
		handleAgentError(c, err, "failed to get session")
		return
	}
	// 可见性校验：会话按项目共享，项目成员都能导出。
	if err := ensureProjectAccessible(ctx, h.projects, session.ProjectID, userID); err != nil {
		c.Error(err)
		return
	}

	result, err := h.exportSessionResources(ctx, session)
	if err != nil {
		c.Error(err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": result})
}

// DownloadSessionResources godoc
// @Summary      下载会话结果压缩包
// @Description  先按 /agent/session/resources/export 的规则汇总分析节点输出与项目报告，再把 report 目录打包成 zip 下载，压缩包名称取自会话标题
// @Tags         Agent
// @Produce      application/zip
// @Param        session_id  query     string  true  "会话 ID"
// @Success      200  {file}    binary  "会话结果压缩包"
// @Failure      400  {object}  errors.AppError
// @Failure      401  {object}  errors.AppError
// @Failure      403  {object}  errors.AppError
// @Failure      404  {object}  errors.AppError
// @Failure      500  {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/session/resources/export/download [get]
func (h *SessionResourceHandler) DownloadSessionResources(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	sessionID, err := parseAgentID(c.Query("session_id"))
	if err != nil || sessionID <= 0 {
		c.Error(errors.NewValidationError("invalid session_id"))
		return
	}

	ctx := c.Request.Context()
	session, err := h.sessions.Get(ctx, sessionID)
	if err != nil {
		handleAgentError(c, err, "failed to get session")
		return
	}
	if err := ensureProjectAccessible(ctx, h.projects, session.ProjectID, userID); err != nil {
		c.Error(err)
		return
	}

	// 下载前先刷新一次导出，保证压缩包内容是最新的分析节点输出 / 报告。
	result, err := h.exportSessionResources(ctx, session)
	if err != nil {
		c.Error(err)
		return
	}

	zipPath, err := zipDirectory(result.ReportDir)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to package report directory: " + err.Error()))
		return
	}
	defer os.Remove(zipPath)

	// 压缩包名称取自会话标题；非 ASCII 标题由 gin 的 FileAttachment 以 RFC 5987 形式编码。
	c.FileAttachment(zipPath, sessionZipFilename(session))
}

// exportSessionResources 把会话关联的分析节点输出与项目报告拷贝到 <sessionDir>/report。
//
// 目标目录布局：
//
//	<sessionDir>/report/<NodeName>/...   分析节点输出目录内容
//	<sessionDir>/report/<Title>.md       项目报告正文（output.md）
//
// 单个资源失败（资源已删除、输出目录/正文文件不存在等）只记入 Failed，
// 不中断其余资源的导出。
func (h *SessionResourceHandler) exportSessionResources(ctx context.Context, session agenttypes.Session) (*sessionResourceExportResult, error) {
	if session.ProjectID <= 0 {
		return nil, errors.NewValidationError("session has no project")
	}
	baseDir := h.storageBaseDir()
	if baseDir == "" {
		return nil, errors.NewInternalServerError("storage base dir is not configured")
	}

	projectDir := utils.GetProjectDir(baseDir, session.ProjectID)
	sessionDir := utils.GetAgentSessionDir(projectDir, session.ID)
	reportDir := filepath.Join(sessionDir, "report")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		return nil, errors.NewInternalServerError("failed to create report directory: " + err.Error())
	}

	result := &sessionResourceExportResult{
		ReportDir: reportDir,
		Exported:  []sessionResourceExportItem{},
		Failed:    []sessionResourceExportFailure{},
	}
	if h.resources == nil {
		return result, nil
	}

	rows, err := h.resources.ListBySession(ctx, session.ID)
	if err != nil {
		return nil, errors.NewInternalServerError("failed to list session resources: " + err.Error())
	}

	// usedNames 保证同一目录下不出现重名的子目录 / 文件（不同节点同名、报告同标题）。
	usedNames := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		switch row.ResourceType {
		case agenttypes.SessionResourceTypeAnalysisNode:
			h.exportAnalysisNodeResource(ctx, baseDir, reportDir, row, usedNames, result)
		case agenttypes.SessionResourceTypeProjectReport:
			h.exportProjectReportResource(ctx, baseDir, reportDir, row, usedNames, result)
		}
	}
	return result, nil
}

// exportAnalysisNodeResource 把分析节点的输出目录内容拷贝到 report/<NodeName>。
func (h *SessionResourceHandler) exportAnalysisNodeResource(
	ctx context.Context,
	baseDir string,
	reportDir string,
	row agenttypes.SessionResource,
	usedNames map[string]struct{},
	result *sessionResourceExportResult,
) {
	if h.analysisRepo == nil {
		result.fail(row, "analysis repository is not available")
		return
	}
	node, err := h.analysisRepo.GetAnalysisNodeByID(ctx, row.ResourceID)
	if err != nil || node == nil {
		result.fail(row, "analysis node not found")
		return
	}

	srcDir := strings.TrimSpace(node.OutputDirectory())
	if srcDir == "" {
		result.fail(row, "analysis node output directory is empty")
		return
	}
	if info, statErr := os.Stat(srcDir); statErr != nil || !info.IsDir() {
		result.fail(row, "analysis node output directory does not exist")
		return
	}

	dirName := uniqueExportName(usedNames, sanitizeExportName(node.NodeName, fmt.Sprintf("node_%d", row.ResourceID)), row.ResourceID)
	dstDir := filepath.Join(reportDir, dirName)
	// 名称已清洗掉路径分隔符，这里再校验一次，防止任何形式的目录逃逸。
	if _, err := utils.SafePathUnderBase(reportDir, dstDir); err != nil {
		result.fail(row, "invalid target path: "+err.Error())
		return
	}
	if err := utils.CopyDir(srcDir, dstDir); err != nil {
		result.fail(row, "failed to copy output directory: "+err.Error())
		return
	}
	result.Exported = append(result.Exported, sessionResourceExportItem{
		ResourceID:   row.ResourceID,
		ResourceType: string(row.ResourceType),
		Name:         node.NodeName,
		Path:         dstDir,
	})
}

// exportProjectReportResource 把项目报告正文（output.md）拷贝到 report/<Title>.md。
func (h *SessionResourceHandler) exportProjectReportResource(
	ctx context.Context,
	baseDir string,
	reportDir string,
	row agenttypes.SessionResource,
	usedNames map[string]struct{},
	result *sessionResourceExportResult,
) {
	if h.projects == nil {
		result.fail(row, "project service is not available")
		return
	}
	report, err := h.projects.GetProjectReportByID(ctx, row.ResourceID)
	if err != nil || report == nil {
		result.fail(row, "project report not found")
		return
	}

	srcFile := report.OutputFile(baseDir)
	if strings.TrimSpace(srcFile) == "" {
		result.fail(row, "project report output file is empty")
		return
	}
	if info, statErr := os.Stat(srcFile); statErr != nil || info.IsDir() {
		result.fail(row, "project report output file does not exist")
		return
	}

	fileName := uniqueExportName(usedNames, reportMarkdownFilename(report.Title, row.ResourceID), row.ResourceID)
	dstFile := filepath.Join(reportDir, fileName)
	if _, err := utils.SafePathUnderBase(reportDir, dstFile); err != nil {
		result.fail(row, "invalid target path: "+err.Error())
		return
	}
	if err := utils.CopyFile(srcFile, dstFile, 0o644); err != nil {
		result.fail(row, "failed to copy report file: "+err.Error())
		return
	}
	result.Exported = append(result.Exported, sessionResourceExportItem{
		ResourceID:   row.ResourceID,
		ResourceType: string(row.ResourceType),
		Name:         report.Title,
		Path:         dstFile,
	})
}

// fail 记录一条导出失败。
func (r *sessionResourceExportResult) fail(row agenttypes.SessionResource, reason string) {
	r.Failed = append(r.Failed, sessionResourceExportFailure{
		ResourceID:   row.ResourceID,
		ResourceType: string(row.ResourceType),
		Reason:       reason,
	})
}

// sanitizeExportName 把资源名清洗成单层、安全的目录 / 文件名。
//
// 去掉路径分隔符与首尾的空白 / 点（避免 "." / ".."），并按字符数截断，
// 名称清洗后为空时使用 fallback
func sanitizeExportName(name, fallback string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", "\x00", "", ":", "_")
	name = replacer.Replace(strings.TrimSpace(name))
	name = strings.Trim(name, " .")
	if runes := []rune(name); len(runes) > 100 {
		name = string(runes[:100])
	}
	name = strings.Trim(name, " .")
	if name == "" {
		return fallback
	}
	return name
}

// reportMarkdownFilename 返回报告的导出文件名：<Title>.md。
// 标题已以 .md 结尾时不再重复追加扩展名。
func reportMarkdownFilename(title string, reportID int64) string {
	name := sanitizeExportName(title, fmt.Sprintf("report_%d", reportID))
	if strings.HasSuffix(strings.ToLower(name), ".md") {
		name = strings.TrimSpace(name[:len(name)-len(".md")])
	}
	if name == "" {
		name = fmt.Sprintf("report_%d", reportID)
	}
	return name + ".md"
}

// uniqueExportName 在 usedNames 中占用 name；重名时追加资源主键（仍冲突再追加序号）。
func uniqueExportName(usedNames map[string]struct{}, name string, resourceID int64) string {
	candidate := name
	if _, exists := usedNames[candidate]; exists {
		candidate = fmt.Sprintf("%s-%d", name, resourceID)
	}
	for i := 2; ; i++ {
		if _, exists := usedNames[candidate]; !exists {
			break
		}
		candidate = fmt.Sprintf("%s-%d-%d", name, resourceID, i)
	}
	usedNames[candidate] = struct{}{}
	return candidate
}

// storageBaseDir 返回 storage.base_dir；未配置时返回空串。
func (h *SessionResourceHandler) storageBaseDir() string {
	if h.cfg == nil || h.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(h.cfg.Storage.BaseDir)
}

// sessionZipFilename 返回下载用的压缩包文件名：<会话标题>.zip。
func sessionZipFilename(session agenttypes.Session) string {
	name := sanitizeExportName(session.Title, fmt.Sprintf("session_%d", session.ID))
	return name + ".zip"
}

// zipDirectory 把 srcDir 的内容打包到系统临时目录里的一个 zip 文件，返回其路径。
//
// zip 内路径相对 srcDir（压缩包根目录就是 report 目录的内容），调用方负责删除临时文件。
func zipDirectory(srcDir string) (string, error) {
	srcDir = strings.TrimSpace(srcDir)
	if srcDir == "" {
		return "", fmt.Errorf("source directory is empty")
	}
	if info, err := os.Stat(srcDir); err != nil {
		return "", err
	} else if !info.IsDir() {
		return "", fmt.Errorf("source is not a directory: %s", srcDir)
	}

	file, err := os.CreateTemp("", "session-resources-*.zip")
	if err != nil {
		return "", err
	}
	zipPath := file.Name()
	// 打包失败时清理半成品，成功时由调用方在响应结束后删除。
	cleanupOnError := func() {
		_ = file.Close()
		_ = os.Remove(zipPath)
	}

	writer := zip.NewWriter(file)
	walkErr := filepath.Walk(srcDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if info.IsDir() {
			// 目录条目以 / 结尾，解压时能保留空的资源目录。
			header.Name += "/"
			header.Method = zip.Store
			_, err := writer.CreateHeader(header)
			return err
		}

		header.Method = zip.Deflate
		entry, err := writer.CreateHeader(header)
		if err != nil {
			return err
		}
		src, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(entry, src)
		closeErr := src.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if walkErr != nil {
		_ = writer.Close()
		cleanupOnError()
		return "", walkErr
	}
	if err := writer.Close(); err != nil {
		cleanupOnError()
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(zipPath)
		return "", err
	}
	return zipPath, nil
}
