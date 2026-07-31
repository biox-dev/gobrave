package handler

import (
	"context"
	"encoding/csv"
	"encoding/json"
	stderrs "errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gobravedev/gobrave/internal/compiler"
	"github.com/gobravedev/gobrave/internal/config"
	dagruntime "github.com/gobravedev/gobrave/internal/dag"
	"github.com/gobravedev/gobrave/internal/errors"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"github.com/gobravedev/gobrave/internal/utils"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type AnalysisHandler struct {
	analysisService         interfaces.AnalysisService
	projectService          interfaces.ProjectService
	projectRepo             interfaces.ProjectRepository
	workflowService         interfaces.WorkflowService
	dataService             interfaces.DataService
	containerService        interfaces.ContainerService
	analysisRepo            interfaces.AnalysisRepository
	dagOrchestrator         interfaces.DagOrchestrator
	dynamicDagOrchestrator  interfaces.DynamicDagOrchestrator
	dataflowDagOrchestrator interfaces.DataflowDagOrchestrator
	nodeOrchestrator        interfaces.NodeOrchestrator
	config                  *config.Config
}

type EditParamsV2Response struct {
	AnalysisName   string                 `json:"analysis_name"`
	IsReport       bool                   `json:"is_report"`
	CacheType      int                    `json:"cache_type"`
	AnalysisID     string                 `json:"analysis_id"`
	Status         string                 `json:"status"`
	ServerStatus   string                 `json:"server_status"`
	RequestParam   map[string]interface{} `json:"request_param"`
	AnalysisResult map[string]interface{} `json:"analysis_result"`
	FormJSON       []interface{}          `json:"formJson"`
}

type EditNodeParamsResponse struct {
	AnalysisName   string                 `json:"analysis_name"`
	IsReport       bool                   `json:"is_report"`
	CacheType      int                    `json:"cache_type"`
	AnalysisID     int64                  `json:"analysis_id,string"`
	AnalysisNodeID string                 `json:"analysis_node_id"`
	Status         string                 `json:"status"`
	ServerStatus   string                 `json:"server_status"`
	RequestParam   map[string]interface{} `json:"request_param"`
	AnalysisResult map[string]interface{} `json:"analysis_result"`
	FormJSON       []interface{}          `json:"formJson"`
}

type VisualizationResultResponse struct {
	Images []map[string]interface{} `json:"images"`
	Tables []map[string]interface{} `json:"tables"`
	HTMLs  []map[string]interface{} `json:"htmls"`
	Files  []map[string]interface{} `json:"files"`
}

type VisualizationNodeFileResponse struct {
	Node         map[string]interface{}      `json:"node"`
	Result       VisualizationResultResponse `json:"result"`
	Status       string                      `json:"status"`
	ServerStatus string                      `json:"server_status"`
	UrlPrefix    string                      `json:"url_prefix"`
}

type VisualizationNodeTreeItem struct {
	ScriptID   string                `json:"script_id,string"`
	ScriptName string                `json:"script_name"`
	Children   []*types.AnalysisNode `json:"children"`
}

type VisualizationNodeTreeResponse struct {
	AnalysisID   string                      `json:"analysis_id"`
	AnalysisName string                      `json:"analysis_name"`
	RelationID   string                      `json:"relation_id"`
	Result       []VisualizationNodeTreeItem `json:"result"`
}

type analysisControllerRequest struct {
	RequestParam   map[string]interface{} `json:"request_param" binding:"required"`
	AnalysisNodeID string                 `json:"analysis_node_id"`
	Save           bool                   `json:"save"`
	IsSubmit       bool                   `json:"is_submit"`
	IsReport       bool                   `json:"is_report"`
}

type analysisNodeByProjectPageRequest struct {
	types.Pagination
	ScriptID int64 `json:"script_id,string"`
}

type analysisByProjectPageRequest struct {
	types.Pagination
	Query types.AnalysisQuey `json:"query"`
}

func NewAnalysisHandler(
	analysisService interfaces.AnalysisService,
	projectService interfaces.ProjectService,
	projectRepo interfaces.ProjectRepository,
	workflowService interfaces.WorkflowService,
	dataService interfaces.DataService,
	containerService interfaces.ContainerService,
	analysisRepo interfaces.AnalysisRepository,
	dagOrchestrator interfaces.DagOrchestrator,
	dynamicDagOrchestrator interfaces.DynamicDagOrchestrator,
	dataflowDagOrchestrator interfaces.DataflowDagOrchestrator,
	nodeOrchestrator interfaces.NodeOrchestrator,
	cfg *config.Config,
) *AnalysisHandler {
	return &AnalysisHandler{
		analysisService:         analysisService,
		projectService:          projectService,
		workflowService:         workflowService,
		dataService:             dataService,
		containerService:        containerService,
		analysisRepo:            analysisRepo,
		projectRepo:             projectRepo,
		dagOrchestrator:         dagOrchestrator,
		dynamicDagOrchestrator:  dynamicDagOrchestrator,
		dataflowDagOrchestrator: dataflowDagOrchestrator,
		nodeOrchestrator:        nodeOrchestrator,
		config:                  cfg,
	}
}

func (h *AnalysisHandler) ParseParams(c *gin.Context) {
	params := make(map[string]interface{})
	if err := c.ShouldBindJSON(&params); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	requestParam, ok := params["request_param"].(map[string]interface{})
	// analysisID, _ := requestParam["analysis_id"].(string)
	// if strings.TrimSpace(analysisID) == "" {
	// 	analysisID = "preview"
	// }
	analysisIDStr := strings.TrimSpace(toString(requestParam["analysis_id"]))
	// analysisIDStr 转 int64
	analysisID, err := strconv.ParseInt(analysisIDStr, 10, 64)
	// analysisID := strings.TrimSpace(fmt.Sprintf("%v", req.RequestParam["analysis_id"]))
	if analysisID == 0 {
		analysisID = 0
	}

	if !ok {
		c.Error(errors.NewValidationError("request_param is required and must be an object"))
		return
	}
	workflowID, ok := requestParam["relation_id"].(string)
	if !ok || strings.TrimSpace(workflowID) == "" {
		c.Error(errors.NewValidationError("request_param.relation_id is required and must be a string"))
		return
	}
	formJSONWrap, err := h.workflowService.GetFormJSONByWorkflowID(c.Request.Context(), workflowID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get form JSON").WithDetails(err.Error()))
		return
	}
	dagDefinition, err := h.workflowService.GetWorkflowVisByWorkflowID(c.Request.Context(), workflowID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get workflow visualization").WithDetails(err.Error()))
		return
	}
	parseAnalysisResult, err := buildParseAnalysisResult(c.Request.Context(), h.dataService, requestParam, formJSONWrap)
	// 将requestParam+formJSONWrap写入json文件，方便调试
	if true {
		baseDir := h.config.Storage.BaseDir
		debugDir := filepath.Join(baseDir, "debug", "parse_analysis_result", analysisIDStr)
		_ = os.MkdirAll(debugDir, os.ModePerm)
		debugPath := filepath.Join(debugDir, fmt.Sprintf("input.json"))
		f, err := os.Create(debugPath)
		if err == nil {
			encoder := json.NewEncoder(f)
			encoder.SetIndent("", "  ")
			_ = encoder.Encode(map[string]interface{}{
				"request_param": requestParam,
				"form_json":     formJSONWrap,
			})
			f.Close()
			logger.Infof(context.Background(), "debug parse analysis result, analysis_id: %d, debug_path: %s", analysisID, debugPath)
		}
		// 将 parseAnalysisResult 写入json文件
		resultPath := filepath.Join(debugDir, "result.json")
		f, err = os.Create(resultPath)
		if err == nil {
			encoder := json.NewEncoder(f)
			encoder.SetIndent("", "  ")
			_ = encoder.Encode(parseAnalysisResult)
			f.Close()
			logger.Infof(context.Background(), "debug parse analysis result, analysis_id: %d, result_path: %s", analysisID, resultPath)
		}
		dagDefinitionPath := filepath.Join(debugDir, "dag_definition.json")
		f, err = os.Create(dagDefinitionPath)
		if err == nil {
			encoder := json.NewEncoder(f)
			encoder.SetIndent("", "  ")
			_ = encoder.Encode(dagDefinition)
			f.Close()
			logger.Infof(context.Background(), "debug parse analysis result, analysis_id: %d, dag_definition_path: %s", analysisID, dagDefinitionPath)
		}

	}
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to build parse analysis result").WithDetails(err.Error()))
		return
	}

	runtimeDAG, err := compiler.BuildRuntimeTasks(analysisID, parseAnalysisResult, dagDefinition)
	if true {
		// 将 parseAnalysisResult+dagDefinition 写入json文件

		baseDir := h.config.Storage.BaseDir
		dagDebug := filepath.Join(baseDir, "debug", "dag", analysisIDStr)
		_ = os.MkdirAll(dagDebug, os.ModePerm)
		paramsPath := filepath.Join(dagDebug, "params.json")
		f, err := os.Create(paramsPath)
		if err == nil {
			encoder := json.NewEncoder(f)
			encoder.SetIndent("", "  ")
			_ = encoder.Encode(map[string]interface{}{
				"parse_analysis_result": parseAnalysisResult,
				"dag_definition":        dagDefinition,
				"analysis_id":           analysisID,
			})
			f.Close()
			logger.Infof(context.Background(), "debug compile runtime dag, analysis_id: %d, params_path: %s", analysisID, paramsPath)
		}

		resultPath := filepath.Join(dagDebug, "runtime_dag.json")
		f, err = os.Create(resultPath)
		if err == nil {
			encoder := json.NewEncoder(f)
			encoder.SetIndent("", "  ")
			_ = encoder.Encode(runtimeDAG)
			f.Close()
			logger.Infof(context.Background(), "debug compile runtime dag, analysis_id: %d, runtime_dag_path: %s", analysisID, resultPath)
		}

	}

	if err != nil {
		c.Error(errors.NewInternalServerError("failed to compile runtime dag").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"form_json":             formJSONWrap,
		"dag_definition":        dagDefinition,
		"parse_analysis_result": parseAnalysisResult,
		"params":                parseAnalysisResult,
		"analysis_nodes":        runtimeDAG["analysis_nodes"],
		"analysis_edges":        runtimeDAG["analysis_edges"],
	})
}

func (h *AnalysisHandler) SaveAnalysisController(c *gin.Context) {
	userID, ok := getCurrentUserID(c)

	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		c.Error(err)
		return
	}

	var req analysisControllerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	workflowID, ok := req.RequestParam["relation_id"].(string)
	if !ok || strings.TrimSpace(workflowID) == "" {
		c.Error(errors.NewValidationError("request_param.relation_id is required and must be a string"))
		return
	}

	formJSONWrap, err := h.workflowService.GetFormJSONByWorkflowID(c.Request.Context(), workflowID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get form JSON").WithDetails(err.Error()))
		return
	}

	parseAnalysisResult, err := buildParseAnalysisResult(c.Request.Context(), h.dataService, req.RequestParam, formJSONWrap)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to build parse analysis result").WithDetails(err.Error()))
		return
	}

	dagDefinition, err := h.workflowService.GetWorkflowVisByWorkflowID(c.Request.Context(), workflowID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get workflow visualization").WithDetails(err.Error()))
		return
	}

	// isRunNode := strings.TrimSpace(req.AnalysisNodeID) != ""
	// dagRuntime := map[string]any{}
	// if !isRunNode {
	// 	analysisID, _ := req.RequestParam["analysis_id"].(string)
	// 	if strings.TrimSpace(analysisID) == "" {
	// 		analysisID = "preview"
	// 	}
	// 	dagRuntime, err = compiler.BuildRuntimeTasks(analysisID, parseAnalysisResult, dagDefinition)
	// 	if err != nil {
	// 		c.Error(errors.NewInternalServerError("failed to compile runtime dag").WithDetails(err.Error()))
	// 		return
	// 	}
	// }
	// analysisID := strings.TrimSpace(fmt.Sprintf("%v", req.RequestParam["analysis_id"]))
	// if analysisID == "" || analysisID == "<nil>" {
	// 	if req.Save {
	// 		analysisID = uuid.NewString()
	// 		req.RequestParam["analysis_id"] = analysisID
	// 	} else {
	// 		analysisID = "preview"
	// 	}
	// }

	analysisIDStr := strings.TrimSpace(toString(req.RequestParam["analysis_id"]))
	// analysisIDStr 转 int64
	analysisID, err := strconv.ParseInt(analysisIDStr, 10, 64)
	// analysisID := strings.TrimSpace(fmt.Sprintf("%v", req.RequestParam["analysis_id"]))
	if analysisID == 0 {
		if req.Save {
			analysisID = utils.GenerateID()
			req.RequestParam["analysis_id"] = analysisID
		} else {
			analysisID = 0
		}
	}

	dagRuntime, err := compiler.BuildRuntimeTasks(analysisID, parseAnalysisResult, dagDefinition)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to compile runtime dag").WithDetails(err.Error()))
		return
	}
	if !req.Save {
		c.JSON(http.StatusOK, gin.H{
			"params":      parseAnalysisResult,
			"dag_runtime": dagRuntime,
		})
		return
	}

	saved, err := h.analysisService.SaveAnalysisController(c.Request.Context(), &types.AnalysisControllerSaveInput{
		RequestParam:        req.RequestParam,
		ParseAnalysisResult: parseAnalysisResult,
		DagRuntime:          dagRuntime,
		IsRunNode:           req.IsSubmit,
		IsReport:            req.IsReport,
		Project:             project,
	})
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to save analysis").WithDetails(err.Error()))
		return
	}

	if req.IsSubmit {
		if err := h.dagOrchestrator.StartAsync(c.Request.Context(), saved.ID); err != nil {
			c.Error(errors.NewInternalServerError("failed to start dag scheduler").WithDetails(err.Error()))
			return
		}
	}

	response := gin.H{
		"analysis_id":           saved.AnalysisID,
		"dag_definition":        dagDefinition,
		"parse_analysis_result": parseAnalysisResult,
		"params":                parseAnalysisResult,
		"dag_runtime":           dagRuntime,
	}

	response["submit_started"] = req.IsSubmit

	c.JSON(http.StatusOK, response)
}

// SaveAnalysisControllerV2 keeps the current JSON dag definition and analysis schema,
// but starts a dynamic Nextflow-like scheduler path that materializes analysis nodes at runtime.
func (h *AnalysisHandler) SaveAnalysisControllerV2(c *gin.Context) {
	userID, ok := getCurrentUserID(c)

	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		c.Error(err)
		return
	}

	var req analysisControllerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	workflowID, ok := req.RequestParam["relation_id"].(string)
	if !ok || strings.TrimSpace(workflowID) == "" {
		c.Error(errors.NewValidationError("request_param.relation_id is required and must be a string"))
		return
	}

	formJSONWrap, err := h.workflowService.GetFormJSONByWorkflowID(c.Request.Context(), workflowID)

	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get form JSON").WithDetails(err.Error()))
		return
	}

	parseAnalysisResult, err := buildParseAnalysisResult(c.Request.Context(), h.dataService, req.RequestParam, formJSONWrap)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to build parse analysis result").WithDetails(err.Error()))
		return
	}

	dagDefinition, err := h.workflowService.GetWorkflowVisByWorkflowID(c.Request.Context(), workflowID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get workflow visualization").WithDetails(err.Error()))
		return
	}
	analysisIDStr := strings.TrimSpace(toString(req.RequestParam["analysis_id"]))
	// analysisIDStr 转 int64
	analysisID, err := strconv.ParseInt(analysisIDStr, 10, 64)
	// analysisID := strings.TrimSpace(fmt.Sprintf("%v", req.RequestParam["analysis_id"]))
	if analysisID == 0 {
		if req.Save {
			analysisID = utils.GenerateID()
			req.RequestParam["analysis_id"] = analysisID
		} else {
			analysisID = 0
		}
	}

	dagRuntime, err := compiler.BuildRuntimeTasks(analysisID, parseAnalysisResult, dagDefinition)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to compile runtime dag").WithDetails(err.Error()))
		return
	}

	if !req.Save {
		c.JSON(http.StatusOK, gin.H{
			"params":      parseAnalysisResult,
			"dag_runtime": dagRuntime,
		})
		return
	}

	// Persist analysis metadata only; nodes are created dynamically during runtime by V2 orchestrator.
	saved, err := h.analysisService.SaveAnalysisController(c.Request.Context(), &types.AnalysisControllerSaveInput{
		RequestParam:        req.RequestParam,
		ParseAnalysisResult: parseAnalysisResult,
		DagRuntime:          nil,
		IsRunNode:           req.IsSubmit,
		IsReport:            req.IsReport,
		Project:             project,
	})
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to save analysis").WithDetails(err.Error()))
		return
	}

	if req.IsSubmit {
		if err := h.dynamicDagOrchestrator.StartAsyncV2(c.Request.Context(), project.ID, saved.ID, parseAnalysisResult, dagDefinition); err != nil {
			c.Error(errors.NewInternalServerError("failed to start dynamic dag scheduler").WithDetails(err.Error()))
			return
		}
	}

	response := gin.H{
		"analysis_id":           fmt.Sprint(saved.ID),
		"dag_definition":        dagDefinition,
		"parse_analysis_result": parseAnalysisResult,
		"params":                parseAnalysisResult,
		"dag_runtime":           dagRuntime,
		"submit_started":        req.IsSubmit,
		"scheduler_mode":        "dynamic_v2",
	}

	c.JSON(http.StatusOK, response)
}

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
	c.JSON(http.StatusOK, gin.H{
		"message": "analysis node output published to project doc dir successfully",
	})

}

func (h *AnalysisHandler) SaveAnalysisNodeControllerWithScript(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req analysisControllerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	scriptID, ok := req.RequestParam["script_id"].(string)
	if !ok || strings.TrimSpace(scriptID) == "" {
		c.Error(errors.NewValidationError("request_param.script_id is required and must be a string"))
		return
	}

	scriptIDInt, err := strconv.ParseInt(scriptID, 10, 64)
	if err != nil {
		c.Error(errors.NewValidationError("request_param.script_id must be a valid integer string").WithDetails(err.Error()))
		return
	}

	formJSONWrap, err := h.workflowService.GetScriptFormJSONByID(c.Request.Context(), scriptIDInt)

	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get form JSON").WithDetails(err.Error()))
		return
	}

	parseAnalysisResult, err := buildParseAnalysisResult(c.Request.Context(), h.dataService, req.RequestParam, formJSONWrap)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to build parse analysis result").WithDetails(err.Error()))
		return
	}
	if !req.Save {
		c.JSON(http.StatusOK, gin.H{
			"params": parseAnalysisResult,
		})
		return
	}

	activeProject, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("active project not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get active project").WithDetails(err.Error()))
		return
	}
	if activeProject == nil || strings.TrimSpace(activeProject.ProjectID) == "" {
		c.Error(errors.NewValidationError("active project is required"))
		return
	}
	projectID := strings.TrimSpace(activeProject.ProjectID)

	// analysisID := strings.TrimSpace(fmt.Sprintf("%v", req.RequestParam["analysis_id"]))
	// if analysisID == "" || analysisID == "<nil>" {
	// 	if req.Save {
	// 		analysisID = uuid.NewString()
	// 		req.RequestParam["analysis_id"] = analysisID
	// 	} else {
	// 		analysisID = "preview"
	// 	}
	// }

	requestedNodeID := parsePositiveInt64(req.RequestParam["analysis_node_id"])
	if requestedNodeID <= 0 {
		requestedNodeID = parsePositiveInt64(req.AnalysisNodeID)
	}

	var existing *types.AnalysisNode
	if requestedNodeID > 0 {
		existing, err = h.analysisRepo.GetAnalysisNodeByID(c.Request.Context(), requestedNodeID)
		if err != nil {
			if stderrs.Is(err, gorm.ErrRecordNotFound) {
				c.Error(errors.NewNotFoundError("analysis node not found"))
				return
			}
			c.Error(errors.NewInternalServerError("failed to query analysis node").WithDetails(err.Error()))
			return
		}
	}

	nodePrimaryID := requestedNodeID
	if existing == nil {
		nodePrimaryID = int64(utils.GenerateID())
	}

	analysisNodeKey := ""
	if existing != nil {
		analysisNodeKey = strings.TrimSpace(existing.AnalysisNodeID)
	}
	if analysisNodeKey == "" {
		analysisNodeKey = fmt.Sprintf("node-%d", nodePrimaryID)
	}

	nodeID := strings.TrimSpace(fmt.Sprintf("%v", req.RequestParam["node_id"]))
	if nodeID == "" || nodeID == "<nil>" {
		nodeID = "node-" + uuid.NewString()
	}

	executorName := strings.TrimSpace(fmt.Sprintf("%v", req.RequestParam["executor"]))
	if executorName == "" || executorName == "<nil>" {
		executorName = "local"
	}

	artifacts := h.buildStandaloneNodeArtifactPaths(projectID, scriptIDInt, nodePrimaryID)
	// requestParam add AnalysisNodeID
	req.RequestParam["analysis_node_id"] = nodePrimaryID

	requestParamBytes, err := json.Marshal(req.RequestParam)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to serialize request_param").WithDetails(err.Error()))
		return
	}

	node := &types.AnalysisNode{
		ID:             nodePrimaryID,
		AnalysisNodeID: analysisNodeKey,
		ProjectID:      activeProject.ID,
		// AnalysisID:             "-",
		NodeID:                 nodeID,
		NodeName:               strings.TrimSpace(fmt.Sprintf("%v", req.RequestParam["analysis_name"])),
		ScriptID:               scriptIDInt,
		InputsPatterns:         types.JSONMap{},
		ResolvedInputs:         types.JSONMap(cloneAnyMapForNode(parseAnalysisResult)),
		OutputPatterns:         types.JSONMap{},
		ResolvedOutputs:        types.JSONMap{},
		Params:                 types.JSONMap(cloneAnyMapForNode(parseAnalysisResult)),
		RequestParam:           string(requestParamBytes),
		Status:                 dagruntime.StatusReady,
		Executor:               executorName,
		Retry:                  0,
		MaxRetry:               3,
		CacheHit:               false,
		UpstreamIDs:            types.JSONSlice{},
		DownstreamIDs:          types.JSONSlice{},
		InputValidationErrors:  types.JSONSlice{},
		OutputValidationErrors: types.JSONSlice{},
		LogPath:                artifacts.LogPath,
		WorkspaceDir:           artifacts.WorkspaceDir,
		OutputDir:              artifacts.OutputDir,
		CommandPath:            artifacts.CommandPath,
		ParamsPath:             artifacts.ParamsPath,
		CreationSource:         "standalone",
	}
	if strings.TrimSpace(node.NodeName) == "" || strings.TrimSpace(node.NodeName) == "<nil>" {
		node.NodeName = scriptID
	}

	if existing != nil {
		node.ID = existing.ID
		if err := h.analysisRepo.UpdateAnalysisNodeByAnalysisNodeID(c.Request.Context(), node.AnalysisNodeID, map[string]any{
			"project_id": node.ProjectID,
			// "analysis_id":              node.AnalysisID,
			"node_id":                  node.NodeID,
			"node_name":                node.NodeName,
			"script_id":                node.ScriptID,
			"inputs_patterns":          node.InputsPatterns,
			"resolved_inputs":          node.ResolvedInputs,
			"output_patterns":          node.OutputPatterns,
			"resolved_outputs":         node.ResolvedOutputs,
			"params":                   node.Params,
			"request_param":            node.RequestParam,
			"status":                   node.Status,
			"executor":                 node.Executor,
			"retry":                    node.Retry,
			"max_retry":                node.MaxRetry,
			"cache_hit":                node.CacheHit,
			"upstream_ids":             node.UpstreamIDs,
			"downstream_ids":           node.DownstreamIDs,
			"input_validation_errors":  node.InputValidationErrors,
			"output_validation_errors": node.OutputValidationErrors,
			"log_path":                 node.LogPath,
			"workspace_dir":            node.WorkspaceDir,
			"output_dir":               node.OutputDir,
			"command_path":             node.CommandPath,
			"params_path":              node.ParamsPath,
			"creation_source":          node.CreationSource,
			"updated_at":               time.Now().UTC(),
		}); err != nil {
			c.Error(errors.NewInternalServerError("failed to update analysis node").WithDetails(err.Error()))
			return
		}
	} else {
		if err := h.analysisRepo.CreateAnalysisNodes(c.Request.Context(), []*types.AnalysisNode{node}); err != nil {
			c.Error(errors.NewInternalServerError("failed to create analysis node").WithDetails(err.Error()))
			return
		}
	}

	if err := h.initializeStandaloneNodeArtifacts(c.Request.Context(), scriptIDInt, artifacts, parseAnalysisResult); err != nil {
		c.Error(errors.NewInternalServerError("failed to initialize node runtime files").WithDetails(err.Error()))
		return
	}

	response := gin.H{
		// "analysis_id":           analysisID,
		"analysis_node_id":      strconv.FormatInt(node.ID, 10),
		"parse_analysis_result": parseAnalysisResult,
		"params":                parseAnalysisResult,
		"submit_started":        req.IsSubmit,
		"scheduler_mode":        "node_v1",
	}

	if req.IsSubmit {
		// if err := h.containerService.DeleteContainerInstancesByOwnerTypeAndOwnerIDs(
		// 	c.Request.Context(),
		// 	types.ContainerOwnerDagNode,
		// 	[]int64{int64(node.ID)},
		// ); err != nil {
		// 	c.Error(errors.NewInternalServerError("failed to cleanup previous container instances before submit").WithDetails(err.Error()))
		// 	return
		// }

		if err := h.nodeOrchestrator.StartAsync(c.Request.Context(), int64(node.ID)); err != nil {
			c.Error(errors.NewInternalServerError("failed to submit analysis node").WithDetails(err.Error()))
			return
		}
	}
	c.JSON(http.StatusOK, response)
}

// RunAnalysisNodeByScriptID 批量运行 script 下所有 creation_source=standalone 的 analysis node
func (h *AnalysisHandler) RunAnalysisNodeByScriptID(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	scriptIDStr := strings.TrimSpace(c.Param("scriptId"))
	scriptID, err := strconv.ParseInt(scriptIDStr, 10, 64)
	if err != nil || scriptID <= 0 {
		c.Error(errors.NewValidationError("invalid script_id").WithDetails(err.Error()))
		return
	}

	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("active project not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get active project").WithDetails(err.Error()))
		return
	}

	nodes, err := h.analysisService.ListAnalysisNodesByProjectIDAndScriptID(c.Request.Context(), project.ID, scriptID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to list analysis nodes").WithDetails(err.Error()))
		return
	}

	standaloneNodes := make([]*types.AnalysisNode, 0)
	for _, node := range nodes {
		if strings.EqualFold(node.CreationSource, "standalone") {
			standaloneNodes = append(standaloneNodes, node)
		}
	}

	if len(standaloneNodes) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"script_id":     scriptID,
			"total":         0,
			"submitted":     0,
			"submitted_ids": []int64{},
			"message":       "no standalone analysis nodes found for this script",
		})
		return
	}

	var submittedIDs []int64
	var errors []string

	for _, node := range standaloneNodes {
		// Skip nodes that are already in an active (non-terminal) state to prevent double-submission
		if node.Status == dagruntime.StatusRunning || node.Status == dagruntime.StatusSubmitted || node.Status == dagruntime.StatusStopping {
			errors = append(errors, fmt.Sprintf("node %d is already %s, skipped", node.ID, node.Status))
			continue
		}

		if err := h.analysisRepo.UpdateAnalysisNodeByAnalysisNodeID(c.Request.Context(), node.AnalysisNodeID, map[string]any{
			"status":                   dagruntime.StatusReady,
			"server_status":            "",
			"cache_hit":                false,
			"rerun_reason":             "batch rerun by script",
			"error_message":            "",
			"exit_code":                0,
			"pid":                      0,
			"job_id":                   "",
			"started_at":               nil,
			"finished_at":              nil,
			"output_validation_errors": types.JSONSlice{},
			"updated_at":               time.Now().UTC(),
		}); err != nil {
			errors = append(errors, fmt.Sprintf("failed to reset node %d: %v", node.ID, err))
			continue
		}

		if err := h.nodeOrchestrator.StartAsync(c.Request.Context(), node.ID); err != nil {
			errors = append(errors, fmt.Sprintf("failed to submit node %d: %v", node.ID, err))
			continue
		}
		submittedIDs = append(submittedIDs, node.ID)
	}

	c.JSON(http.StatusOK, gin.H{
		"script_id":     scriptID,
		"total":         len(standaloneNodes),
		"submitted":     len(submittedIDs),
		"submitted_ids": submittedIDs,
		"errors":        errors,
	})
}

func (h *AnalysisHandler) RunAnalysisNode(c *gin.Context) {
	analysisNodeID := strings.TrimSpace(c.Param("analysisNodeId"))
	analysisNodeIDInt, err := strconv.ParseInt(analysisNodeID, 10, 64)
	if err != nil {
		c.Error(errors.NewBadRequestError("invalid analysis node id").WithDetails(err.Error()))
		return
	}
	if err := h.RunAnalysisNodeWithID(c.Request.Context(), analysisNodeIDInt); err != nil {
		c.Error(errors.NewInternalServerError("failed to run analysis node!").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"analysis_node_id": analysisNodeID})

}

func (h *AnalysisHandler) RunAnalysisNodeWithID(ctx context.Context, analsyisNodeId int64) error {
	if analsyisNodeId <= 0 {
		return errors.NewBadRequestError("invalid analysis node id")
	}

	node, err := h.analysisRepo.GetAnalysisNodeByID(ctx, analsyisNodeId)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return errors.NewNotFoundError("analysis node not found")
		}
		return errors.NewInternalServerError("failed to query analysis node").WithDetails(err.Error())
	}

	// Guard against double-submission: skip if node is already in an active state
	if node.Status == dagruntime.StatusRunning || node.Status == dagruntime.StatusSubmitted || node.Status == dagruntime.StatusStopping {
		return errors.NewBadRequestError(fmt.Sprintf("analysis node is already %s, please wait for it to finish", node.Status))
	}

	if err := h.analysisRepo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{
		"status":                   dagruntime.StatusReady,
		"server_status":            "",
		"cache_hit":                false,
		"rerun_reason":             "node rerun requested by user",
		"error_message":            "",
		"exit_code":                0,
		"pid":                      0,
		"job_id":                   "",
		"started_at":               nil,
		"finished_at":              nil,
		"output_validation_errors": types.JSONSlice{},
		"updated_at":               time.Now().UTC(),
	}); err != nil {
		return errors.NewInternalServerError("failed to reset analysis node status").WithDetails(err.Error())
	}

	if err := h.nodeOrchestrator.StartAsync(ctx, analsyisNodeId); err != nil {
		return errors.NewInternalServerError("failed to submit analysis node").WithDetails(err.Error())
	}
	return nil
}

type standaloneNodeArtifacts struct {
	ID           int64
	WorkspaceDir string
	OutputDir    string
	ParamsPath   string
	CommandPath  string
	projectDir   string
	LogPath      string
}

func (h *AnalysisHandler) buildStandaloneNodeArtifactPaths(
	projectID string,
	scriptID int64,
	analysisNodeID int64,
) *standaloneNodeArtifacts {
	baseDir := "."
	if h != nil && h.config != nil && h.config.Storage != nil {
		if v := strings.TrimSpace(h.config.Storage.BaseDir); v != "" {
			baseDir = v
		}
	}
	analsyisNodeDir := utils.GetAnalysisNodeDir(baseDir, projectID, fmt.Sprint(scriptID))
	projectDir := utils.GetProjectDir(baseDir, projectID)
	workspaceDir := filepath.Join(analsyisNodeDir, strconv.FormatInt(analysisNodeID, 10))
	outputDir := filepath.Join(workspaceDir, "output")
	paramsPath := filepath.Join(workspaceDir, "params.json")
	commandPath := filepath.Join(workspaceDir, "run.sh")
	logPath := filepath.Join(workspaceDir, "command.log")

	return &standaloneNodeArtifacts{
		ID:           analysisNodeID,
		WorkspaceDir: workspaceDir,
		OutputDir:    outputDir,
		projectDir:   projectDir,
		ParamsPath:   paramsPath,
		CommandPath:  commandPath,
		LogPath:      logPath,
	}
}

func (h *AnalysisHandler) initializeStandaloneNodeArtifacts(
	ctx context.Context,
	scriptID int64,
	artifacts *standaloneNodeArtifacts,
	params map[string]interface{},
) error {
	if artifacts == nil {
		return fmt.Errorf("artifacts is nil")
	}

	if err := os.MkdirAll(artifacts.OutputDir, 0o755); err != nil {
		return err
	}

	paramsPayload := cloneAnyMapForNode(params)
	paramsPayload["output_dir"] = artifacts.OutputDir
	paramsPayload["project_dir"] = artifacts.projectDir
	paramsBytes, err := json.MarshalIndent(paramsPayload, "", "  ")
	if err != nil {
		return err
	}
	paramsBytes = append(paramsBytes, '\n')
	if err := os.WriteFile(artifacts.ParamsPath, paramsBytes, 0o644); err != nil {
		return err
	}

	script, err := h.workflowService.GetScriptByID(ctx, scriptID)
	if err != nil {
		return err
	}
	if script == nil {
		return fmt.Errorf("script not found")
	}
	scriptDir, scriptMainFile, err := h.workflowService.GetScriptFileByScriptID(ctx, script.ID)
	if err != nil {
		return err
	}
	scriptPath := filepath.Join(scriptDir, scriptMainFile)
	baseDir := "."
	if h != nil && h.config != nil && h.config.Storage != nil {
		if v := strings.TrimSpace(h.config.Storage.BaseDir); v != "" {
			baseDir = v
		}
	}
	if !filepath.IsAbs(scriptPath) {
		scriptPath = filepath.Join(baseDir, scriptPath)
	}

	scriptContent, err := os.ReadFile(scriptPath)
	if err != nil {
		return err
	}

	scriptWorkspaceDir := filepath.Join(artifacts.WorkspaceDir, scriptMainFile)
	if _, err := os.Lstat(scriptWorkspaceDir); err != nil {
		if os.IsNotExist(err) {
			if err := os.Symlink(scriptPath, scriptWorkspaceDir); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	// synlink io_schema.json
	ioSchemaPath := filepath.Join(scriptDir, "io_schema.json")
	scriptWorkspaceIoSchemaPath := filepath.Join(artifacts.WorkspaceDir, "io_schema.json")
	if _, err := os.Lstat(ioSchemaPath); err == nil {
		if _, err := os.Lstat(scriptWorkspaceIoSchemaPath); err != nil {

			if os.IsNotExist(err) {
				if err := os.Symlink(ioSchemaPath, scriptWorkspaceIoSchemaPath); err != nil {
					return err
				}
			}
		}
	}

	node := &types.AnalysisNode{
		ID:           artifacts.ID,
		ParamsPath:   artifacts.ParamsPath,
		OutputDir:    artifacts.OutputDir,
		WorkspaceDir: artifacts.WorkspaceDir,
		CommandPath:  artifacts.CommandPath,
		LogPath:      artifacts.LogPath,
	}
	runScript, err := buildStandaloneRunScript(node, script.ScriptType, scriptPath, string(scriptContent), paramsPayload)
	if err != nil {
		return err
	}
	if err := os.WriteFile(artifacts.CommandPath, []byte(runScript), 0o755); err != nil {
		return err
	}

	if _, err := os.Stat(artifacts.LogPath); err != nil {
		if os.IsNotExist(err) {
			if err := os.WriteFile(artifacts.LogPath, []byte(""), 0o644); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	return nil
}

func buildStandaloneRunScript(
	node *types.AnalysisNode,
	scriptType string,
	scriptPath string,
	scriptContent string,
	params map[string]interface{},
) (string, error) {

	return dagruntime.BuildRunScript(node, scriptType, scriptPath, scriptContent, params)
}

func cloneAnyMapForNode(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func parsePositiveInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int:
		if n > 0 {
			return int64(n)
		}
	case int64:
		if n > 0 {
			return n
		}
	case int32:
		if n > 0 {
			return int64(n)
		}
	case float64:
		if n > 0 {
			return int64(n)
		}
	case float32:
		if n > 0 {
			return int64(n)
		}
	case string:
		s := strings.TrimSpace(n)
		if s == "" || s == "<nil>" {
			return 0
		}
		parsed, err := strconv.ParseInt(s, 10, 64)
		if err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

// SaveAnalysisControllerV3 keeps the current request payload and persistence schema,
// while using the V3 dataflow orchestrator entry (Nextflow-like model).
func (h *AnalysisHandler) SaveAnalysisControllerV3(c *gin.Context) {
	userID, ok := getCurrentUserID(c)

	project, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		c.Error(err)
		return
	}

	var req analysisControllerRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}

	workflowID, ok := req.RequestParam["relation_id"].(string)
	if !ok || strings.TrimSpace(workflowID) == "" {
		c.Error(errors.NewValidationError("request_param.relation_id is required and must be a string"))
		return
	}

	formJSONWrap, err := h.workflowService.GetFormJSONByWorkflowID(c.Request.Context(), workflowID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get form JSON").WithDetails(err.Error()))
		return
	}

	parseAnalysisResult, err := buildParseAnalysisResult(c.Request.Context(), h.dataService, req.RequestParam, formJSONWrap)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to build parse analysis result").WithDetails(err.Error()))
		return
	}

	dagDefinition, err := h.workflowService.GetWorkflowVisByWorkflowID(c.Request.Context(), workflowID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get workflow visualization").WithDetails(err.Error()))
		return
	}

	// analysisID := strings.TrimSpace(fmt.Sprintf("%v", req.RequestParam["analysis_id"]))
	// if analysisID == "" || analysisID == "<nil>" {
	// 	if req.Save {
	// 		analysisID = uuid.NewString()
	// 		req.RequestParam["analysis_id"] = analysisID
	// 	} else {
	// 		analysisID = "preview"
	// 	}
	// }

	analysisIDStr := strings.TrimSpace(toString(req.RequestParam["analysis_id"]))
	// analysisIDStr 转 int64
	analysisID, err := strconv.ParseInt(analysisIDStr, 10, 64)
	// analysisID := strings.TrimSpace(fmt.Sprintf("%v", req.RequestParam["analysis_id"]))
	if analysisID == 0 {
		if req.Save {
			analysisID = utils.GenerateID()
			req.RequestParam["analysis_id"] = analysisID
		} else {
			analysisID = 0
		}
	}

	dagRuntime, err := compiler.BuildRuntimeTasks(analysisID, parseAnalysisResult, dagDefinition)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to compile runtime dag").WithDetails(err.Error()))
		return
	}

	if !req.Save {
		c.JSON(http.StatusOK, gin.H{
			"params":      parseAnalysisResult,
			"dag_runtime": dagRuntime,
		})
		return
	}

	saved, err := h.analysisService.SaveAnalysisController(c.Request.Context(), &types.AnalysisControllerSaveInput{
		RequestParam:        req.RequestParam,
		ParseAnalysisResult: parseAnalysisResult,
		DagRuntime:          nil,
		IsRunNode:           req.IsSubmit,
		IsReport:            req.IsReport,
		Project:             project,
	})
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to save analysis").WithDetails(err.Error()))
		return
	}

	if req.IsSubmit {
		if err := h.dataflowDagOrchestrator.StartAsyncV3(c.Request.Context(), project.ID, saved.ID, parseAnalysisResult, dagDefinition); err != nil {
			c.Error(errors.NewInternalServerError("failed to start dataflow dag scheduler").WithDetails(err.Error()))
			return
		}
	}

	response := gin.H{
		"analysis_id":           saved.AnalysisID,
		"dag_definition":        dagDefinition,
		"parse_analysis_result": parseAnalysisResult,
		"params":                parseAnalysisResult,
		"dag_runtime":           dagRuntime,
		"submit_started":        req.IsSubmit,
		"scheduler_mode":        "dataflow_v3",
	}

	c.JSON(http.StatusOK, response)
}

func (h *AnalysisHandler) StopAnalysis(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	analysisID := strings.TrimSpace(c.Param("analysisId"))

	analysisIDInt, err := strconv.ParseInt(analysisID, 10, 64)
	if err != nil || analysisIDInt <= 0 {
		c.Error(errors.NewValidationError("analysisId must be a positive integer"))
		return
	}

	if err := h.dagOrchestrator.StopAsync(c.Request.Context(), analysisIDInt); err != nil {
		c.Error(errors.NewInternalServerError("failed to stop dag scheduler").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"analysis_id":  analysisID,
		"job_status":   "stopping",
		"stop_started": true,
	})
}

func (h *AnalysisHandler) StopAnalysisNode(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	analysisNodeIDParam := strings.TrimSpace(c.Param("analysisNodeId"))
	if analysisNodeIDParam == "" {
		c.Error(errors.NewValidationError("analysisNodeId is required"))
		return
	}

	analysisNodeID, err := strconv.ParseInt(analysisNodeIDParam, 10, 64)
	if err != nil || analysisNodeID <= 0 {
		c.Error(errors.NewValidationError("analysisNodeId must be a positive integer"))
		return
	}

	if err := h.nodeOrchestrator.StopAsync(c.Request.Context(), analysisNodeID); err != nil {
		c.Error(errors.NewInternalServerError("failed to stop analysis node").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusAccepted, gin.H{
		"analysis_node_id": analysisNodeID,
		"job_status":       "stopping",
		"stop_started":     true,
	})
}

// DeleteAnalysisNode godoc
// @Summary      删除分析节点
// @Description  按 analysisNodeId 删除分析节点记录
// @Tags         分析
// @Produce      json
// @Param        analysisNodeId  path      string             true  "分析节点 ID"
// @Success      200             {object}  map[string]string
// @Failure      400             {object}  errors.AppError
// @Failure      401             {object}  errors.AppError
// @Failure      404             {object}  errors.AppError
// @Failure      500             {object}  errors.AppError
// @Security     Bearer
// @Router       /analysis/node/delete/{analysisNodeId} [post]
func (h *AnalysisHandler) DeleteAnalysisNode(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	analysisNodeIDParam := strings.TrimSpace(c.Param("analysisNodeId"))
	if analysisNodeIDParam == "" {
		c.Error(errors.NewValidationError("analysisNodeId is required"))
		return
	}

	analysisNodeID, err := strconv.ParseInt(analysisNodeIDParam, 10, 64)
	if err != nil || analysisNodeID <= 0 {
		c.Error(errors.NewValidationError("analysisNodeId must be a positive integer"))
		return
	}

	if err := h.analysisService.DeleteAnalysisNode(c.Request.Context(), analysisNodeID); err != nil {
		c.Error(errors.NewInternalServerError("failed to delete analysis node").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"analysis_node_id": analysisNodeID,
		"message":          "analysis node deleted successfully",
	})
}

// DeleteAnalysis godoc
// @Summary      删除分析任务
// @Description  按 analysisId 删除分析任务，同时清理关联的 analysis_nodes、analysis_edges、container_instances 及工作目录
// @Tags         分析
// @Produce      json
// @Param        analysisId  path      string             true  "分析 ID"
// @Success      200         {object}  map[string]string
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      404         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /analysis/delete/{analysisId} [post]
func (h *AnalysisHandler) DeleteAnalysis(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	analysisIDParam := strings.TrimSpace(c.Param("analysisId"))
	if analysisIDParam == "" {
		c.Error(errors.NewValidationError("analysisId is required"))
		return
	}

	analysisID, err := strconv.ParseInt(analysisIDParam, 10, 64)
	if err != nil || analysisID <= 0 {
		c.Error(errors.NewValidationError("analysisId must be a positive integer"))
		return
	}

	if err := h.analysisService.DeleteAnalysis(c.Request.Context(), analysisID); err != nil {
		c.Error(errors.NewInternalServerError("failed to delete analysis").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"analysis_id": analysisID,
		"message":     "analysis deleted successfully",
	})
}

// PageAnalysisByProject godoc
// @Summary      按当前项目分页查询分析任务
// @Description  默认按当前用户 active project 的 project_id 过滤，支持可选 query 条件
// @Tags         分析
// @Accept       json
// @Produce      json
// @Param        request  body      handler.analysisByProjectPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /analysis/list-by-project-page [post]
func (h *AnalysisHandler) PageAnalysisByProject(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req analysisByProjectPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	activeProject, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("active project not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get active project").WithDetails(err.Error()))
		return
	}
	if activeProject == nil || activeProject.ID == 0 {
		c.Error(errors.NewValidationError("active project is required"))
		return
	}

	projectID := activeProject.ID
	req.Query.ProjectID = projectID
	items, total, err := h.analysisService.PageAnalysisByProjectID(c.Request.Context(), &req.Pagination, projectID, &req.Query)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to page analyses by project").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":       items,
		"total":      total,
		"page":       req.GetPage(),
		"page_size":  req.GetPageSize(),
		"project_id": projectID,
		"query":      req.Query,
	})
}

// PageAnalysisNodeByProject godoc
// @Summary      按当前项目分页查询分析节点
// @Description  默认按当前用户 active project 的 project_id 过滤，支持可选 script_id
// @Tags         分析
// @Accept       json
// @Produce      json
// @Param        request  body      handler.analysisNodeByProjectPageRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /analysis/node/list-by-project-page [post]
func (h *AnalysisHandler) PageAnalysisNodeByProject(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req analysisNodeByProjectPageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	activeProject, err := h.projectService.GetActiveProjectByUserID(c.Request.Context(), userID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("active project not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get active project").WithDetails(err.Error()))
		return
	}
	if activeProject == nil || activeProject.ID == 0 {
		c.Error(errors.NewValidationError("active project is required"))
		return
	}

	projectID := activeProject.ID
	scriptID := req.ScriptID
	items, total, err := h.analysisRepo.PageAnalysisNodesByProjectID(c.Request.Context(), &req.Pagination, projectID, scriptID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to page analysis nodes by project").WithDetails(err.Error()))
		return
	}

	if req.Page < 1 {
		req.Page = 1
	}
	if req.PageSize < 1 {
		req.PageSize = 20
	}
	if req.PageSize > 10 {
		req.PageSize = 10
	}

	c.JSON(http.StatusOK, gin.H{
		"data":       items,
		"total":      total,
		"page":       req.Page,
		"page_size":  req.PageSize,
		"project_id": projectID,
	})
}

// EditParamsV2 godoc
// @Summary      编辑分析参数（V2）
// @Description  查询 analysis 基础信息，并按 workflowId 复用工作流表单逻辑返回 formJson 与 analysis_result
// @Tags         分析
// @Produce      json
// @Param        analysisId  path      string                       true  "分析 ID"
// @Success      200         {object}  handler.EditParamsV2Response
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      404         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /analysis/edit-params-v2/{analysisId} [post]
func (h *AnalysisHandler) EditParamsV2(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	analysisIDStr := c.Param("analysisId")
	analysisID, err := strconv.ParseInt(strings.TrimSpace(analysisIDStr), 10, 64)
	if err != nil || analysisID <= 0 {
		c.Error(errors.NewValidationError("analysisId must be a positive integer"))
		return
	}

	analysisItem, err := h.analysisService.GetAnalysisByID(c.Request.Context(), analysisID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("analysis not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get analysis").WithDetails(err.Error()))
		return
	}

	requestParam := make(map[string]interface{})
	if analysisItem.RequestParam != "" {
		if err := json.Unmarshal([]byte(analysisItem.RequestParam), &requestParam); err != nil {
			c.Error(errors.NewInternalServerError("failed to parse request_param").WithDetails(err.Error()))
			return
		}
	}
	requestParam["analysis_type"] = "workflow"
	requestParam["analysis_id"] = fmt.Sprintf("%d", analysisItem.ID)

	// TODO data的projectId 没有修改
	project, err := h.projectRepo.GetProjectByID(c.Request.Context(), analysisItem.ProjectID)
	if err != nil {
		c.Error(errors.NewNotFoundError("project not found"))
	}
	formJSONWrap, analysisResult, err := buildWorkflowFormData(
		c.Request.Context(),
		h.workflowService,
		h.dataService,
		analysisItem.WorkflowID,
		project.ProjectID,
	)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("workflow not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to build workflow form data").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, EditParamsV2Response{
		AnalysisName:   analysisItem.AnalysisName,
		IsReport:       analysisItem.IsReport,
		CacheType:      analysisItem.CacheType,
		AnalysisID:     analysisItem.AnalysisID,
		Status:         analysisItem.JobStatus,
		ServerStatus:   analysisItem.ServerStatus,
		RequestParam:   requestParam,
		AnalysisResult: analysisResult,
		FormJSON:       formJSONWrap,
	})
}

// EditNodeParams godoc
// @Summary      编辑分析节点参数
// @Description  依次查询 AnalysisNode、Analysis、Workflow(dag_definition)、Module(io_schema) 后组装 formJson
// @Tags         分析
// @Produce      json
// @Param        analysisNodeId  path      string                             true  "分析节点 ID"
// @Success      200             {object}  handler.EditNodeParamsResponse
// @Failure      400             {object}  errors.AppError
// @Failure      401             {object}  errors.AppError
// @Failure      404             {object}  errors.AppError
// @Failure      500             {object}  errors.AppError
// @Security     Bearer
// @Router       /analysis/edit-node-params/{analysisNodeId} [post]
func (h *AnalysisHandler) EditNodeParams(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	analysisNodeIDParam := strings.TrimSpace(c.Param("analysisNodeId"))
	if analysisNodeIDParam == "" {
		c.Error(errors.NewValidationError("analysisNodeId is required"))
		return
	}

	analysisNodeID, err := strconv.ParseInt(analysisNodeIDParam, 10, 64)
	if err != nil || analysisNodeID <= 0 {
		c.Error(errors.NewValidationError("analysisNodeId must be a positive integer"))
		return
	}

	analysisNode, err := h.analysisService.GetAnalysisNodeByID(c.Request.Context(), analysisNodeID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("analysis node not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get analysis node").WithDetails(err.Error()))
		return
	}

	scriptItem, err := h.workflowService.GetScriptByID(c.Request.Context(), analysisNode.ScriptID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("script not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get script").WithDetails(err.Error()))
		return
	}

	requestParam := make(map[string]interface{})
	if strings.TrimSpace(analysisNode.RequestParam) != "" {
		if err := json.Unmarshal([]byte(analysisNode.RequestParam), &requestParam); err != nil {
			c.Error(errors.NewInternalServerError("failed to parse analysis node request_param").WithDetails(err.Error()))
			return
		}
	}
	// requestParam["analysis_node_id"] = analysisNodeID
	requestParam["analysis_type"] = "workflow"
	requestParam["analysis_node_id"] = fmt.Sprintf("%d", analysisNode.ID)
	analysisName := analysisNode.NodeName
	isReport := false
	cacheType := types.CacheTypeRerunAll
	analysisIDValue := analysisNode.AnalysisID
	serverStatus := analysisNode.ServerStatus

	analysisItem, err := h.analysisService.GetAnalysisByID(c.Request.Context(), analysisNode.AnalysisID)
	if err != nil && !stderrs.Is(err, gorm.ErrRecordNotFound) {
		c.Error(errors.NewInternalServerError("failed to get analysis").WithDetails(err.Error()))
		return
	}

	if analysisItem != nil {
		if strings.TrimSpace(analysisItem.AnalysisName) != "" {
			analysisName = analysisItem.AnalysisName
		}
		isReport = analysisItem.IsReport
		cacheType = analysisItem.CacheType
		analysisIDValue = analysisItem.ID
		if strings.TrimSpace(analysisItem.ServerStatus) != "" {
			serverStatus = analysisItem.ServerStatus
		}
		if len(requestParam) == 0 && strings.TrimSpace(analysisItem.RequestParam) != "" {
			if err := json.Unmarshal([]byte(analysisItem.RequestParam), &requestParam); err != nil {
				c.Error(errors.NewInternalServerError("failed to parse request_param").WithDetails(err.Error()))
				return
			}
		}
	}

	// formJSON := make([]interface{}, 0)
	if analysisItem != nil && strings.TrimSpace(analysisItem.WorkflowID) != "" {
		workflowItem, err := h.workflowService.GetWorkflowByWorkflowID(c.Request.Context(), analysisItem.WorkflowID)
		if err != nil {
			if stderrs.Is(err, gorm.ErrRecordNotFound) {
				c.Error(errors.NewNotFoundError("workflow not found"))
				return
			}
			c.Error(errors.NewInternalServerError("failed to get workflow").WithDetails(err.Error()))
			return
		}
		formJSON, err := buildNodeFormJSON(workflowItem.DagDefinition, scriptItem, scriptItem.ScriptID)
		if err != nil {
			c.Error(errors.NewInternalServerError("failed to build node form json").WithDetails(err.Error()))
			return
		}
		c.JSON(http.StatusOK, EditNodeParamsResponse{
			AnalysisName:   analysisName,
			IsReport:       isReport,
			CacheType:      cacheType,
			AnalysisID:     analysisIDValue,
			AnalysisNodeID: strconv.FormatInt(analysisNode.ID, 10),
			Status:         analysisNode.Status,
			ServerStatus:   serverStatus,
			RequestParam:   requestParam,
			FormJSON:       formJSON,
		})
	} else {
		// TODO analysisNode 的 scriptID 后续变成 int64，直接使用 scriptID 查询 formJSON
		script, err := h.workflowService.GetScriptByID(c.Request.Context(), analysisNode.ScriptID)
		if err != nil {
			if stderrs.Is(err, gorm.ErrRecordNotFound) {
				c.Error(errors.NewNotFoundError("script not found"))
				return
			}
			c.Error(errors.NewInternalServerError("failed to get script").WithDetails(err.Error()))
			return
		}
		// TODO data的projectId 没有修改
		project, err := h.projectRepo.GetProjectByID(c.Request.Context(), analysisNode.ProjectID)
		if err != nil {
			c.Error(errors.NewNotFoundError("project not found"))
		}
		formJSON, analysisResult, err := buildScriptFormData(c.Request.Context(), h.workflowService, h.dataService, script.ID, project.ProjectID)

		// formJSON, err = h.workflowService.GetFormJSONByScriptID(c.Request.Context(), analysisNode.ScriptID)
		if err != nil {
			c.Error(errors.NewInternalServerError("failed to get form json by script").WithDetails(err.Error()))
			return
		}
		c.JSON(http.StatusOK, EditNodeParamsResponse{
			AnalysisName:   analysisName,
			IsReport:       isReport,
			CacheType:      cacheType,
			AnalysisID:     analysisIDValue,
			AnalysisNodeID: strconv.FormatInt(analysisNode.ID, 10),
			Status:         analysisNode.Status,
			ServerStatus:   serverStatus,
			RequestParam:   requestParam,
			FormJSON:       formJSON,
			AnalysisResult: analysisResult,
		})
	}

}

// VisualizationNodeFile godoc
// @Summary      分析节点结果文件可视化
// @Description  查询 AnalysisNode 并补充容器模板与镜像信息，同时返回 output_dir 下可视化资源列表
// @Tags         分析
// @Produce      json
// @Param        analysisNodeId  path      string                                 true  "分析节点 ID"
// @Success      200             {object}  handler.VisualizationNodeFileResponse
// @Failure      400             {object}  errors.AppError
// @Failure      401             {object}  errors.AppError
// @Failure      404             {object}  errors.AppError
// @Failure      500             {object}  errors.AppError
// @Security     Bearer
// @Router       /analysis/visualization-node-file/{analysisNodeId} [get]
func (h *AnalysisHandler) VisualizationNodeFile(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	analysisNodeIDParam := strings.TrimSpace(c.Param("analysisNodeId"))
	if analysisNodeIDParam == "" {
		c.Error(errors.NewValidationError("analysisNodeId is required"))
		return
	}

	analysisNodeID, err := strconv.ParseInt(analysisNodeIDParam, 10, 64)
	if err != nil || analysisNodeID <= 0 {
		c.Error(errors.NewValidationError("analysisNodeId must be a positive integer"))
		return
	}

	analysisNode, err := h.analysisService.GetAnalysisNodeByID(c.Request.Context(), analysisNodeID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("analysis node not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get analysis node").WithDetails(err.Error()))
		return
	}
	script, err := h.workflowService.GetScriptByID(c.Request.Context(), analysisNode.ScriptID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get script").WithDetails(err.Error()))
		return
	}
	scriptType := script.ScriptType

	nodePayload, err := h.buildVisualizationNodePayload(c, analysisNode)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to build visualization node payload").WithDetails(err.Error()))
		return
	}
	outputDir := strings.TrimSpace(analysisNode.OutputDir)
	dataDir := filepath.Join(h.config.Storage.BaseDir, "data")
	// outputDir 去除 dataDir 前缀
	if after, ok := strings.CutPrefix(outputDir, dataDir); ok {
		outputDir = after
	}

	prefix := fmt.Sprintf("/data-analysis%s/", outputDir)

	result, err := visualizationResultsPath(analysisNode.OutputDir, prefix, scriptType, h.config)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to list visualization files").WithDetails(err.Error()))
		return
	}
	projectID := analysisNode.ProjectID
	if projectID == 0 {
		analysis, err := h.analysisService.GetAnalysisByID(c.Request.Context(), analysisNode.AnalysisID)
		if err != nil {
			c.Error(errors.NewInternalServerError("failed to find project").WithDetails(err.Error()))
			return
		}
		projectID = analysis.ProjectID

	}

	// project, err := h.projectRepo.GetProjectByID(c.Request.Context(), projectID)
	// if err != nil {
	// 	c.Error(errors.NewInternalServerError("failed to get project").WithDetails(err.Error()))
	// 	return
	// }

	c.JSON(http.StatusOK, VisualizationNodeFileResponse{
		Node:         nodePayload,
		Result:       result,
		Status:       analysisNode.Status,
		ServerStatus: analysisNode.ServerStatus,
		UrlPrefix:    prefix,
	})
}
func (h *AnalysisHandler) GetAnalysisNode(c *gin.Context) *types.AnalysisNode {
	if _, ok := getCurrentUserID(c); !ok {
		return nil
	}

	analysisNodeIDParam := strings.TrimSpace(c.Param("analysisNodeId"))
	if analysisNodeIDParam == "" {
		c.Error(errors.NewValidationError("analysisNodeId is required"))
		return nil
	}

	analysisNodeID, err := strconv.ParseInt(analysisNodeIDParam, 10, 64)
	if err != nil || analysisNodeID <= 0 {
		c.Error(errors.NewValidationError("analysisNodeId must be a positive integer"))
		return nil
	}

	analysisNode, err := h.analysisService.GetAnalysisNodeByID(c.Request.Context(), analysisNodeID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("analysis node not found"))
			return nil
		}
		c.Error(errors.NewInternalServerError("failed to get analysis node").WithDetails(err.Error()))
		return nil
	}
	return analysisNode

}

func (h *AnalysisHandler) AnalysisNodeLogs(c *gin.Context) {
	analysisNode := h.GetAnalysisNode(c)
	if analysisNode == nil {
		return
	}
	logPath := strings.TrimSpace(analysisNode.LogPath)
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

func (h *AnalysisHandler) AnalysisNodeParams(c *gin.Context) {
	analysisNode := h.GetAnalysisNode(c)
	if analysisNode == nil {
		return
	}
	paramsPath := strings.TrimSpace(analysisNode.ParamsPath)
	if paramsPath == "" {
		c.Error(errors.NewNotFoundError("params_path is empty"))
		return
	}

	paramsBytes, err := os.ReadFile(paramsPath)
	if err != nil {
		if os.IsNotExist(err) {
			c.Error(errors.NewNotFoundError("params file not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to read params file").WithDetails(err.Error()))
		return
	}

	var params map[string]interface{}
	if err := json.Unmarshal(paramsBytes, &params); err != nil {
		c.Error(errors.NewInternalServerError("failed to parse params file").WithDetails(err.Error()))
		return
	}
	c.JSON(http.StatusOK, params)
}

// VisualizationNodeTree godoc
// @Summary      分析节点树可视化
// @Description  按 workflow dag_definition 的脚本节点分组，返回 analysis nodes 树结构
// @Tags         分析
// @Produce      json
// @Param        analysisId  path      string                                 true  "分析 ID"
// @Success      200         {object}  handler.VisualizationNodeTreeResponse
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      404         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /analysis/visualization-node-tree/{analysisId} [get]
func (h *AnalysisHandler) VisualizationNodeTree(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	analysisID := strings.TrimSpace(c.Param("analysisId"))
	analysisIDInt, err := strconv.ParseInt(analysisID, 10, 64)
	if err != nil || analysisIDInt <= 0 {
		c.Error(errors.NewValidationError("analysisId must be a positive integer"))
		return
	}

	analysisItem, err := h.analysisService.GetAnalysisByID(c.Request.Context(), analysisIDInt)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("analysis not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get analysis").WithDetails(err.Error()))
		return
	}

	dagDefinition, err := h.workflowService.GetWorkflowVisByWorkflowID(c.Request.Context(), analysisItem.WorkflowID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("workflow not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get workflow visualization").WithDetails(err.Error()))
		return
	}

	nodesAny, _ := dagDefinition["nodes"].([]any)
	scriptOrder := make([]string, 0, len(nodesAny))
	scriptNames := make(map[string]string)
	scriptSet := make(map[string]struct{})
	for _, nodeAny := range nodesAny {
		node, ok := nodeAny.(map[string]any)
		if !ok {
			continue
		}
		scriptID := strings.TrimSpace(fmt.Sprintf("%v", node["script_id"]))
		if scriptID == "" {
			continue
		}
		if _, exists := scriptSet[scriptID]; !exists {
			scriptSet[scriptID] = struct{}{}
			scriptOrder = append(scriptOrder, scriptID)
		}

		scriptName := strings.TrimSpace(fmt.Sprintf("%v", node["name"]))
		if scriptName == "" || scriptName == "<nil>" {
			scriptName = strings.TrimSpace(fmt.Sprintf("%v", node["script_name"]))
		}
		if scriptName == "" || scriptName == "<nil>" {
			scriptName = scriptID
		}
		scriptNames[scriptID] = scriptName
	}

	analysisNodes, err := h.analysisService.ListAnalysisNodesByAnalysisID(c.Request.Context(), analysisIDInt)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to list analysis nodes").WithDetails(err.Error()))
		return
	}

	grouped := make(map[string][]*types.AnalysisNode)
	for _, node := range analysisNodes {
		script, err := h.workflowService.GetScriptByID(c.Request.Context(), node.ScriptID)
		if err != nil {
			return
		}
		if node == nil {
			continue
		}
		if len(scriptSet) > 0 {
			if _, ok := scriptSet[script.ScriptID]; !ok {
				continue
			}
		}
		grouped[script.ScriptID] = append(grouped[script.ScriptID], node)
		if _, exists := scriptNames[script.ScriptID]; !exists || strings.TrimSpace(scriptNames[script.ScriptID]) == "" {
			scriptNames[script.ScriptID] = script.ScriptID
		}
	}

	result := make([]VisualizationNodeTreeItem, 0)
	if len(scriptOrder) > 0 {
		for _, scriptID := range scriptOrder {
			result = append(result, VisualizationNodeTreeItem{
				ScriptID:   scriptID,
				ScriptName: scriptNames[scriptID],
				Children:   grouped[scriptID],
			})
		}
	} else {
		for scriptID, children := range grouped {
			result = append(result, VisualizationNodeTreeItem{
				ScriptID:   scriptID,
				ScriptName: scriptNames[scriptID],
				Children:   children,
			})
		}
		sort.Slice(result, func(i, j int) bool {
			return result[i].ScriptID < result[j].ScriptID
		})
	}

	c.JSON(http.StatusOK, VisualizationNodeTreeResponse{
		AnalysisID:   analysisItem.AnalysisID,
		AnalysisName: analysisItem.AnalysisName,
		RelationID:   analysisItem.WorkflowID,
		Result:       result,
	})
}

func (h *AnalysisHandler) buildVisualizationNodePayload(c *gin.Context, analysisNode *types.AnalysisNode) (map[string]interface{}, error) {
	b, err := json.Marshal(analysisNode)
	if err != nil {
		return nil, err
	}

	nodeMap := make(map[string]interface{})
	if err := json.Unmarshal(b, &nodeMap); err != nil {
		return nil, err
	}

	nodeMap["status"] = analysisNode.Status
	nodeMap["server_status"] = analysisNode.ServerStatus
	nodeMap["analysis_id"] = analysisNode.AnalysisID

	// TODO
	script, err := h.workflowService.GetScriptByID(c.Request.Context(), analysisNode.ScriptID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.NewNotFoundError("script not found")
		}
		return nil, errors.NewInternalServerError("failed to get script").WithDetails(err.Error())
	}
	nodeMap["script_id"] = script.ID

	return h.attachContainerInfoToNode(c, nodeMap)
}

func (h *AnalysisHandler) attachContainerInfoToNode(c *gin.Context, node map[string]interface{}) (map[string]interface{}, error) {
	scriptID, _ := node["script_id"].(int64)
	if scriptID == 0 {
		return node, nil
	}

	snapshot, err := h.workflowService.GetScriptContainerSnapshotByScriptID(c.Request.Context(), scriptID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return node, nil
		}
		return nil, err
	}

	containerID := snapshot.ContainerID
	node["container_id"] = strconv.FormatInt(containerID, 10)
	node["container_name"] = ""
	node["container_image"] = ""
	node["image_id"] = ""
	node["image_status"] = "pending"

	node["container_name"] = strings.TrimSpace(snapshot.ContainerName)
	if snapshot.ImageID > 0 {
		node["image_id"] = snapshot.ImageID
	}

	containerImage := strings.TrimSpace(snapshot.ContainerImage)
	if containerImage == "" {
		if strings.TrimSpace(snapshot.ImageTag) != "" {
			containerImage = snapshot.ImageName + ":" + snapshot.ImageTag
		} else {
			containerImage = snapshot.ImageName
		}
	}

	status := strings.TrimSpace(snapshot.ImageStatus)
	if strings.EqualFold(status, "ready") {
		status = "exist"
	}
	if status == "" {
		status = "pending"
	}

	node["container_image"] = containerImage
	node["image_status"] = status

	return node, nil
}

func visualizationResultsPath(path, prefix string, scriptType string, cfg *config.Config) (VisualizationResultResponse, error) {
	result := VisualizationResultResponse{
		Images: make([]map[string]interface{}, 0),
		Tables: make([]map[string]interface{}, 0),
		HTMLs:  make([]map[string]interface{}, 0),
		Files:  make([]map[string]interface{}, 0),
	}

	path = strings.TrimSpace(path)
	if path == "" {
		return result, nil
	}
	// if scriptType == "qmd" {
	// 	fullPath := filepath.Join(path, "output.md")
	// 	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
	// 		return result, nil
	// 	}
	// 	markdownItem, err := buildMarkdownOutput(fullPath, cfg)
	// 	if err != nil {
	// 		return result, err
	// 	}
	// 	result.Tables = append(result.Tables, markdownItem)
	// 	return result, nil
	// }

	entries, err := os.ReadDir(path)

	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return result, err
	}

	// add  filename and filepath in result.Files
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		filename := entry.Name()
		fullPath := filepath.Join(path, filename)

		result.Files = append(result.Files, map[string]interface{}{
			"filename": filename,
			"filepath": fullPath,
			"url":      filepath.Join(prefix, filename),
		})
	}

	imageGroups := make(map[string][]map[string]interface{})
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		filename := entry.Name()
		fullPath := filepath.Join(path, filename)
		lowerName := strings.ToLower(filename)

		switch {
		case isImageFile(lowerName) && scriptType != "qmd":
			imageItem := buildImageOutput(fullPath, cfg)
			baseName := imageGroupName(filename)
			imageGroups[baseName] = append(imageGroups[baseName], imageItem)
		case strings.HasSuffix(lowerName, ".html") && scriptType != "qmd":
			htmlItem := buildHTMLOutput(fullPath, cfg)
			result.HTMLs = append(result.HTMLs, htmlItem)
		case isTableFile(lowerName) && scriptType != "qmd":
			tableItem, err := buildTableOutput(fullPath, cfg, 10)
			if err != nil {
				return result, err
			}
			result.Tables = append(result.Tables, tableItem)
		case strings.HasSuffix(lowerName, ".md"):
			markdownItem, err := buildMarkdownOutput(fullPath, cfg)
			if err != nil {
				return result, err
			}
			result.Tables = append(result.Tables, markdownItem)
		}

	}

	groupNames := make([]string, 0, len(imageGroups))
	for groupName := range imageGroups {
		groupNames = append(groupNames, groupName)
	}
	sort.Strings(groupNames)

	for _, groupName := range groupNames {
		group := imageGroups[groupName]
		if len(group) == 0 {
			continue
		}

		urls := make([]map[string]string, 0, len(group))
		for _, img := range group {
			url, _ := img["url"].(string)
			format := strings.TrimPrefix(strings.ToLower(filepath.Ext(url)), ".")
			if strings.HasSuffix(strings.ToLower(url), ".download.pdf") {
				format = "pdf"
			}
			urls = append(urls, map[string]string{
				"format": format,
				"url":    url,
			})
		}

		merged := map[string]interface{}{}
		for k, v := range group[0] {
			merged[k] = v
		}
		merged["urls"] = urls
		result.Images = append(result.Images, merged)
	}

	sort.Slice(result.Tables, func(i, j int) bool {
		a, _ := result.Tables[i]["order"].(int)
		b, _ := result.Tables[j]["order"].(int)
		return a > b
	})

	return result, nil
}

func isImageFile(name string) bool {
	return strings.HasSuffix(name, ".png") ||
		strings.HasSuffix(name, ".jpg") ||
		strings.HasSuffix(name, ".jpeg") ||
		strings.HasSuffix(name, ".pdf") ||
		strings.HasSuffix(name, ".download.pdf")
}

func isTableFile(name string) bool {
	return strings.HasSuffix(name, ".csv") ||
		// strings.HasSuffix(name, ".md") ||
		strings.HasSuffix(name, ".tsv") ||
		strings.HasSuffix(name, ".txt") ||
		strings.HasSuffix(name, ".xlsx") ||
		strings.HasSuffix(name, ".info") ||
		strings.HasSuffix(name, ".vis") ||
		strings.HasSuffix(name, ".feature.list") ||
		strings.HasSuffix(name, ".diff") ||
		strings.HasSuffix(name, ".log") ||
		strings.HasSuffix(name, ".download.tsv")
}

func imageGroupName(filename string) string {
	name := strings.TrimSuffix(filename, filepath.Ext(filename))
	if strings.HasSuffix(strings.ToLower(filename), ".download.pdf") {
		name = strings.TrimSuffix(filename, ".download.pdf")
	}
	return name
}

func buildImageOutput(path string, cfg *config.Config) map[string]interface{} {
	url := buildAnalysisFileURL(path, cfg)
	filename := filepath.Base(path)
	data := url

	lowerName := strings.ToLower(path)
	if strings.HasSuffix(lowerName, ".download.pdf") {
		filename = strings.TrimSuffix(filename, ".download.pdf")
		data = strings.TrimSuffix(url, ".download.pdf") + ".png"
	} else if strings.HasSuffix(lowerName, ".pdf") {
		filename = strings.TrimSuffix(filename, ".pdf")
	} else {
		filename = strings.TrimSuffix(filename, filepath.Ext(filename))
	}

	return map[string]interface{}{
		"data":     data,
		"type":     "img",
		"filename": filename,
		"url":      url,
	}
}

func buildHTMLOutput(path string, cfg *config.Config) map[string]interface{} {
	url := buildAnalysisFileURL(path, cfg)
	return map[string]interface{}{
		"data":     url,
		"type":     "img",
		"filename": filepath.Base(path),
		"url":      url,
	}
}
func buildMarkdownOutput(path string, cfg *config.Config) (map[string]interface{}, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	item := map[string]interface{}{
		"data":     string(content),
		"type":     "md",
		"filename": filepath.Base(path),
		"url":      buildAnalysisFileURL(path, cfg),
	}
	return item, nil
}

func buildTableOutput(path string, cfg *config.Config, rowLimit int) (map[string]interface{}, error) {
	item := map[string]interface{}{
		"data":     "",
		"order":    0,
		"type":     "table",
		"filename": filepath.Base(path),
		"url":      buildAnalysisFileURL(path, cfg),
	}

	name := strings.ToLower(path)
	switch {
	case strings.HasSuffix(name, ".download.tsv"):
		item["data"] = []interface{}{}
		item["type"] = "download"
		return item, nil
	case strings.HasSuffix(name, ".csv"):
		tableData, err := readDelimitedTable(path, ',', rowLimit)
		if err != nil {
			return nil, err
		}
		item["data"] = tableData
		return item, nil
	case strings.HasSuffix(name, ".tsv"):
		tableData, err := readDelimitedTable(path, '\t', rowLimit)
		if err != nil {
			return nil, err
		}
		item["data"] = tableData
		return item, nil
	case strings.HasSuffix(name, ".vis"):
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var data interface{}
		if err := json.Unmarshal(raw, &data); err != nil {
			item["data"] = string(raw)
		} else {
			item["data"] = data
		}
		item["type"] = strings.TrimSuffix(filepath.Base(path), ".vis")
		return item, nil
	case strings.HasSuffix(name, ".diff"):
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var data interface{}
		if err := json.Unmarshal(raw, &data); err != nil {
			item["data"] = string(raw)
		} else {
			item["data"] = data
		}
		item["type"] = "diff"
		item["order"] = 11
		return item, nil
	case strings.HasSuffix(name, ".feature.list"):
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		item["data"] = string(content)
		item["type"] = "feature_list"
		item["order"] = 9
		return item, nil
	case strings.HasSuffix(name, ".info"):
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		item["data"] = string(content)
		item["type"] = "info"
		item["order"] = 10
		return item, nil
	// case strings.HasSuffix(name, ".md"):
	// content, err := os.ReadFile(path)
	// if err != nil {
	// 	return nil, err
	// }
	// item["data"] = string(content)
	// item["type"] = "md"
	// item["order"] = 10
	// 	return item, nil
	case strings.HasSuffix(name, ".xlsx"):
		item["data"] = []interface{}{}
		item["type"] = "download"
		return item, nil
	default:
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		item["data"] = string(content)
		item["type"] = "string"
		return item, nil
	}
}

func readDelimitedTable(path string, sep rune, rowLimit int) (map[string]interface{}, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.Comma = sep
	r.FieldsPerRecord = -1

	records := make([][]string, 0, rowLimit+1)
	for {
		rec, err := r.Read()
		if err != nil {
			if stderrs.Is(err, os.ErrClosed) {
				return nil, err
			}
			if stderrs.Is(err, io.EOF) {
				break
			}
			if stderrs.Is(err, csv.ErrFieldCount) {
				records = append(records, rec)
				if len(records) > rowLimit {
					break
				}
				continue
			}
			return nil, err
		}
		records = append(records, rec)
		if rowLimit > 0 && len(records) > rowLimit {
			break
		}
	}

	nrow := 0
	ncol := 0
	if len(records) > 0 {
		ncol = len(records[0])
		nrow = len(records) - 1
	}

	tables := make([][]string, 0, len(records))
	tables = append(tables, records...)

	return map[string]interface{}{
		"nrow":   nrow,
		"ncol":   ncol,
		"tables": tables,
	}, nil
}

// scriptTypeToMarkdownLang maps a script type to a markdown code fence language identifier.
func scriptTypeToMarkdownLang(scriptType string) string {
	switch strings.ToLower(strings.TrimSpace(scriptType)) {
	case "python", "py":
		return "python"
	case "r", "rscript":
		return "r"
	case "shell", "bash", "sh":
		return "bash"
	case "perl":
		return "perl"
	case "ruby":
		return "ruby"
	case "javascript", "js", "node":
		return "javascript"
	case "typescript", "ts":
		return "typescript"
	case "go", "golang":
		return "go"
	case "java":
		return "java"
	case "rust":
		return "rust"
	case "c":
		return "c"
	case "cpp", "c++":
		return "cpp"
	case "sql":
		return "sql"
	case "nextflow", "nf":
		return "groovy"
	default:
		return ""
	}
}

func buildAnalysisFileURL(path string, cfg *config.Config) string {
	p := filepath.Clean(path)
	p = filepath.ToSlash(p)

	base := ""
	if cfg != nil && cfg.Storage != nil {
		base = strings.TrimSpace(cfg.Storage.BaseDir)
	}

	if base != "" {
		base = filepath.Clean(base)
		base = filepath.ToSlash(base)
		analysisBaseDir := fmt.Sprintf("%s/analysis", base)
		dataBaseDir := fmt.Sprintf("%s/data", base)
		if strings.HasPrefix(p, analysisBaseDir) {
			rel := strings.TrimPrefix(p, analysisBaseDir)
			if !strings.HasPrefix(rel, "/") {
				rel = "/" + rel
			}
			return "/images-analysis" + rel
		} else if strings.HasPrefix(p, dataBaseDir) {
			rel := strings.TrimPrefix(p, dataBaseDir)
			if !strings.HasPrefix(rel, "/") {
				rel = "/" + rel
			}
			return "/data-analysis" + rel
		}
	}

	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "/images-analysis" + p
}
