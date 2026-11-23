package handler

import (
	"context"
	stderrs "errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// PublishProjectReportToDoc 把报告正文（output.md）发布到报告文档目录
// （POST /project-report/:reportId/publish-to-doc）。
//
// 报告内容只有一个事实源（output.md），因此发布只需把该文件写入文档目录并登记到
// SUMMARY.md，不再遍历条目。
func (h *AnalysisHandler) PublishProjectReportToDoc(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	reportID, err := strconv.ParseInt(strings.TrimSpace(c.Param("reportId")), 10, 64)
	if err != nil || reportID <= 0 {
		c.Error(errors.NewValidationError("invalid report_id").WithDetails(err.Error()))
		return
	}

	ctx := c.Request.Context()

	report, err := h.projectService.GetProjectReportDetailByID(ctx, userID, reportID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project report not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get project report").WithDetails(err.Error()))
		return
	}

	content, err := h.projectService.GetProjectReportFile(ctx, userID, report.ID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to read project report file").WithDetails(err.Error()))
		return
	}

	projectDocDir := utils.GetProjectDocDir(h.config.Storage.BaseDir, report.ProjectID, strconv.FormatInt(report.ID, 10))
	if err := h.publishReportMarkdownToDoc(projectDocDir, report, content); err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "project report published to project doc dir successfully",
	})
}

// publishReportMarkdownToDoc 把报告正文写入文档目录并登记到 SUMMARY.md。
func (h *AnalysisHandler) publishReportMarkdownToDoc(projectDocDir string, report *types.ProjectReport, content string) error {
	if report == nil {
		return errors.NewValidationError("project report is nil")
	}
	if err := os.MkdirAll(projectDocDir, 0o755); err != nil {
		return errors.NewInternalServerError("failed to create project doc dir").WithDetails(err.Error())
	}

	entry := "./output.md"
	target := filepath.Join(projectDocDir, "output.md")
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return errors.NewInternalServerError("failed to write report to project doc dir").WithDetails(err.Error())
	}

	line := fmt.Sprintf("- [%s](%s)\n", docSectionTitle(report.Title, "Report"), entry)
	if err := appendProjectDocSummary(projectDocDir, entry, line); err != nil {
		return errors.NewInternalServerError("failed to update SUMMARY.md").WithDetails(err.Error())
	}
	return nil
}

// publishAnalysisToDoc 将 Analysis 及其所有 AnalysisNode 的输出发布到报告文档目录。
func (h *AnalysisHandler) publishAnalysisToDoc(ctx context.Context, analysisID int64, projectDocDir, title string) error {
	analysis, err := h.analysisService.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		return errors.NewInternalServerError("failed to get analysis").WithDetails(err.Error())
	}
	analysisNodes, err := h.copyAnalysisNodesOutput(ctx, analysisID, projectDocDir)
	if err != nil {
		return err
	}

	var analysisNodeTitleList strings.Builder
	for _, node := range analysisNodes {
		analysisNodeTitleList.WriteString(fmt.Sprintf("- [%s](./%d/output.md)\n", node.NodeName, node.ID))
	}

	entry := fmt.Sprintf("./%d/analysis.md", analysis.ID)
	line := fmt.Sprintf("- [%s](%s)\n", docSectionTitle(title, analysis.AnalysisName), entry)
	if err := appendProjectDocSummary(projectDocDir, entry, line); err != nil {
		return errors.NewInternalServerError("failed to update SUMMARY.md").WithDetails(err.Error())
	}

	targetAnalysisFile := filepath.Join(projectDocDir, fmt.Sprint(analysis.ID), "analysis.md")
	return h.writeAnalysisToDoc(analysis, targetAnalysisFile, analysisNodeTitleList.String())
}

// publishAnalysisNodeToDoc 将单个 AnalysisNode 的输出发布到报告文档目录。
func (h *AnalysisHandler) publishAnalysisNodeToDoc(ctx context.Context, analysisNodeID int64, projectDocDir, title string) error {
	analysisNode, err := h.copyAnalysisNodeOutput(ctx, analysisNodeID, projectDocDir)
	if err != nil {
		return err
	}

	entry := fmt.Sprintf("./%d/output.md", analysisNode.ID)
	line := fmt.Sprintf("- [%s](%s)\n", docSectionTitle(title, analysisNode.NodeName), entry)
	if err := appendProjectDocSummary(projectDocDir, entry, line); err != nil {
		return errors.NewInternalServerError("failed to update SUMMARY.md").WithDetails(err.Error())
	}
	return nil
}

// publishAISummaryToDoc 将 AI 摘要发布到报告文档目录。
//
// 摘要正文会引用其所属对象（Analysis / AnalysisNode）输出目录下的图片，因此拷贝的
// 目录与 analysis / analysis_node 条目完全一致（源目录为所属对象的 OutputDir）；
// 摘要正文本身写成 markdown 放到同一个子目录下，保证正文中的相对图片链接可用。
func (h *AnalysisHandler) publishAISummaryToDoc(ctx context.Context, summaryID int64, projectDocDir, title string) error {
	if h.aiSummaryRepo == nil {
		return errors.NewInternalServerError("ai summary repository is unavailable")
	}
	summary, err := h.aiSummaryRepo.GetAISummaryByID(ctx, summaryID)
	if err != nil {
		return errors.NewInternalServerError("failed to get ai summary").WithDetails(err.Error())
	}

	var summaryDir string
	var entry string
	// projectID 用于定位摘要正文文件（见 utils.GetAISummaryFile）；
	// 优先取所属对象（Analysis / AnalysisNode）的项目，回退到摘要记录上的 ProjectID。
	var projectID int64
	switch summary.OwnerType {
	case types.SummaryOwnerAnalysisNode:
		node, err := h.copyAnalysisNodeOutput(ctx, summary.OwnerID, projectDocDir)
		if err != nil {
			return err
		}
		summaryDir = filepath.Join(projectDocDir, fmt.Sprint(node.ID))
		entry = fmt.Sprintf("./%d/%d.md", node.ID, summary.ID)
		projectID = node.ProjectID
	case types.SummaryOwnerAnalysis:
		analysis, err := h.analysisService.GetAnalysisByID(ctx, summary.OwnerID)
		if err != nil {
			return errors.NewInternalServerError("failed to get analysis").WithDetails(err.Error())
		}
		if _, err := h.copyAnalysisNodesOutput(ctx, summary.OwnerID, projectDocDir); err != nil {
			return err
		}
		summaryDir = filepath.Join(projectDocDir, fmt.Sprint(summary.OwnerID))
		entry = fmt.Sprintf("./%d/%d.md", summary.OwnerID, summary.ID)
		projectID = analysis.ProjectID
	default:
		return errors.NewInternalServerError(fmt.Sprintf("unsupported ai summary owner type: %s", summary.OwnerType))
	}
	if projectID == 0 {
		projectID = summary.ProjectID
	}

	if err := os.MkdirAll(summaryDir, 0o755); err != nil {
		return errors.NewInternalServerError("failed to create ai summary dir").WithDetails(err.Error())
	}

	// 摘要正文不再落库，从文件读取（文件缺失时视为空正文）。
	// projectID 直接作为存储目录主键。
	content, err := utils.ReadAISummaryFile(h.config.Storage.BaseDir, projectID, summary.ID)
	if err != nil {
		return errors.NewInternalServerError("failed to read ai summary content").WithDetails(err.Error())
	}

	summaryMdPath := filepath.Join(summaryDir, fmt.Sprintf("%d.md", summary.ID))
	if err := os.WriteFile(summaryMdPath, []byte(content), 0o644); err != nil {
		return errors.NewInternalServerError("failed to write ai summary to project doc dir").WithDetails(err.Error())
	}

	line := fmt.Sprintf("- [%s](%s)\n", docSectionTitle(title, summary.Title), entry)
	if err := appendProjectDocSummary(projectDocDir, entry, line); err != nil {
		return errors.NewInternalServerError("failed to update SUMMARY.md").WithDetails(err.Error())
	}
	return nil
}

// copyAnalysisNodesOutput 将 Analysis 下所有节点输出拷贝到报告文档目录的
// <analysisID>/<nodeID>/ 子目录，并返回节点列表（保持原有顺序）。
func (h *AnalysisHandler) copyAnalysisNodesOutput(ctx context.Context, analysisID int64, projectDocDir string) ([]*types.AnalysisNode, error) {
	analysisNodes, err := h.analysisService.ListAnalysisNodesByAnalysisID(ctx, analysisID)
	if err != nil {
		return nil, errors.NewInternalServerError("failed to list analysis nodes").WithDetails(err.Error())
	}

	for _, node := range analysisNodes {
		destDir := filepath.Join(projectDocDir, fmt.Sprint(analysisID), fmt.Sprint(node.ID))
		if err := utils.CopyDir(node.OutputDirectory(), destDir); err != nil {
			return nil, errors.NewInternalServerError("failed to copy analysis node output to project doc dir").WithDetails(err.Error())
		}
	}
	return analysisNodes, nil
}

// copyAnalysisNodeOutput 将单个 AnalysisNode 的输出拷贝到报告文档目录的 <nodeID>/ 子目录。
func (h *AnalysisHandler) copyAnalysisNodeOutput(ctx context.Context, analysisNodeID int64, projectDocDir string) (*types.AnalysisNode, error) {
	node, err := h.analysisService.GetAnalysisNodeByID(ctx, analysisNodeID)
	if err != nil {
		return nil, errors.NewInternalServerError("failed to get analysis node").WithDetails(err.Error())
	}

	destDir := filepath.Join(projectDocDir, fmt.Sprint(node.ID))
	if err := utils.CopyDir(node.OutputDirectory(), destDir); err != nil {
		return nil, errors.NewInternalServerError("failed to copy analysis node output to project doc dir").WithDetails(err.Error())
	}
	return node, nil
}

func (h *AnalysisHandler) writeAnalysisToDoc(analysis *types.AnalysisWorkflow, targetAnalysisFile, analysisNodeTitleList string) error {
	markdownContent := fmt.Sprintf("# %s\n\n%s\n", analysis.AnalysisName, analysisNodeTitleList)

	if err := os.MkdirAll(filepath.Dir(targetAnalysisFile), 0o755); err != nil {
		return errors.NewInternalServerError("failed to create target analysis dir").WithDetails(err.Error())
	}

	if err := os.WriteFile(targetAnalysisFile, []byte(markdownContent), 0o644); err != nil {
		return errors.NewInternalServerError("failed to write analysis markdown file").WithDetails(err.Error())
	}
	return nil
}

// docSectionTitle 返回 SUMMARY.md 中使用的标题，优先使用条目标题，其次回退到对象名称。
func docSectionTitle(title, fallback string) string {
	if strings.TrimSpace(title) != "" {
		return title
	}
	if strings.TrimSpace(fallback) != "" {
		return fallback
	}
	return "Untitled"
}

// appendProjectDocSummary 确保 SUMMARY.md 存在，并在 entry 不存在时追加 line。
func appendProjectDocSummary(projectDocDir, entry, line string) error {
	summaryPath := filepath.Join(projectDocDir, "SUMMARY.md")
	if _, err := os.Stat(summaryPath); os.IsNotExist(err) {
		if err := os.MkdirAll(projectDocDir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(summaryPath, []byte("# Summary\n\n"), 0o644); err != nil {
			return err
		}
	}

	content, err := os.ReadFile(summaryPath)
	if err != nil {
		return err
	}
	if strings.Contains(string(content), entry) {
		return nil
	}

	f, err := os.OpenFile(summaryPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = f.WriteString(line)
	return err
}
