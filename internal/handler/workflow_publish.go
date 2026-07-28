package handler

import (
	"encoding/json"
	stderrs "errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gobravedev/gobrave/internal/errors"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"github.com/gobravedev/gobrave/internal/utils"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type PublishWorkflowRequest struct {
	WorkflowID int64  `json:"workflow_id,string"`
	Url        string `json:"url"`
	Version    string `json:"version"`
	Message    string `json:"message"`
}

func (h *WorkflowHandler) PublishWorkflow(c *gin.Context) {
	var req PublishWorkflowRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if h.cfg == nil || strings.TrimSpace(h.cfg.Storage.BaseDir) == "" {
		c.Error(errors.NewInternalServerError("storage base dir is not configured"))
		return
	}

	workflow, err := h.workflowService.GetWorkflowByID(c.Request.Context(), req.WorkflowID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("workflow not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get workflow").WithDetails(err.Error()))
		return
	}

	exportPayload, err := h.workflowService.GenerateWorkflowJSONByWorkflowID(c.Request.Context(), workflow.ID, h.cfg.Storage.BaseDir)
	if err != nil {
		if stderrs.Is(err, interfaces.ErrInvalidDagDefinitionJSON) {
			c.Error(errors.NewValidationError("dag_definition is not valid JSON format"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to generate workflow export").WithDetails(err.Error()))
		return
	}

	pathName, err := buildStorePathNameFromURL(req.Url)
	if err != nil {
		pathName = workflow.WorkflowID
	}
	storePath := filepath.Join(h.cfg.Storage.BaseDir, "store", pathName)

	publishURLsJSON, err := buildPublishURLsJSON(pathName)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to build publish urls").WithDetails(err.Error()))
		return
	}

	store := &types.Store{
		// StoreID:     workflow.WorkflowID,
		StoreType:   "workflow",
		Name:        workflow.Name,
		Origin:      "local",
		URL:         req.Url,
		Status:      "done",
		Path:        storePath,
		PathName:    pathName,
		Category:    workflow.Category,
		Tags:        workflow.Tags,
		Img:         workflow.Img,
		PublishURLs: publishURLsJSON,
		Version:     req.Version,
		Message:     req.Message,
	}

	if err := os.MkdirAll(storePath, 0o755); err != nil {
		c.Error(errors.NewInternalServerError("failed to create store path").WithDetails(err.Error()))
		return
	}

	if workflow.StoreID != 0 {
		existingStore, storeErr := h.storeService.GetStoreByID(c.Request.Context(), workflow.StoreID)
		if storeErr != nil {
			if stderrs.Is(storeErr, gorm.ErrRecordNotFound) {
				c.Error(errors.NewNotFoundError("store not found"))
				return
			}
			c.Error(errors.NewInternalServerError("failed to get store").WithDetails(storeErr.Error()))
			return
		}

		if existingStore != nil {
			if existingStore.Path != "" && existingStore.Path != storePath {
				if stat, statErr := os.Stat(existingStore.Path); statErr == nil && stat.IsDir() {
					if rmErr := os.RemoveAll(existingStore.Path); rmErr != nil {
						c.Error(errors.NewInternalServerError("failed to clean old store path").WithDetails(rmErr.Error()))
						return
					}
				}
			}
			store.ID = existingStore.ID
			// store.StoreID = existingStore.StoreID
		}

		if err := h.storeService.UpdateStore(c.Request.Context(), store); err != nil {
			c.Error(errors.NewInternalServerError("failed to update store").WithDetails(err.Error()))
			return
		}
	} else {
		if err := h.storeService.CreateStore(c.Request.Context(), store); err != nil {
			c.Error(errors.NewInternalServerError("failed to create store").WithDetails(err.Error()))
			return
		}
		workflow.StoreID = store.ID
	}

	workflow.URL = req.Url
	workflow.Version = req.Version
	workflow.Message = req.Message
	if err := h.workflowService.UpdateWorkflow(c.Request.Context(), workflow); err != nil {
		c.Error(errors.NewInternalServerError("failed to update workflow publish info").WithDetails(err.Error()))
		return
	}

	workflowSourceDir := filepath.Join(h.cfg.Storage.BaseDir, "pipeline", "tools", workflow.WorkflowID)
	workflowTargetDir := filepath.Join(storePath, "tools", workflow.WorkflowID)
	if err := copyDirReplace(workflowSourceDir, workflowTargetDir); err != nil {
		c.Error(errors.NewInternalServerError("failed to copy workflow files").WithDetails(err.Error()))
		return
	}

	storeWorkflowJSONPath := filepath.Join(storePath, "workflow.json")
	storeWorkflowBytes, err := json.MarshalIndent(exportPayload, "", "  ")
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to encode store workflow json").WithDetails(err.Error()))
		return
	}
	if err := os.WriteFile(storeWorkflowJSONPath, storeWorkflowBytes, 0o644); err != nil {
		c.Error(errors.NewInternalServerError("failed to write store workflow json").WithDetails(err.Error()))
		return
	}

	for _, scriptItem := range exportPayload.Scripts {
		scriptID := scriptIDFromExportScript(scriptItem)
		if scriptID == "" {
			continue
		}
		sourceScriptDir := filepath.Join(h.cfg.Storage.BaseDir, "pipeline", "script", scriptID)
		targetScriptDir := filepath.Join(storePath, "script", scriptID)
		if err := copyDirReplace(sourceScriptDir, targetScriptDir); err != nil {
			c.Error(errors.NewInternalServerError("failed to copy script files").WithDetails(err.Error()))
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    "success",
		"store":      store,
		"workflow":   workflow,
		"store_path": storePath,
	})

}

type PublishScriptRequest struct {
	ScriptID int64  `json:"script_id,string"`
	Url      string `json:"url"`
	Version  string `json:"version"`
	Message  string `json:"message"`
}

func (h *WorkflowHandler) PublishScript(c *gin.Context) {
	var req PublishScriptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if h.cfg == nil || strings.TrimSpace(h.cfg.Storage.BaseDir) == "" {
		c.Error(errors.NewInternalServerError("storage base dir is not configured"))
		return
	}

	script, err := h.workflowService.GetScriptByID(c.Request.Context(), req.ScriptID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("script not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get script").WithDetails(err.Error()))
		return
	}

	exportPayload, err := h.workflowService.GenerateScriptJSONByScriptID(c.Request.Context(), script.ID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to generate script export").WithDetails(err.Error()))
		return
	}

	pathName, err := buildStorePathNameFromURL(req.Url)
	if err != nil {
		pathName = script.ScriptID
	}
	storePath := filepath.Join(h.cfg.Storage.BaseDir, "store", pathName)

	publishURLsJSON, err := buildPublishURLsJSON(pathName)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to build publish urls").WithDetails(err.Error()))
		return
	}

	store := &types.Store{
		StoreType:   "script",
		Name:        script.ComponentName,
		Origin:      "local",
		URL:         req.Url,
		Status:      "done",
		Path:        storePath,
		PathName:    pathName,
		Category:    script.Category,
		Tags:        nil,
		Img:         script.Img,
		PublishURLs: publishURLsJSON,
		Version:     req.Version,
		Message:     req.Message,
	}

	if script.Tags != "" {
		if json.Valid([]byte(script.Tags)) {
			store.Tags = datatypes.JSON(script.Tags)
		}
	}

	if err := os.MkdirAll(storePath, 0o755); err != nil {
		c.Error(errors.NewInternalServerError("failed to create store path").WithDetails(err.Error()))
		return
	}

	if script.StoreID != 0 {
		existingStore, storeErr := h.storeService.GetStoreByID(c.Request.Context(), script.StoreID)
		if storeErr != nil {
			if stderrs.Is(storeErr, gorm.ErrRecordNotFound) {
				c.Error(errors.NewNotFoundError("store not found"))
				return
			}
			c.Error(errors.NewInternalServerError("failed to get store").WithDetails(storeErr.Error()))
			return
		}

		if existingStore != nil {
			if existingStore.Path != "" && existingStore.Path != storePath {
				if stat, statErr := os.Stat(existingStore.Path); statErr == nil && stat.IsDir() {
					if rmErr := os.RemoveAll(existingStore.Path); rmErr != nil {
						c.Error(errors.NewInternalServerError("failed to clean old store path").WithDetails(rmErr.Error()))
						return
					}
				}
			}
			store.ID = existingStore.ID
		}

		if err := h.storeService.UpdateStore(c.Request.Context(), store); err != nil {
			c.Error(errors.NewInternalServerError("failed to update store").WithDetails(err.Error()))
			return
		}
	} else {
		if err := h.storeService.CreateStore(c.Request.Context(), store); err != nil {
			c.Error(errors.NewInternalServerError("failed to create store").WithDetails(err.Error()))
			return
		}
		script.StoreID = store.ID
	}

	script.URL = req.Url
	script.Version = req.Version
	script.Message = req.Message
	if err := h.workflowService.UpdateScript(c.Request.Context(), script); err != nil {
		c.Error(errors.NewInternalServerError("failed to update script publish info").WithDetails(err.Error()))
		return
	}

	storeScriptJSONPath := filepath.Join(storePath, "script.json")
	storeScriptBytes, err := json.MarshalIndent(exportPayload, "", "  ")
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to encode store script json").WithDetails(err.Error()))
		return
	}
	if err := os.WriteFile(storeScriptJSONPath, storeScriptBytes, 0o644); err != nil {
		c.Error(errors.NewInternalServerError("failed to write store script json").WithDetails(err.Error()))
		return
	}
	project, err := h.projectService.GetProjectByID(c.Request.Context(), script.ProjectID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get project").WithDetails(err.Error()))
		return
	}

	sourceScriptDir := utils.GetScriptFileDir(h.cfg.Storage.BaseDir, project.ProjectID, script.ScriptID)
	// sourceScriptDir := filepath.Join(h.cfg.Storage.BaseDir, "pipeline", "script", script.ScriptID)
	targetScriptDir := filepath.Join(storePath, "script")
	if err := copyDirReplace(sourceScriptDir, targetScriptDir); err != nil {
		c.Error(errors.NewInternalServerError("failed to copy script files").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":    "success",
		"store":      store,
		"script":     script,
		"store_path": storePath,
	})
}

func (h *WorkflowHandler) InstallWorkflow(c *gin.Context) {
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
	if h.cfg == nil || strings.TrimSpace(h.cfg.Storage.BaseDir) == "" {
		c.Error(errors.NewInternalServerError("storage base dir is not configured"))
		return
	}
	storeID := c.Param("storeId")
	if storeID == "" {
		c.Error(errors.NewValidationError("storeId is required"))
		return
	}
	storeIDInt, err := strconv.ParseInt(storeID, 10, 64)
	if err != nil || storeIDInt == 0 {
		c.Error(errors.NewValidationError("storeId must be a valid integer"))
		return
	}

	store, err := h.storeService.GetStoreByID(c.Request.Context(), storeIDInt)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("store not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get store").WithDetails(err.Error()))
		return
	}
	if store == nil || strings.TrimSpace(store.Path) == "" {
		c.Error(errors.NewValidationError("store path is empty"))
		return
	}

	workflowJSONPath, err := resolveStoreWorkflowJSONPath(store.Path)
	if err != nil {
		c.Error(errors.NewNotFoundError("workflow.json not found in store"))
		return
	}

	content, err := os.ReadFile(workflowJSONPath)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to read workflow json").WithDetails(err.Error()))
		return
	}

	payload := &types.WorkflowJSONExportResponse{}
	if err := json.Unmarshal(content, payload); err != nil {
		c.Error(errors.NewInternalServerError("failed to parse workflow json").WithDetails(err.Error()))
		return
	}
	if payload.WorkflowID == "" {
		c.Error(errors.NewValidationError("workflow_id is required in workflow.json"))
		return
	}
	if err := normalizeInstalledWorkflowMap(payload.Workflow); err != nil {
		c.Error(errors.NewValidationError("workflow.json contains invalid workflow fields").WithDetails(err.Error()))
		return
	}

	wfBytes, err := json.Marshal(payload.Workflow)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to decode workflow body").WithDetails(err.Error()))
		return
	}
	installWorkflow := &types.Workflow{}
	if err := json.Unmarshal(wfBytes, installWorkflow); err != nil {
		c.Error(errors.NewInternalServerError("failed to parse workflow body").WithDetails(err.Error()))
		return
	}

	installWorkflow.ID = 0
	installWorkflow.ProjectID = project.ID
	installWorkflow.StoreID = store.ID
	// installWorkflow.WorkflowID = payload.WorkflowID
	if strings.TrimSpace(store.URL) != "" {
		installWorkflow.URL = store.URL
	}
	if strings.TrimSpace(store.Version) != "" {
		installWorkflow.Version = store.Version
	}
	if strings.TrimSpace(store.Message) != "" {
		installWorkflow.Message = store.Message
	}

	existingWorkflow, err := h.workflowService.ExistsWorkflowInProjectByWorkflowID(c.Request.Context(), project.ID, payload.WorkflowID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to check existing workflow").WithDetails(err.Error()))
		return
	}
	if existingWorkflow != nil {
		installWorkflow.ID = existingWorkflow.ID
		if err := h.workflowService.UpdateWorkflow(c.Request.Context(), installWorkflow); err != nil {
			c.Error(errors.NewInternalServerError("failed to update installed workflow").WithDetails(err.Error()))
			return
		}
	} else {

		if err := h.workflowService.CreateWorkflow(c.Request.Context(), installWorkflow); err != nil {
			c.Error(errors.NewInternalServerError("failed to install workflow").WithDetails(err.Error()))
			return
		}
	}

	installedScriptCount := 0
	for _, scriptMap := range payload.Scripts {
		scriptBytes, marshalErr := json.Marshal(scriptMap)
		if marshalErr != nil {
			c.Error(errors.NewInternalServerError("failed to decode script body").WithDetails(marshalErr.Error()))
			return
		}
		installScript := &types.Script{}
		if unmarshalErr := json.Unmarshal(scriptBytes, installScript); unmarshalErr != nil {
			c.Error(errors.NewInternalServerError("failed to parse script body").WithDetails(unmarshalErr.Error()))
			return
		}

		installScript.ID = 0
		installScript.ProjectID = project.ID
		installScript.StoreID = store.ID
		if installScript.ComponentType == "" {
			installScript.ComponentType = "script"
		}

		existingScript, err := h.workflowService.ExistsScriptInProjectByScriptID(c.Request.Context(), project.ID, installScript.ScriptID)
		if err != nil {
			c.Error(errors.NewInternalServerError("failed to check existing script").WithDetails(err.Error()))
			return
		}

		if existingScript != nil {
			installScript.ID = existingScript.ID
			if err := h.workflowService.UpdateScript(c.Request.Context(), installScript); err != nil {
				c.Error(errors.NewInternalServerError("failed to update installed script").WithDetails(err.Error()))
				return
			}
		} else {
			if err := h.workflowService.CreateScript(c.Request.Context(), installScript); err != nil {
				c.Error(errors.NewInternalServerError("failed to install script").WithDetails(err.Error()))
				return
			}
		}

		scriptID := strings.TrimSpace(installScript.ScriptID)
		if scriptID == "" {
			scriptID = scriptIDFromExportScript(scriptMap)
		}
		if scriptID != "" {
			scriptDir := utils.GetScriptDir(h.cfg.Storage.BaseDir, project.ProjectID)
			sourceScriptDir := filepath.Join(store.Path, "script", scriptID)
			targetScriptDir := filepath.Join(scriptDir, scriptID)
			if copyErr := copyDirReplace(sourceScriptDir, targetScriptDir); copyErr != nil {
				c.Error(errors.NewInternalServerError("failed to install script files").WithDetails(copyErr.Error()))
				return
			}
		}

		installedScriptCount++
	}
	workflowDir := utils.GetWorkflowDir(h.cfg.Storage.BaseDir, project.ProjectID)
	storeWorkflowDir := filepath.Dir(workflowJSONPath)
	localWorkflowDir := filepath.Join(workflowDir, payload.WorkflowID)
	if err := copyDirReplace(storeWorkflowDir, localWorkflowDir); err != nil {
		c.Error(errors.NewInternalServerError("failed to install workflow files").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":                "success",
		"workflow_id":            installWorkflow.WorkflowID,
		"installed_workflow_id":  installWorkflow.ID,
		"installed_script_count": installedScriptCount,
	})

}

func (h *WorkflowHandler) InstallScript(c *gin.Context) {
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
	if h.cfg == nil || strings.TrimSpace(h.cfg.Storage.BaseDir) == "" {
		c.Error(errors.NewInternalServerError("storage base dir is not configured"))
		return
	}
	storeID := c.Param("storeId")
	if storeID == "" {
		c.Error(errors.NewValidationError("storeId is required"))
		return
	}
	storeIDInt, err := strconv.ParseInt(storeID, 10, 64)
	if err != nil || storeIDInt == 0 {
		c.Error(errors.NewValidationError("storeId must be a valid integer"))
		return
	}

	store, err := h.storeService.GetStoreByID(c.Request.Context(), storeIDInt)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("store not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to get store").WithDetails(err.Error()))
		return
	}
	if store == nil || strings.TrimSpace(store.Path) == "" {
		c.Error(errors.NewValidationError("store path is empty"))
		return
	}

	scriptJSONPath, err := resolveStoreScriptJSONPath(store.Path)
	if err != nil {
		c.Error(errors.NewNotFoundError("script.json not found in store"))
		return
	}

	content, err := os.ReadFile(scriptJSONPath)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to read script json").WithDetails(err.Error()))
		return
	}

	payload := &types.ScriptJSONExportResponse{}
	if err := json.Unmarshal(content, payload); err != nil {
		c.Error(errors.NewInternalServerError("failed to parse script json").WithDetails(err.Error()))
		return
	}
	if payload.ScriptID == "" {
		c.Error(errors.NewValidationError("script_id is required in script.json"))
		return
	}

	scriptBytes, err := json.Marshal(payload.Script)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to decode script body").WithDetails(err.Error()))
		return
	}
	installScript := &types.Script{}
	if err := json.Unmarshal(scriptBytes, installScript); err != nil {
		c.Error(errors.NewInternalServerError("failed to parse script body").WithDetails(err.Error()))
		return
	}

	installScript.ID = 0
	installScript.ProjectID = project.ID
	installScript.StoreID = store.ID
	if installScript.ComponentType == "" {
		installScript.ComponentType = "script"
	}
	if strings.TrimSpace(store.URL) != "" {
		installScript.InstallKey = store.URL
	}
	if strings.TrimSpace(store.Version) != "" {
		installScript.Version = store.Version
	}
	if strings.TrimSpace(store.Message) != "" {
		installScript.Message = store.Message
	}

	existingScript, err := h.workflowService.ExistsScriptInProjectByScriptID(c.Request.Context(), project.ID, payload.ScriptID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to check existing script").WithDetails(err.Error()))
		return
	}

	if existingScript != nil {
		installScript.ID = existingScript.ID
		if err := h.workflowService.UpdateScript(c.Request.Context(), installScript); err != nil {
			c.Error(errors.NewInternalServerError("failed to update installed script").WithDetails(err.Error()))
			return
		}
	} else {
		if err := h.workflowService.CreateScript(c.Request.Context(), installScript); err != nil {
			c.Error(errors.NewInternalServerError("failed to install script").WithDetails(err.Error()))
			return
		}
	}

	scriptID := strings.TrimSpace(installScript.ScriptID)
	if scriptID == "" {
		scriptID = payload.ScriptID
	}
	if scriptID != "" {
		targetScriptDir := utils.GetScriptFileDir(h.cfg.Storage.BaseDir, project.ProjectID, scriptID)
		sourceScriptDir := filepath.Join(store.Path, "script")
		// targetScriptDir := filepath.Join(scriptDir, scriptID)
		if copyErr := copyDirReplace(sourceScriptDir, targetScriptDir); copyErr != nil {
			c.Error(errors.NewInternalServerError("failed to install script files").WithDetails(copyErr.Error()))
			return
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"message":             "success",
		"script_id":           installScript.ScriptID,
		"installed_script_id": installScript.ID,
	})
}

func resolveStoreWorkflowJSONPath(storePath string) (string, error) {
	storePath = strings.TrimSpace(storePath)
	if storePath == "" {
		return "", fmt.Errorf("store path is empty")
	}

	directPath := filepath.Join(storePath, "workflow.json")
	if stat, err := os.Stat(directPath); err == nil && !stat.IsDir() {
		return directPath, nil
	}

	var found string
	err := filepath.Walk(storePath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info == nil || info.IsDir() {
			return nil
		}
		if strings.EqualFold(info.Name(), "workflow.json") {
			found = path
			return io.EOF
		}
		return nil
	})
	if err != nil && !stderrs.Is(err, io.EOF) {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("workflow.json not found")
	}
	return found, nil
}

func resolveStoreScriptJSONPath(storePath string) (string, error) {
	storePath = strings.TrimSpace(storePath)
	if storePath == "" {
		return "", fmt.Errorf("store path is empty")
	}

	directPath := filepath.Join(storePath, "script.json")
	if stat, err := os.Stat(directPath); err == nil && !stat.IsDir() {
		return directPath, nil
	}

	var found string
	err := filepath.Walk(storePath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info == nil || info.IsDir() {
			return nil
		}
		if strings.EqualFold(info.Name(), "script.json") {
			found = path
			return io.EOF
		}
		return nil
	})
	if err != nil && !stderrs.Is(err, io.EOF) {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("script.json not found")
	}
	return found, nil
}

func buildStorePathNameFromURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("url is empty")
	}

	parts := strings.Split(rawURL, "/")
	cleanParts := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || strings.Contains(p, ":") {
			continue
		}
		cleanParts = append(cleanParts, p)
	}
	if len(cleanParts) < 2 {
		return "", fmt.Errorf("invalid url: %s", rawURL)
	}

	owner := cleanParts[len(cleanParts)-2]
	repo := strings.TrimSuffix(cleanParts[len(cleanParts)-1], ".git")
	if owner == "" || repo == "" {
		return "", fmt.Errorf("invalid url: %s", rawURL)
	}

	return filepath.Join(owner, repo), nil
}

func buildPublishURLsJSON(pathName string) (datatypes.JSON, error) {
	publishURLs := []map[string]string{
		{
			"name":  "github",
			"ssh":   fmt.Sprintf("git@github.com:%s.git", pathName),
			"https": fmt.Sprintf("https://github.com/%s.git", pathName),
		},
		{
			"name":  "gitee",
			"ssh":   fmt.Sprintf("git@gitee.com:%s.git", pathName),
			"https": fmt.Sprintf("https://gitee.com/%s.git", pathName),
		},
	}

	b, err := json.Marshal(publishURLs)
	if err != nil {
		return nil, err
	}
	return datatypes.JSON(b), nil
}

func copyDirReplace(srcDir string, dstDir string) error {
	info, err := os.Stat(srcDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("source is not a directory: %s", srcDir)
	}

	if err := os.RemoveAll(dstDir); err != nil {
		return err
	}
	if err := os.MkdirAll(dstDir, 0o755); err != nil {
		return err
	}

	return filepath.Walk(srcDir, func(path string, fileInfo os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}
		if relPath == "." {
			return nil
		}

		targetPath := filepath.Join(dstDir, relPath)
		if fileInfo.IsDir() {
			return os.MkdirAll(targetPath, 0o755)
		}

		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return err
		}

		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		dstFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fileInfo.Mode())
		if err != nil {
			return err
		}
		defer dstFile.Close()

		_, err = io.Copy(dstFile, srcFile)
		return err
	})
}

func scriptIDFromExportScript(item map[string]any) string {
	if item == nil {
		return ""
	}
	if v, ok := item["component_id"].(string); ok {
		return strings.TrimSpace(v)
	}
	if v, ok := item["script_id"].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func normalizeInstalledWorkflowMap(workflow map[string]any) error {
	if workflow == nil {
		return nil
	}

	dagDefinition, exists := workflow["dag_definition"]
	if !exists || dagDefinition == nil {
		return nil
	}

	switch value := dagDefinition.(type) {
	case string:
		return nil
	default:
		b, err := json.Marshal(value)
		if err != nil {
			return err
		}
		workflow["dag_definition"] = string(b)
		return nil
	}
}
