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

// PublishProjectReportItemToDoc 是单条目发布入口（POST /project-report-item/:itemId/publish-to-doc）。
// 它按 ProjectReportItem 的 OwnerType 解析出 OwnerID，并将对应内容发布到该条目所属
// ProjectReport 的文档目录（见 utils.GetProjectDocDir）。
//
// 支持的 OwnerType：
//   - analysis      → 发布 Analysis 及其节点输出
//   - analysis_node → 发布单个 AnalysisNode 输出
//   - ai_summary    → 发布 AI 摘要（同时拷贝其所属对象的输出目录以引用图片）
func (h *AnalysisHandler) PublishProjectReportItemToDoc(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	itemID, err := strconv.ParseInt(strings.TrimSpace(c.Param("itemId")), 10, 64)
	if err != nil || itemID <= 0 {
		c.Error(errors.NewValidationError("invalid item_id").WithDetails(err.Error()))
		return
	}

	ctx := c.Request.Context()

	item, err := h.projectService.GetProjectReportItemDetailByID(ctx, userID, itemID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project report item not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get project report item").WithDetails(err.Error()))
		return
	}

	report, err := h.projectService.GetProjectReportDetailByID(ctx, userID, item.ProjectReportID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project report not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get project report").WithDetails(err.Error()))
		return
	}

	projectDocDir := utils.GetProjectDocDir(h.config.Storage.BaseDir, report.ProjectID, strconv.FormatInt(report.ID, 10))

	if err := h.publishProjectReportItem(ctx, item, projectDocDir); err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "project report item published to project doc dir successfully",
	})
}

// PublishProjectReportToDoc 遍历报告下所有 ProjectReportItem 并逐个发布到报告文档目录
// （POST /project-report/:reportId/publish-to-doc）。
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

	items, err := h.projectService.ListProjectReportItemsByReportID(ctx, userID, report.ID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to list project report items").WithDetails(err.Error()))
		return
	}

	projectDocDir := utils.GetProjectDocDir(h.config.Storage.BaseDir, report.ProjectID, strconv.FormatInt(report.ID, 10))

	for _, item := range items {
		if err := h.publishProjectReportItem(ctx, item, projectDocDir); err != nil {
			c.Error(err)
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "project report published to project doc dir successfully",
	})
}

// publishProjectReportItem 按条目的 OwnerType 解析出 OwnerID，并将对应内容发布到
// 报告文档目录。单条目发布与整报告发布共用该方法。
func (h *AnalysisHandler) publishProjectReportItem(ctx context.Context, item *types.ProjectReportItem, projectDocDir string) error {
	if item == nil {
		return errors.NewValidationError("project report item is nil")
	}

	switch item.OwnerType {
	case types.ProjectReportItemOwnerAnalysis:
		return h.publishAnalysisToDoc(ctx, item.OwnerID, projectDocDir, item.Title)
	case types.ProjectReportItemOwnerAnalysisNode:
		return h.publishAnalysisNodeToDoc(ctx, item.OwnerID, projectDocDir, item.Title)
	case types.ProjectReportItemOwnerAISummary:
		return h.publishAISummaryToDoc(ctx, item.OwnerID, projectDocDir, item.Title)
	default:
		return errors.NewValidationError(fmt.Sprintf("unsupported owner_type: %s", item.OwnerType))
	}
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
	switch summary.OwnerType {
	case types.SummaryOwnerAnalysisNode:
		node, err := h.copyAnalysisNodeOutput(ctx, summary.OwnerID, projectDocDir)
		if err != nil {
			return err
		}
		summaryDir = filepath.Join(projectDocDir, fmt.Sprint(node.ID))
		entry = fmt.Sprintf("./%d/%d.md", node.ID, summary.ID)
	case types.SummaryOwnerAnalysis:
		if _, err := h.analysisService.GetAnalysisByID(ctx, summary.OwnerID); err != nil {
			return errors.NewInternalServerError("failed to get analysis").WithDetails(err.Error())
		}
		if _, err := h.copyAnalysisNodesOutput(ctx, summary.OwnerID, projectDocDir); err != nil {
			return err
		}
		summaryDir = filepath.Join(projectDocDir, fmt.Sprint(summary.OwnerID))
		entry = fmt.Sprintf("./%d/%d.md", summary.OwnerID, summary.ID)
	default:
		return errors.NewInternalServerError(fmt.Sprintf("unsupported ai summary owner type: %s", summary.OwnerType))
	}

	if err := os.MkdirAll(summaryDir, 0o755); err != nil {
		return errors.NewInternalServerError("failed to create ai summary dir").WithDetails(err.Error())
	}
	summaryMdPath := filepath.Join(summaryDir, fmt.Sprintf("%d.md", summary.ID))
	if err := os.WriteFile(summaryMdPath, []byte(summary.Content), 0o644); err != nil {
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
		if err := utils.CopyDir(node.OutputDir, destDir); err != nil {
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
	if err := utils.CopyDir(node.OutputDir, destDir); err != nil {
		return nil, errors.NewInternalServerError("failed to copy analysis node output to project doc dir").WithDetails(err.Error())
	}
	return node, nil
}

func (h *AnalysisHandler) writeAnalysisToDoc(analysis *types.Analysis, targetAnalysisFile, analysisNodeTitleList string) error {
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
