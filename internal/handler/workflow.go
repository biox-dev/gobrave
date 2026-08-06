package handler

import (
	"encoding/json"
	stderrs "errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gobravedev/gobrave/internal/config"
	"github.com/gobravedev/gobrave/internal/errors"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"github.com/gobravedev/gobrave/internal/utils"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type WorkflowHandler struct {
	workflowService  interfaces.WorkflowService
	containerService interfaces.ContainerService
	dataService      interfaces.DataService
	projectService   interfaces.ProjectService
	storeService     interfaces.StoreService
	cfg              *config.Config
}

type WorkflowFormJSONResponse struct {
	Type     string        `json:"type"`
	FormJSON []interface{} `json:"formJson"`
}

type WorkflowFormResponse struct {
	Type           string                 `json:"type"`
	FormJSON       []interface{}          `json:"formJson"`
	AnalysisResult map[string]interface{} `json:"analysis_result"`
}

type WorkflowJSONExportResponse struct {
	Path               string           `json:"path"`
	WorkflowID         string           `json:"workflow_id"`
	Workflow           map[string]any   `json:"workflow"`
	Scripts            []map[string]any `json:"scripts"`
	ContainerTemplates []map[string]any `json:"container_templates"`
}

type createScriptRequest struct {
	ID                  int64  `json:"id,string"`
	ScriptID            string `json:"component_id"`
	InstallKey          string `json:"install_key"`
	ComponentName       string `json:"component_name"`
	Description         string `json:"description"`
	ComponentIDs        string `json:"component_ids"`
	Img                 string `json:"img"`
	ContainerTemplateID int64  `json:"container_template_id,string"`
	ToolsContainerID    string `json:"tools_container_id"`
	Prompt              string `json:"prompt"`
	IOSchema            string `json:"io_schema"`
	SubContainerID      string `json:"sub_container_id"`
	Tags                string `json:"tags"`
	FileType            string `json:"file_type"`
	ScriptType          string `json:"script_type"`
	Category            string `json:"category"`
	Content             string `json:"content"`
	OrderIndex          int    `json:"order_index"`
	Position            string `json:"position"`
	Edges               string `json:"edges"`
}

type createWorkflowRequest struct {
	ID                 int64  `json:"id,string"`
	Name               string `json:"name"`
	Img                string `json:"img"`
	Tags               string `json:"tags"`
	URL                string `json:"url"`
	Category           string `json:"category"`
	Description        string `json:"description"`
	Prompt             string `json:"prompt"`
	DagDefinition      string `json:"dag_definition"`
	WorkflowID         string `json:"relation_id"`
	RelationType       string `json:"relation_type"`
	InstallKey         string `json:"install_key"`
	ModuleID           string `json:"component_id"`
	ContainerID        string `json:"container_id"`
	ParentComponentID  string `json:"parent_component_id"`
	InputComponentIDs  string `json:"input_component_ids"`
	OutputComponentIDs string `json:"output_component_ids"`
	OrderIndex         int    `json:"order_index"`
	Version            string `json:"version"`
	Message            string `json:"message"`
}

type pageScriptRequest struct {
	types.Pagination
	Query types.ScriptPageQuery `json:"query"`
}

type pageWorkflowRequest struct {
	types.Pagination
	Query types.WorkflowPageQuery `json:"query"`
}

func NewWorkflowHandler(workflowService interfaces.WorkflowService,
	containerService interfaces.ContainerService,
	dataService interfaces.DataService,
	projectService interfaces.ProjectService,
	storeService interfaces.StoreService, cfg *config.Config) *WorkflowHandler {
	return &WorkflowHandler{
		workflowService:  workflowService,
		containerService: containerService,
		dataService:      dataService,
		projectService:   projectService,
		storeService:     storeService,
		cfg:              cfg,
	}
}

// SaveScript godoc
// @Summary      保存脚本组件
// @Description  保存 script 组件：当请求包含 id 时更新记录，否则创建新记录
// @Tags         工作流
// @Accept       json
// @Produce      json
// @Param        request  body      handler.createScriptRequest  true  "请求参数"
// @Success      200      {object}  types.Script
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /workflow/save-script [post]
func (h *WorkflowHandler) SaveScript(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
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

	projectID := project.ID
	var req createScriptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if req.Content != "" && !json.Valid([]byte(req.Content)) {
		c.Error(errors.NewValidationError("content is not valid JSON format"))
		return
	}
	if req.IOSchema != "" && !json.Valid([]byte(req.IOSchema)) {
		c.Error(errors.NewValidationError("io_schema is not valid JSON format"))
		return
	}

	var scriptID string
	if req.ID != 0 {
		existing, err := h.workflowService.GetScriptByID(c.Request.Context(), req.ID)
		if err != nil {
			if stderrs.Is(err, gorm.ErrRecordNotFound) {
				c.Error(errors.NewNotFoundError("script component not found"))
				return
			}
			c.Error(errors.NewInternalServerError("failed to query script component").WithDetails(err.Error()))
			return
		}
		scriptID = req.ScriptID
		if scriptID == "" {
			scriptID = existing.ScriptID
		}
	} else {
		scriptID = req.ScriptID
		if scriptID == "" {
			scriptID = uuid.NewString()
		}
	}

	if req.IOSchema == "" {
		req.IOSchema = GetDefaultIOSchame()
	}
	item := &types.Script{
		ID:                  req.ID,
		ScriptID:            scriptID,
		ProjectID:           projectID,
		InstallKey:          req.InstallKey,
		ComponentType:       "script",
		ComponentName:       req.ComponentName,
		Description:         req.Description,
		ComponentIDs:        req.ComponentIDs,
		Img:                 req.Img,
		ContainerTemplateID: req.ContainerTemplateID,
		ToolsContainerID:    req.ToolsContainerID,
		Prompt:              req.Prompt,
		IOSchema:            req.IOSchema,
		SubContainerID:      req.SubContainerID,
		Tags:                req.Tags,
		FileType:            req.FileType,
		ScriptType:          req.ScriptType,
		Category:            req.Category,
		Content:             req.Content,
		OrderIndex:          req.OrderIndex,
		Position:            req.Position,
		Edges:               req.Edges,
	}

	if req.ID != 0 {
		if err := h.workflowService.UpdateScript(c.Request.Context(), item); err != nil {
			if stderrs.Is(err, gorm.ErrRecordNotFound) {
				c.Error(errors.NewNotFoundError("script component not found"))
				return
			}
			c.Error(errors.NewInternalServerError("failed to update script").WithDetails(err.Error()))
			return
		}
	} else {
		if err := h.workflowService.CreateScript(c.Request.Context(), item); err != nil {
			c.Error(errors.NewInternalServerError("failed to create script").WithDetails(err.Error()))
			return
		}
	}

	scriptDir, scriptFile, _ := utils.GetScriptFile(h.cfg.Storage.BaseDir, project.ProjectID, item.ScriptType, item.ScriptID)
	ioSchemaFile := filepath.Join(scriptDir, "io_schema.json")
	// Write io_schema.json file if IOSchema is provided
	if item.IOSchema != "" {
		if err := os.MkdirAll(filepath.Dir(ioSchemaFile), 0o755); err != nil {
			c.Error(errors.NewInternalServerError("failed to prepare script directory").WithDetails(err.Error()))
			return
		}
		if err := os.WriteFile(ioSchemaFile, []byte(item.IOSchema), 0o644); err != nil {
			c.Error(errors.NewInternalServerError("failed to write io_schema file").WithDetails(err.Error()))
			return
		}
	}
	scriptFilePath := filepath.Join(scriptDir, scriptFile)
	// 如果不存在脚本文件，则创建一个空的脚本文件
	if _, err := os.Stat(scriptFilePath); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(scriptFilePath), 0o755); err != nil {
			c.Error(errors.NewInternalServerError("failed to prepare script directory").WithDetails(err.Error()))
			return
		}
		content := GetInitScript(item.ScriptType)
		if err := os.WriteFile(scriptFilePath, []byte(content), 0o644); err != nil {
			c.Error(errors.NewInternalServerError("failed to create script file").WithDetails(err.Error()))
			return
		}
	}

	c.JSON(http.StatusOK, item)
}

// SaveWorkflow godoc
// @Summary      保存工作流
// @Description  保存 workflow 组件：当请求包含 id 时更新记录，否则创建新记录
// @Tags         工作流
// @Accept       json
// @Produce      json
// @Param        request  body      handler.createWorkflowRequest  true  "请求参数"
// @Success      200      {object}  types.Workflow
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /workflow/save-workflow [post]
func (h *WorkflowHandler) SaveWorkflow(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
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

	projectID := project.ID
	var req createWorkflowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	req.Tags = normalizeJSONOrDefault(req.Tags, "[]")
	req.InputComponentIDs = normalizeJSONOrDefault(req.InputComponentIDs, "[]")
	req.OutputComponentIDs = normalizeJSONOrDefault(req.OutputComponentIDs, "[]")

	if req.DagDefinition != "" && !json.Valid([]byte(req.DagDefinition)) {
		c.Error(errors.NewValidationError("dag_definition is not valid JSON format"))
		return
	}
	if req.Tags != "" && !json.Valid([]byte(req.Tags)) {
		c.Error(errors.NewValidationError("tags is not valid JSON format"))
		return
	}
	if req.InputComponentIDs != "" && !json.Valid([]byte(req.InputComponentIDs)) {
		c.Error(errors.NewValidationError("input_component_ids is not valid JSON format"))
		return
	}
	if req.OutputComponentIDs != "" && !json.Valid([]byte(req.OutputComponentIDs)) {
		c.Error(errors.NewValidationError("output_component_ids is not valid JSON format"))
		return
	}

	workflowID := req.WorkflowID
	if req.ID != 0 {
		existing, err := h.workflowService.GetWorkflowByID(c.Request.Context(), req.ID)
		if err != nil {
			if stderrs.Is(err, gorm.ErrRecordNotFound) {
				c.Error(errors.NewNotFoundError("workflow not found"))
				return
			}
			c.Error(errors.NewInternalServerError("failed to query workflow").WithDetails(err.Error()))
			return
		}
		if workflowID == "" {
			workflowID = existing.WorkflowID
		}
	} else if workflowID == "" {
		workflowID = uuid.NewString()
	}

	item := &types.Workflow{
		ID:                 req.ID,
		ProjectID:          projectID,
		Name:               req.Name,
		Img:                req.Img,
		Tags:               datatypes.JSON([]byte(req.Tags)),
		URL:                req.URL,
		Category:           req.Category,
		Description:        req.Description,
		Prompt:             req.Prompt,
		DagDefinition:      req.DagDefinition,
		WorkflowID:         workflowID,
		RelationType:       req.RelationType,
		InstallKey:         req.InstallKey,
		ModuleID:           req.ModuleID,
		ContainerID:        req.ContainerID,
		ParentComponentID:  req.ParentComponentID,
		InputComponentIDs:  datatypes.JSON([]byte(req.InputComponentIDs)),
		OutputComponentIDs: datatypes.JSON([]byte(req.OutputComponentIDs)),
		OrderIndex:         req.OrderIndex,
		Version:            req.Version,
		Message:            req.Message,
	}

	if req.ID != 0 {
		if err := h.workflowService.UpdateWorkflow(c.Request.Context(), item); err != nil {
			if stderrs.Is(err, gorm.ErrRecordNotFound) {
				c.Error(errors.NewNotFoundError("workflow not found"))
				return
			}
			c.Error(errors.NewInternalServerError("failed to update workflow").WithDetails(err.Error()))
			return
		}
	} else {
		if err := h.workflowService.CreateWorkflow(c.Request.Context(), item); err != nil {
			c.Error(errors.NewInternalServerError("failed to create workflow").WithDetails(err.Error()))
			return
		}
	}

	c.JSON(http.StatusOK, item)
}

// FindScript godoc
// @Summary      查询组件
// @Description  查询 script：按主键 ID 查询并附带容器模板名称
// @Tags         工作流
// @Produce      json
// @Param        id  path      string  true  "Script 主键 ID"
// @Success      200      {object}  map[string]any
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /find-script/{id} [get]
func (h *WorkflowHandler) FindScript(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	idStr := c.Param("id")
	if idStr == "" {
		c.Error(errors.NewValidationError("id is required"))
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id == 0 {
		c.Error(errors.NewValidationError("id must be a valid integer"))
		return
	}

	item, err := h.workflowService.GetScriptByID(c.Request.Context(), id)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("script component not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to find script component").WithDetails(err.Error()))
		return
	}

	result := map[string]any{}
	b, err := json.Marshal(item)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to serialize script component").WithDetails(err.Error()))
		return
	}
	if err := json.Unmarshal(b, &result); err != nil {
		c.Error(errors.NewInternalServerError("failed to format script component").WithDetails(err.Error()))
		return
	}

	if item.ContainerTemplateID != 0 {
		containerTemplate, containerErr := h.containerService.GetContainerTemplateByID(c.Request.Context(), item.ContainerTemplateID)
		if containerErr == nil && containerTemplate != nil {
			result["continername"] = containerTemplate.Name
		}
	}

	c.JSON(http.StatusOK, result)
}

// PageScript godoc
// @Summary      分页查询脚本组件
// @Description  分页查询 script，支持 query 条件过滤与排序；后续扩展字段仅需新增 query 字段并补充仓储过滤逻辑
// @Tags         工作流
// @Accept       json
// @Produce      json
// @Param        request  body      handler.pageScriptRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /workflow/page-script [post]
func (h *WorkflowHandler) PageScript(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req pageScriptRequest

	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
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
	if project == nil || project.ID == 0 {
		c.Error(errors.NewValidationError("active project is required"))
		return
	}

	req.Query.ProjectID = project.ID

	items, total, err := h.workflowService.PageScript(c.Request.Context(), &req.Pagination, &req.Query)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to page scripts").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      items,
		"total":     total,
		"page":      req.GetPage(),
		"page_size": req.GetPageSize(),
		"query":     req.Query,
	})
}

// PageWorkflow godoc
// @Summary      分页查询工作流
// @Description  分页查询 workflow，支持 query 条件过滤与排序；后续扩展字段仅需新增 query 字段并补充仓储过滤逻辑
// @Tags         工作流
// @Accept       json
// @Produce      json
// @Param        request  body      handler.pageWorkflowRequest  true  "分页请求参数"
// @Success      200      {object}  map[string]interface{}
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      500      {object}  errors.AppError
// @Security     Bearer
// @Router       /workflow/page-workflow [post]
func (h *WorkflowHandler) PageWorkflow(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req pageWorkflowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	userID, ok := getCurrentUserID(c)
	if !ok {
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
	if project == nil || project.ID == 0 {
		c.Error(errors.NewValidationError("active project is required"))
		return
	}

	req.Query.ProjectID = project.ID

	items, total, err := h.workflowService.PageWorkflow(c.Request.Context(), &req.Pagination, &req.Query)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to page workflows").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"data":      items,
		"total":     total,
		"page":      req.GetPage(),
		"page_size": req.GetPageSize(),
		"query":     req.Query,
	})
}

// GetFromJSONByRelationID godoc
// @Summary      获取工作流表单配置
// @Description  根据 workflowId 解析工作流 DAG，聚合输入/参数/formJson 配置
// @Tags         工作流
// @Produce      json
// @Param        workflowId  path      string                    true  "工作流 ID"
// @Success      200         {object}  handler.WorkflowFormJSONResponse
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      404         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /workflow/tools/get-from-json/{workflowId} [get]
func (h *WorkflowHandler) GetFromJSONByWorlflow(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	workflowID := c.Param("workflowId")
	if workflowID == "" {
		c.Error(errors.NewValidationError("workflowId is required"))
		return
	}

	formJSONWrap, err := h.workflowService.GetFormJSONByWorkflowID(c.Request.Context(), workflowID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("workflow not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get form json by workflow id").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, WorkflowFormJSONResponse{
		Type:     "tools",
		FormJSON: formJSONWrap,
	})
}

// GetWorkflowVis godoc
// @Summary      获取工作流可视化 DAG
// @Description  根据 workflowId 返回 DAG 可视化结构（包含 nodes/edges，并补充 script 视图字段）
// @Tags         工作流
// @Produce      json
// @Param        workflowId  path      string  true  "工作流 ID"
// @Success      200         {object}  map[string]interface{}
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      404         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /tools/get-workflow-vis/{workflowId} [get]
func (h *WorkflowHandler) GetWorkflowVis(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	workflowID := strings.TrimSpace(c.Param("workflowId"))
	if workflowID == "" {
		c.Error(errors.NewValidationError("workflowId is required"))
		return
	}

	dagDefinition, err := h.workflowService.GetWorkflowVisByWorkflowID(c.Request.Context(), workflowID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("workflow not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get workflow visualization").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, dagDefinition)
}

// GetWorkflowForm godoc
// @Summary      获取工作流表单与分析数据
// @Description  基于 workflowId 返回 formJson，并按 input_type 自动补充 analysis_result（sample 与按 role 分组的文件）
// @Tags         工作流
// @Produce      json
// @Param        workflowId  path      string                       true  "工作流 ID"
// @Success      200         {object}  handler.WorkflowFormResponse
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      404         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /workflow/{workflowId}/form [get]
func (h *WorkflowHandler) GetWorkflowForm(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	workflowID := c.Param("workflowId")
	if workflowID == "" {
		c.Error(errors.NewValidationError("workflowId is required"))
		return
	}

	userID, ok := getCurrentUserID(c)
	if !ok {
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

	formJSONWrap, analysisResult, err := buildWorkflowFormData(c.Request.Context(), h.workflowService, h.dataService, workflowID, project.ProjectID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("workflow not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get workflow form").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, WorkflowFormResponse{
		Type:           "tools",
		FormJSON:       formJSONWrap,
		AnalysisResult: analysisResult,
	})
}

func (h *WorkflowHandler) GetScriptForm(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	scriptID := c.Param("scriptId")
	if scriptID == "" {
		c.Error(errors.NewValidationError("scriptId is required"))
		return
	}
	// 使用 int64 类型的 scriptID 进行查询
	scriptIDInt, err := strconv.ParseInt(scriptID, 10, 64)
	if err != nil {
		c.Error(errors.NewValidationError("scriptId must be a valid integer"))
		return
	}

	userID, ok := getCurrentUserID(c)
	if !ok {
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

	formJSONWrap, analysisResult, err := buildScriptFormData(c.Request.Context(), h.workflowService, h.dataService, scriptIDInt, project.ProjectID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("script not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get script form").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, WorkflowFormResponse{
		Type:           "tools",
		FormJSON:       formJSONWrap,
		AnalysisResult: analysisResult,
	})
}

// GetScriptContent godoc
// @Summary      获取脚本文件内容
// @Description  基于 scriptId 查询脚本主文件，返回绝对路径 path 与文件内容 content
// @Tags         工作流
// @Produce      json
// @Param        scriptId  path      string                 true  "脚本 ID"
// @Success      200       {object}  map[string]interface{}
// @Failure      400       {object}  errors.AppError
// @Failure      401       {object}  errors.AppError
// @Failure      404       {object}  errors.AppError
// @Failure      500       {object}  errors.AppError
// @Security     Bearer
// @Router       /script/{scriptId}/content [get]
func (h *WorkflowHandler) GetScriptContent(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	scriptID := c.Param("scriptId")
	if scriptID == "" {
		c.Error(errors.NewValidationError("scriptId is required"))
		return
	}

	// 使用 int64 类型的 scriptID 进行查询
	scriptIDInt, err := strconv.ParseInt(scriptID, 10, 64)
	if err != nil {
		c.Error(errors.NewValidationError("scriptId must be a valid integer"))
		return
	}

	scriptDir, scriptMainFile, err := h.workflowService.GetScriptFileByScriptID(c.Request.Context(), scriptIDInt)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("script not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get script main file").WithDetails(err.Error()))
		return
	}

	path := filepath.Join(scriptDir, scriptMainFile)
	// if !filepath.IsAbs(path) {
	// 	path = filepath.Join(h.cfg.Storage.BaseDir, path)
	// }

	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			c.Error(errors.NewNotFoundError("script file not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to read script file").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"path":    path,
		"content": string(content),
	})
}

func (h *WorkflowHandler) GetWorkflowById(c *gin.Context) {
	workflowId := c.Param("workflowId")
	if workflowId == "" {
		c.Error(errors.NewValidationError("workflowId is required"))
		return
	}
	workflowIDInt, err := strconv.ParseInt(workflowId, 10, 64)
	if err != nil {
		c.Error(errors.NewValidationError("workflowId must be a valid integer"))
		return
	}
	workflow, err := h.workflowService.GetWorkflowByID(c.Request.Context(), workflowIDInt)

	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("workflow not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get workflow").WithDetails(err.Error()))
		return
	}

	storeVersion := ""

	storeID := workflow.StoreID
	if storeID != 0 {
		store, err := h.storeService.GetStoreByID(c.Request.Context(), storeID)
		if err != nil {
			c.Error(errors.NewInternalServerError("failed to get store").WithDetails(err.Error()))
			return
		}
		if store != nil {
			storeVersion = store.Version
		}

	}
	workflowVersion := &types.WorkflowVersion{
		// ID:                 workflow.ID,
		// ProjectID:          workflow.ProjectID,
		// StoreID:            workflow.StoreID,
		// Name:               workflow.Name,
		// Img:                workflow.Img,
		// Tags:               workflow.Tags,
		// URL:                workflow.URL,
		// Category:           workflow.Category,
		// Description:        workflow.Description,
		// Prompt:             workflow.Prompt,
		// DagDefinition:      workflow.DagDefinition,
		// WorkflowID:         workflow.WorkflowID,
		// RelationType:       workflow.RelationType,
		// InstallKey:         workflow.InstallKey,
		// ModuleID:           workflow.ModuleID,
		// ContainerID:        workflow.ContainerID,
		// ParentComponentID:  workflow.ParentComponentID,
		// InputComponentIDs:  workflow.InputComponentIDs,
		// OutputComponentIDs: workflow.OutputComponentIDs,
		// OrderIndex:         workflow.OrderIndex,
		// Version:            workflow.Version,
		// UpdateInfo:         workflow.UpdateInfo,
		// CreatedAt:          workflow.CreatedAt,
		// UpdatedAt:          workflow.UpdatedAt,
		Workflow:     *workflow,
		StoreVersion: storeVersion,
	}

	c.JSON(http.StatusOK, workflowVersion)
}

// GetScriptById godoc
// @Summary      根据 ID 获取脚本
// @Description  根据主键 scriptId 查询脚本详情，附带 StoreVersion
// @Tags         工作流
// @Produce      json
// @Param        scriptId  path      string                true  "脚本主键 ID"
// @Success      200       {object}  types.ScriptVersion
// @Failure      400       {object}  errors.AppError
// @Failure      401       {object}  errors.AppError
// @Failure      404       {object}  errors.AppError
// @Failure      500       {object}  errors.AppError
// @Security     Bearer
// @Router       /script/{scriptId} [get]
func (h *WorkflowHandler) GetScriptById(c *gin.Context) {
	scriptId := c.Param("scriptId")
	if scriptId == "" {
		c.Error(errors.NewValidationError("scriptId is required"))
		return
	}
	scriptIDInt, err := strconv.ParseInt(scriptId, 10, 64)
	if err != nil {
		c.Error(errors.NewValidationError("scriptId must be a valid integer"))
		return
	}
	script, err := h.workflowService.GetScriptByID(c.Request.Context(), scriptIDInt)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("script not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get script").WithDetails(err.Error()))
		return
	}

	storeVersion := ""
	storeID := script.StoreID
	if storeID != 0 {
		store, err := h.storeService.GetStoreByID(c.Request.Context(), storeID)
		if err != nil {
			c.Error(errors.NewInternalServerError("failed to get store").WithDetails(err.Error()))
			return
		}
		if store != nil {
			storeVersion = store.Version
		}
	}

	scriptVersion := &types.ScriptVersion{
		Script:       *script,
		StoreVersion: storeVersion,
	}

	c.JSON(http.StatusOK, scriptVersion)
}

// GenerateWorkflowJSON godoc
// @Summary      生成工作流 JSON
// @Description  根据 workflowId 读取工作流、脚本与 ContainerTemplateID，导出可落盘的 workflow.json
// @Tags         工作流
// @Produce      json
// @Param        workflowId  path      string  true  "工作流 ID"
// @Success      200         {object}  handler.WorkflowJSONExportResponse
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      404         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /workflow/{workflowId}/generate-workflow-json [post]
func (h *WorkflowHandler) GenerateWorkflowJSON(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	workflowID := c.Param("workflowId")
	if workflowID == "" {
		c.Error(errors.NewValidationError("workflowId is required"))
		return
	}
	// 使用 int64 类型的 workflowID 进行查询
	workflowIDInt, err := strconv.ParseInt(workflowID, 10, 64)
	if err != nil {
		c.Error(errors.NewValidationError("workflowId must be a valid integer"))
		return
	}

	if h.cfg == nil || strings.TrimSpace(h.cfg.Storage.BaseDir) == "" {
		c.Error(errors.NewInternalServerError("storage base dir is not configured"))
		return
	}

	exportPayload, err := h.workflowService.GenerateWorkflowJSONByWorkflowID(c.Request.Context(), workflowIDInt, h.cfg.Storage.BaseDir)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("workflow not found"))
			return
		}
		if stderrs.Is(err, interfaces.ErrInvalidDagDefinitionJSON) {
			c.Error(errors.NewValidationError("dag_definition is not valid JSON format"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to generate workflow export").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, exportPayload)
}

// DeleteWorkflow godoc
// @Summary      删除工作流
// @Description  按 workflowId 删除工作流；如果该 workflow 下存在 analysis 记录则拒绝删除
// @Tags         工作流
// @Produce      json
// @Param        workflowId  path      string             true  "工作流 ID"
// @Success      200         {object}  map[string]string
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      404         {object}  errors.AppError
// @Failure      409         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /workflow/delete/{workflowId} [post]
func (h *WorkflowHandler) DeleteWorkflow(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	workflowID := c.Param("workflowId")
	if workflowID == "" {
		c.Error(errors.NewValidationError("workflowId is required"))
		return
	}
	workflowIDInt, err := strconv.ParseInt(workflowID, 10, 64)
	if err != nil {
		c.Error(errors.NewValidationError("workflowId must be a valid integer"))
		return
	}

	if err := h.workflowService.DeleteWorkflow(c.Request.Context(), workflowIDInt); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("workflow not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to delete workflow").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"workflow_id": workflowID,
		"message":     "workflow deleted successfully",
	})
}

// DeleteScript godoc
// @Summary      删除脚本组件
// @Description  按 scriptId 删除脚本；如果该脚本下存在 analysis_node 记录，或被某 workflow 的 dag_definition 引用，则拒绝删除
// @Tags         工作流
// @Produce      json
// @Param        scriptId  path      string             true  "脚本主键 ID"
// @Success      200       {object}  map[string]string
// @Failure      400       {object}  errors.AppError
// @Failure      401       {object}  errors.AppError
// @Failure      404       {object}  errors.AppError
// @Failure      409       {object}  errors.AppError
// @Failure      500       {object}  errors.AppError
// @Security     Bearer
// @Router       /script/delete/{scriptId} [post]
func (h *WorkflowHandler) DeleteScript(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	scriptID := c.Param("scriptId")
	if scriptID == "" {
		c.Error(errors.NewValidationError("scriptId is required"))
		return
	}
	scriptIDInt, err := strconv.ParseInt(scriptID, 10, 64)
	if err != nil {
		c.Error(errors.NewValidationError("scriptId must be a valid integer"))
		return
	}

	if err := h.workflowService.DeleteScript(c.Request.Context(), scriptIDInt); err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("script not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to delete script").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"script_id": scriptID,
		"message":   "script deleted successfully",
	})
}

func buildCompatSampleItem(item interface{}) (map[string]interface{}, error) {
	b, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}

	result := make(map[string]interface{})
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, err
	}

	result["label"] = result["sample_name"]
	result["value"] = result["id"]

	return result, nil
}

func extractStringList(v interface{}) []string {
	if v == nil {
		return nil
	}

	if values, ok := v.([]string); ok {
		return values
	}

	items, ok := v.([]interface{})
	if !ok {
		return nil
	}

	result := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && s != "" {
			result = append(result, s)
		}
	}
	return result
}

func normalizeJSONOrDefault(raw string, defaultJSON string) string {
	if strings.TrimSpace(raw) == "" {
		return defaultJSON
	}
	return raw
}
