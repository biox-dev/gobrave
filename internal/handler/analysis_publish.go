package handler

import (
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

func (h *AnalysisHandler) PublishScriptAnalysisNodeToDoc(c *gin.Context) {

	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}
	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		c.Error(err)
		return
	}
	projectDocDir := utils.GetProjectDocDir(h.config.Storage.BaseDir, project.ProjectID)

	scriptIDStr := strings.TrimSpace(c.Param("scriptId"))
	scriptID, err := strconv.ParseInt(scriptIDStr, 10, 64)
	if err != nil || scriptID <= 0 {
		c.Error(errors.NewValidationError("invalid script_id").WithDetails(err.Error()))
		return
	}
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get script").WithDetails(err.Error()))
		return
	}
	analysisNodes, err := h.analysisService.ListAnalysisNodesByProjectIDAndScriptID(c.Request.Context(), project.ID, scriptID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to list analysis nodes").WithDetails(err.Error()))
		return
	}
	script, err := h.workflowService.GetScriptByID(c.Request.Context(), scriptID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get script file").WithDetails(err.Error()))
		return
	}

	for _, node := range analysisNodes {
		sourceDir := node.OutputDir
		destDir := filepath.Join(projectDocDir, fmt.Sprint(node.ScriptID), fmt.Sprint(node.ID))
		err = utils.CopyDir(sourceDir, destDir)
		if err != nil {
			c.Error(errors.NewInternalServerError("failed to copy analysis node output to project doc dir").WithDetails(err.Error()))
			return
		}
	}

	summaryFilePath := filepath.Join(projectDocDir, "SUMMARY.md")
	if _, err := os.Stat(summaryFilePath); os.IsNotExist(err) {
		f, err := os.Create(summaryFilePath)
		if err != nil {
			c.Error(errors.NewInternalServerError("failed to create SUMMARY.md").WithDetails(err.Error()))
			return
		}
		defer f.Close()
		f.WriteString("# Summary\n\n")
	}
	f, err := os.OpenFile(summaryFilePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to open SUMMARY.md").WithDetails(err.Error()))
		return
	}
	defer f.Close()
	content, err := os.ReadFile(summaryFilePath)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to read SUMMARY.md").WithDetails(err.Error()))
		return
	}
	if !strings.Contains(string(content), fmt.Sprintf("./%d/script.md", scriptID)) {
		line := fmt.Sprintf("- [%s](./%d/script.md)\n", script.ComponentName, scriptID)
		if _, err := f.WriteString(line); err != nil {
			c.Error(errors.NewInternalServerError("failed to write to SUMMARY.md").WithDetails(err.Error()))
			return
		}
	}

	// 构建 analysisNodeTitleList  markdown 随后放在 script.md 中，可以使用 strings.Builder 来构建
	var analysisNodeTitleList strings.Builder

	for _, node := range analysisNodes {
		// 实现避免重复写入的逻辑，先读取 SUMMARY.md 的内容，检查是否已经存在该节点的链接
		content, err := os.ReadFile(summaryFilePath)
		if err != nil {
			c.Error(errors.NewInternalServerError("failed to read SUMMARY.md").WithDetails(err.Error()))
			return
		}
		titleLine := fmt.Sprintf("- [%s](./%d/output.md)\n", node.NodeName, node.ID)

		analysisNodeTitleList.WriteString(titleLine)
		if strings.Contains(string(content), fmt.Sprintf("./%d/%d/output.md", node.ScriptID, node.ID)) {
			continue // 如果已经存在该节点的链接，则跳过写入
		}
		line := fmt.Sprintf("\t- [%s](./%d/%d/output.md)\n", node.NodeName, node.ScriptID, node.ID)
		if _, err := f.WriteString(line); err != nil {
			c.Error(errors.NewInternalServerError("failed to write to SUMMARY.md").WithDetails(err.Error()))
			return
		}
	}
	if err := h.writeScriptToDoc(script, project.ProjectID, projectDocDir, analysisNodeTitleList.String()); err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "script analysis node output published to project doc dir successfully",
	})

}

func (h *AnalysisHandler) writeScriptToDoc(script *types.Script, projectID, projectDocDir, analysisNodeTitleList string) error {
	scriptDir, scriptFile, err := utils.GetScriptFile(h.config.Storage.BaseDir, projectID, script.ScriptType, script.ScriptID)
	if err != nil {
		return errors.NewInternalServerError("failed to get script file").WithDetails(err.Error())
	}
	scriptFilePath := filepath.Join(scriptDir, scriptFile)
	if _, err := os.Stat(scriptFilePath); os.IsNotExist(err) {
		return errors.NewInternalServerError("script file does not exist").WithDetails(err.Error())
	}
	targetScriptFile := filepath.Join(projectDocDir, fmt.Sprint(script.ID), "script.md")
	// 读取脚本文件内容，写入到 markdown 文件中，添加代码块标记
	scriptContent, err := os.ReadFile(scriptFilePath)
	if err != nil {
		return errors.NewInternalServerError("failed to read script file").WithDetails(err.Error())
	}
	codeLang := scriptTypeToMarkdownLang(script.ScriptType)
	markdownContent := string(scriptContent)
	if script.ScriptType == "qmd" {
		markdownContent = fmt.Sprintf("# %s\n\n%s\n", script.ComponentName, analysisNodeTitleList)
	} else {
		markdownContent = fmt.Sprintf("# %s\n\n%s\n\n```%s\n%s\n```", script.ComponentName, analysisNodeTitleList, codeLang, string(scriptContent))

	}
	if err := os.MkdirAll(filepath.Dir(targetScriptFile), 0o755); err != nil {
		return errors.NewInternalServerError("failed to create target script dir").WithDetails(err.Error())
	}

	if err := os.WriteFile(targetScriptFile, []byte(markdownContent), 0o644); err != nil {
		return errors.NewInternalServerError("failed to write script markdown file").WithDetails(err.Error())
	}
	return nil
}

func (h *AnalysisHandler) PublishToDocByWorkflowID(c *gin.Context) {
	workflowIDStr := strings.TrimSpace(c.Param("workflowId"))
	// workflowID, err := strconv.ParseInt(workflowIDStr, 10, 64)
	// if err != nil || workflowID <= 0 {
	// 	c.Error(errors.NewValidationError("invalid workflow_id").WithDetails(err.Error()))
	// 	return
	// }
	workflow, err := h.workflowService.GetWorkflowByWorkflowID(c.Request.Context(), workflowIDStr)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get workflow").WithDetails(err.Error()))
		return
	}
	project, err := h.projectRepo.GetProjectByID(c.Request.Context(), workflow.ProjectID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get project").WithDetails(err.Error()))
		return
	}
	projectDocDir := utils.GetProjectDocDir(h.config.Storage.BaseDir, project.ProjectID)
	// 将 SUMMARY.md 中添加该分析的链接
	summaryFilePath := filepath.Join(projectDocDir, "SUMMARY.md")
	if _, err := os.Stat(summaryFilePath); os.IsNotExist(err) {
		f, err := os.Create(summaryFilePath)
		if err != nil {
			c.Error(errors.NewInternalServerError("failed to create SUMMARY.md").WithDetails(err.Error()))
			return
		}
		defer f.Close()
		f.WriteString("# Summary\n\n")
	}
	f, err := os.OpenFile(summaryFilePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to open SUMMARY.md").WithDetails(err.Error()))
		return
	}
	defer f.Close()
	content, err := os.ReadFile(summaryFilePath)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to read SUMMARY.md").WithDetails(err.Error()))
		return
	}

	if !strings.Contains(string(content), fmt.Sprintf("./%d/workflow.md", workflow.ID)) {
		line := fmt.Sprintf("- [%s](./%d/workflow.md)\n", workflow.Name, workflow.ID)
		if _, err := f.WriteString(line); err != nil {
			c.Error(errors.NewInternalServerError("failed to write to SUMMARY.md").WithDetails(err.Error()))
			return
		}
	}

	analysisList, err := h.analysisService.ListAnalysisByWorkflowID(c.Request.Context(), workflow.WorkflowID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to list analysis by workflow ID").WithDetails(err.Error()))
		return
	}
	var workflowTitleList strings.Builder

	for _, analysis := range analysisList {
		if !analysis.IsReport {
			continue
		}
		analsyisTitleLine := fmt.Sprintf("- [%s](./%d/analysis.md)\n", analysis.AnalysisName, analysis.ID)
		workflowTitleList.WriteString(analsyisTitleLine)
		if !strings.Contains(string(content), fmt.Sprintf("./%d/%d/analysis.md", workflow.ID, analysis.ID)) {
			line := fmt.Sprintf("- [%s](./%d/%d/analysis.md)\n", analysis.AnalysisName, workflow.ID, analysis.ID)

			if _, err := f.WriteString(fmt.Sprintf("\t%s", line)); err != nil {

				c.Error(errors.NewInternalServerError("failed to write to SUMMARY.md").WithDetails(err.Error()))
				return
			}
		}

		analysisNodes, err := h.analysisService.ListAnalysisNodesByAnalysisID(c.Request.Context(), analysis.ID)
		if err != nil {
			c.Error(errors.NewInternalServerError("failed to list analysis nodes").WithDetails(err.Error()))
			return
		}
		// 构建 analysisNodeTitleList  markdown 随后放在 script.md 中，可以使用 strings.Builder 来构建
		var analysisNodeTitleList strings.Builder

		for _, node := range analysisNodes {
			content, err := os.ReadFile(summaryFilePath)
			if err != nil {
				c.Error(errors.NewInternalServerError("failed to read SUMMARY.md").WithDetails(err.Error()))
				return
			}
			// titleLine := fmt.Sprintf("- [%s](./%d/%d/%d/output.md)\n", node.NodeName, workflow.ID, analysis.ID, node.ID)
			workflowNodeTitleLine := fmt.Sprintf("- [%s](./%d/%d/output.md)\n", node.NodeName, analysis.ID, node.ID)

			workflowTitleList.WriteString(fmt.Sprintf("\t%s", workflowNodeTitleLine))
			nodeTitleLine := fmt.Sprintf("- [%s](./%d/output.md)\n", node.NodeName, node.ID)

			analysisNodeTitleList.WriteString(nodeTitleLine)
			if !strings.Contains(string(content), fmt.Sprintf("./%d/%d/%d/output.md", workflow.ID, analysis.ID, node.ID)) {
				line := fmt.Sprintf("\t\t- [%s](./%d/%d/%d/output.md)\n", node.NodeName, workflow.ID, analysis.ID, node.ID)
				if _, err := f.WriteString(line); err != nil {
					c.Error(errors.NewInternalServerError("failed to write to SUMMARY.md").WithDetails(err.Error()))
					return
				}
			}

			sourceDir := node.OutputDir
			destDir := filepath.Join(projectDocDir, fmt.Sprint(workflow.ID), fmt.Sprint(analysis.ID), fmt.Sprint(node.ID))
			err = utils.CopyDir(sourceDir, destDir)
			if err != nil {
				c.Error(errors.NewInternalServerError("failed to copy analysis node output to project doc dir").WithDetails(err.Error()))
				return
			}
		}

		targetAnalysisFile := filepath.Join(projectDocDir, fmt.Sprint(workflow.ID), fmt.Sprint(analysis.ID), "analysis.md")

		if err := h.writeAnalysisToDoc(analysis, targetAnalysisFile, analysisNodeTitleList.String()); err != nil {
			c.Error(err)
			return
		}

	}

	targetWorkflowFile := filepath.Join(projectDocDir, fmt.Sprint(workflow.ID), "workflow.md")

	if err := h.writeWorkflowToDoc(workflow, targetWorkflowFile, workflowTitleList.String()); err != nil {
		c.Error(err)
		return
	}
}

func (h *AnalysisHandler) writeWorkflowToDoc(workflow *types.Workflow, targetWorkflowFile, workflowTitleList string) error {
	markdownContent := fmt.Sprintf("# %s\n\n%s\n", workflow.Name, workflowTitleList)

	if err := os.MkdirAll(filepath.Dir(targetWorkflowFile), 0o755); err != nil {
		return errors.NewInternalServerError("failed to create target workflow dir").WithDetails(err.Error())
	}

	if err := os.WriteFile(targetWorkflowFile, []byte(markdownContent), 0o644); err != nil {
		return errors.NewInternalServerError("failed to write workflow markdown file").WithDetails(err.Error())
	}
	return nil
}

func (h *AnalysisHandler) PublishToDocByAnalysisID(c *gin.Context) {
	analysisIDStr := strings.TrimSpace(c.Param("analsyisId"))
	analysisID, err := strconv.ParseInt(analysisIDStr, 10, 64)
	if err != nil || analysisID <= 0 {
		c.Error(errors.NewValidationError("invalid analysis_id").WithDetails(err.Error()))
		return
	}
	analysis, err := h.analysisService.GetAnalysisByID(c.Request.Context(), analysisID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get analysis").WithDetails(err.Error()))
		return
	}
	project, err := h.projectRepo.GetProjectByID(c.Request.Context(), analysis.ProjectID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get project").WithDetails(err.Error()))
		return
	}
	analysisNodes, err := h.analysisService.ListAnalysisNodesByAnalysisID(c.Request.Context(), analysis.ID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to list analysis nodes").WithDetails(err.Error()))
		return
	}
	projectDocDir := utils.GetProjectDocDir(h.config.Storage.BaseDir, project.ProjectID)
	// analysisOutputDir := analysis.OutputDir
	// projectDocAnalysisDir := filepath.Join(projectDocDir, fmt.Sprint(analysis.ID))
	// 拷贝 analysisOutputDir 下的所有文件到 projectDocDir 下

	for _, node := range analysisNodes {
		sourceDir := node.OutputDir
		destDir := filepath.Join(projectDocDir, fmt.Sprint(analysis.ID), fmt.Sprint(node.ID))
		err = utils.CopyDir(sourceDir, destDir)
		if err != nil {
			c.Error(errors.NewInternalServerError("failed to copy analysis node output to project doc dir").WithDetails(err.Error()))
			return
		}
	}

	// 将 SUMMARY.md 中添加该分析的链接
	summaryFilePath := filepath.Join(projectDocDir, "SUMMARY.md")
	if _, err := os.Stat(summaryFilePath); os.IsNotExist(err) {
		f, err := os.Create(summaryFilePath)
		if err != nil {
			c.Error(errors.NewInternalServerError("failed to create SUMMARY.md").WithDetails(err.Error()))
			return
		}
		defer f.Close()
		f.WriteString("# Summary\n\n")
	}
	f, err := os.OpenFile(summaryFilePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to open SUMMARY.md").WithDetails(err.Error()))
		return
	}
	defer f.Close()
	content, err := os.ReadFile(summaryFilePath)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to read SUMMARY.md").WithDetails(err.Error()))
		return
	}

	if !strings.Contains(string(content), fmt.Sprintf("./%d/analysis.md", analysisID)) {
		line := fmt.Sprintf("- [%s](./%d/analysis.md)\n", analysis.AnalysisName, analysisID)
		if _, err := f.WriteString(line); err != nil {
			c.Error(errors.NewInternalServerError("failed to write to SUMMARY.md").WithDetails(err.Error()))
			return
		}
	}

	// 构建 analysisNodeTitleList  markdown 随后放在 script.md 中，可以使用 strings.Builder 来构建
	var analysisNodeTitleList strings.Builder

	for _, node := range analysisNodes {
		// 实现避免重复写入的逻辑，先读取 SUMMARY.md 的内容，检查是否已经存在该节点的链接
		content, err := os.ReadFile(summaryFilePath)
		if err != nil {
			c.Error(errors.NewInternalServerError("failed to read SUMMARY.md").WithDetails(err.Error()))
			return
		}
		titleLine := fmt.Sprintf("- [%s](./%d/output.md)\n", node.NodeName, node.ID)

		analysisNodeTitleList.WriteString(titleLine)
		if strings.Contains(string(content), fmt.Sprintf("./%d/%d/output.md", analysis.ID, node.ID)) {
			continue // 如果已经存在该节点的链接，则跳过写入
		}
		line := fmt.Sprintf("\t- [%s](./%d/%d/output.md)\n", node.NodeName, analysis.ID, node.ID)
		if _, err := f.WriteString(line); err != nil {
			c.Error(errors.NewInternalServerError("failed to write to SUMMARY.md").WithDetails(err.Error()))
			return
		}
	}
	targetAnalysisFile := filepath.Join(projectDocDir, fmt.Sprint(analysis.ID), "analysis.md")

	if err := h.writeAnalysisToDoc(analysis, targetAnalysisFile, analysisNodeTitleList.String()); err != nil {
		c.Error(err)
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "analysis output published to project doc dir successfully",
	})
}

func (h *AnalysisHandler) writeAnalysisToDoc(analysis *types.Analysis, targetAnalysisFile, analysisNodeTitleList string) error {
	// analysisFilePath := filepath.Join(projectDocDir, fmt.Sprint(analysis.ID), "analysis.md")

	markdownContent := fmt.Sprintf("# %s\n\n%s\n", analysis.AnalysisName, analysisNodeTitleList)

	if err := os.MkdirAll(filepath.Dir(targetAnalysisFile), 0o755); err != nil {
		return errors.NewInternalServerError("failed to create target analysis dir").WithDetails(err.Error())
	}

	if err := os.WriteFile(targetAnalysisFile, []byte(markdownContent), 0o644); err != nil {
		return errors.NewInternalServerError("failed to write analysis markdown file").WithDetails(err.Error())
	}
	return nil
}

func (h *AnalysisHandler) PublishToDocByAnalysisNodeID(c *gin.Context) {

	analysisNodeIDStr := strings.TrimSpace(c.Param("analysisNodeId"))
	analysisNodeID, err := strconv.ParseInt(analysisNodeIDStr, 10, 64)
	if err != nil || analysisNodeID <= 0 {
		c.Error(errors.NewValidationError("invalid analysis_node_id").WithDetails(err.Error()))
		return
	}
	analysisNode, err := h.analysisService.GetAnalysisNodeByID(c.Request.Context(), analysisNodeID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get analysis node").WithDetails(err.Error()))
		return
	}
	project, err := h.projectRepo.GetProjectByID(c.Request.Context(), analysisNode.ProjectID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get project").WithDetails(err.Error()))
		return
	}
	projectDocDir := utils.GetProjectDocDir(h.config.Storage.BaseDir, project.ProjectID)
	analsyisNodeOutputDir := analysisNode.OutputDir
	projectDocNodeDir := filepath.Join(projectDocDir, fmt.Sprint(analysisNode.ID))
	// 拷贝 analsyisNodeOutputDir 下的所有文件到 projectDocDir 下
	err = utils.CopyDir(analsyisNodeOutputDir, projectDocNodeDir)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to copy analysis node output to project doc dir").WithDetails(err.Error()))
		return
	}
	// 将 SUMMARY.md 中添加该分析节点的链接
	summaryFilePath := filepath.Join(projectDocDir, "SUMMARY.md")
	if _, err := os.Stat(summaryFilePath); os.IsNotExist(err) {
		f, err := os.Create(summaryFilePath)
		if err != nil {
			c.Error(errors.NewInternalServerError("failed to create SUMMARY.md").WithDetails(err.Error()))
			return
		}
		defer f.Close()
		f.WriteString("# Summary\n\n")
	}
	f, err := os.OpenFile(summaryFilePath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to open SUMMARY.md").WithDetails(err.Error()))
		return
	}
	defer f.Close()
	content, err := os.ReadFile(summaryFilePath)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to read SUMMARY.md").WithDetails(err.Error()))
		return
	}
	if !strings.Contains(string(content), fmt.Sprintf("./%d/output.md", analysisNode.ID)) {
		line := fmt.Sprintf("- [%s](./%d/output.md)\n", analysisNode.NodeName, analysisNode.ID)
		if _, err := f.WriteString(line); err != nil {
			c.Error(errors.NewInternalServerError("failed to write to SUMMARY.md").WithDetails(err.Error()))
			return
		}
	}

	// 将 AnalysisNode 下的 AISummary 列表写入 projectDocNodeDir，文件名为 <summary_id>.md，
	// 并在 SUMMARY.md 中添加对应链接，title 为 AISummary ID。
	summaries, err := h.aiSummaryRepo.ListAISummariesByOwner(c.Request.Context(), types.SummaryOwnerAnalysisNode, analysisNode.ID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to list ai summaries").WithDetails(err.Error()))
		return
	}
	for _, summary := range summaries {
		summaryMdPath := filepath.Join(projectDocNodeDir, fmt.Sprintf("%d.md", summary.ID))
		if err := os.WriteFile(summaryMdPath, []byte(summary.Content), 0o644); err != nil {
			c.Error(errors.NewInternalServerError("failed to write ai summary to project doc dir").WithDetails(err.Error()))
			return
		}

		summaryLink := fmt.Sprintf("./%d/%d.md", analysisNode.ID, summary.ID)
		if !strings.Contains(string(content), summaryLink) {
			line := fmt.Sprintf("- [%s](%s)\n", summary.Title, summaryLink)
			if _, err := f.WriteString(line); err != nil {
				c.Error(errors.NewInternalServerError("failed to write to SUMMARY.md").WithDetails(err.Error()))
				return
			}
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "analysis node output published to project doc dir successfully",
	})

}

// PublishProjectReportToDoc copies a file-based project report into the project doc
// directory and registers a link in SUMMARY.md.
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

	report, err := h.projectService.GetProjectReportDetailByID(c.Request.Context(), userID, reportID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("project report not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get project report").WithDetails(err.Error()))
		return
	}
	if report.ContentSource != types.ProjectReportContentSourceFile {
		c.Error(errors.NewValidationError("only file-based project report can be published to doc"))
		return
	}

	reportIDStr := strconv.FormatInt(report.ID, 10)
	filename := filepath.Base(strings.TrimSpace(report.Filename))
	if filename == "" || filename == "." {
		filename = types.DefaultProjectReportFilename
	}
	title := strings.TrimSpace(report.Title)
	if title == "" {
		title = filename
	}

	projectDocDir := utils.GetProjectDocDir(h.config.Storage.BaseDir, report.ProjectID)
	reportDir := utils.GetProjectReportDir(h.config.Storage.BaseDir, report.ProjectID, reportIDStr)

	if err := utils.CopyDir(reportDir, filepath.Join(projectDocDir, reportIDStr)); err != nil {
		c.Error(errors.NewInternalServerError("failed to copy project report to project doc dir").WithDetails(err.Error()))
		return
	}

	entry := fmt.Sprintf("./%s/%s", reportIDStr, filename)
	line := fmt.Sprintf("- [%s](%s)\n", title, entry)
	if err := appendProjectDocSummary(projectDocDir, entry, line); err != nil {
		c.Error(errors.NewInternalServerError("failed to update SUMMARY.md").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message": "project report published to project doc dir successfully",
	})
}

// appendProjectDocSummary ensures SUMMARY.md exists and appends line only when entry is absent.
func appendProjectDocSummary(projectDocDir, entry, line string) error {
	summaryPath := filepath.Join(projectDocDir, "SUMMARY.md")
	if _, err := os.Stat(summaryPath); os.IsNotExist(err) {
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
