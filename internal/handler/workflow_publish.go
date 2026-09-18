package handler

import (
	"context"
	"encoding/json"
	stderrs "errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// scriptJSONFileName 是脚本导出文件名：SaveScript 写入脚本目录并纳入 git 提交，
// PublishScript 随脚本目录一起推送到 store 裸仓库，InstallScript 同步回脚本目录后
// 再读取该文件导入数据库。
const scriptJSONFileName = "script.json"

// writeScriptJSONAndCommit 生成 script.json 写入脚本目录，并确保脚本目录是一个 git 仓库、
// 把当前工作区改动提交为一个 commit（工作区无变更时不会产生空提交）。
//
// SaveScript 与 PublishScript 共用：保存时落盘并提交；发布时在 push 前再跑一次，
// 保证 sourceScriptDir 仓库存在、工作区内容已提交且 script.json 一定存在。
//
// scriptPK 是 script 表主键（int64），scriptDir 是脚本目录绝对路径。
func (h *WorkflowHandler) writeScriptJSONAndCommit(ctx context.Context, scriptPK int64, scriptDir, commitMessage string) error {
	exportPayload, err := h.workflowService.GenerateScriptJSONByScriptID(ctx, scriptPK)
	if err != nil {
		return fmt.Errorf("failed to generate script json: %w", err)
	}
	scriptJSONBytes, err := json.MarshalIndent(exportPayload, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode script json: %w", err)
	}
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		return fmt.Errorf("failed to prepare script directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, scriptJSONFileName), scriptJSONBytes, 0o644); err != nil {
		return fmt.Errorf("failed to write script json: %w", err)
	}

	// 维护脚本目录的 git 版本仓库：不存在则初始化（默认分支 main），已存在则复用。
	repo, err := utils.EnsureGitRepo(scriptDir)
	if err != nil {
		return fmt.Errorf("failed to init script git repository: %w", err)
	}
	gitUser, gitEmail := config.ResolveGitIdentity(h.cfg)
	if _, err := utils.CommitAll(repo, commitMessage, utils.GitIdentity{Name: gitUser, Email: gitEmail}); err != nil {
		return fmt.Errorf("failed to commit script changes: %w", err)
	}
	return nil
}

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
	project, err := h.projectService.GetProjectByID(c.Request.Context(), workflow.ProjectID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get project").WithDetails(err.Error()))
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

	// pathName, err := buildStorePathNameFromURL(req.Url)
	// if err != nil {
	// 	pathName = workflow.WorkflowID
	// }
	storePath := utils.GetWorkflowOrScriptStoreDir(h.cfg.Storage.BaseDir, workflow.WorkflowID) //filepath.Join(h.cfg.Storage.BaseDir, "store", workflow.WorkflowID)

	publishURLsJSON, err := buildPublishURLsJSON(workflow.WorkflowID)
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
		PathName:    workflow.WorkflowID,
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
			// 旧目录由 PathName 推导；PathName 变化时清理遗留目录，
			// 避免 base_dir/store 下残留旧产物。
			if oldPathName := strings.TrimSpace(existingStore.PathName); oldPathName != "" && oldPathName != strings.TrimSpace(store.PathName) {
				oldStorePath := utils.GetWorkflowOrScriptStoreDir(h.cfg.Storage.BaseDir, oldPathName)
				if stat, statErr := os.Stat(oldStorePath); statErr == nil && stat.IsDir() {
					if rmErr := os.RemoveAll(oldStorePath); rmErr != nil {
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

	// workflowSourceDir := filepath.Join(h.cfg.Storage.BaseDir, "pipeline", "tools", workflow.WorkflowID)
	workflowSourceDir := utils.GetWorkflowFileDir(h.cfg.Storage.BaseDir, project.ProjectID, workflow.WorkflowID)
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
		sourceScriptDir := utils.GetScriptFileDir(h.cfg.Storage.BaseDir, project.ProjectID, scriptID)

		// sourceScriptDir := filepath.Join(h.cfg.Storage.BaseDir, "pipeline", "script", scriptID)
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

	project, err := h.projectService.GetProjectByID(c.Request.Context(), script.ProjectID)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to get project").WithDetails(err.Error()))
		return
	}

	// 脚本目录是发布的数据源：script.json 与 git 提交由 writeScriptJSONAndCommit
	// 在 push 前统一生成，这里不再校验文件是否存在。
	sourceScriptDir := utils.GetScriptFileDir(h.cfg.Storage.BaseDir, project.ProjectID, script.ScriptID)

	storePath := utils.GetWorkflowOrScriptStoreDir(h.cfg.Storage.BaseDir, script.ScriptID) //filepath.Join(h.cfg.Storage.BaseDir, "store", script.ScriptID)

	publishURLsJSON, err := buildPublishURLsJSON(script.ScriptID)
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
		PathName:    script.ScriptID,
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
			// 旧目录由 PathName 推导；PathName 变化时清理遗留目录。
			if oldPathName := strings.TrimSpace(existingStore.PathName); oldPathName != "" && oldPathName != strings.TrimSpace(store.PathName) {
				oldStorePath := utils.GetWorkflowOrScriptStoreDir(h.cfg.Storage.BaseDir, oldPathName)
				if stat, statErr := os.Stat(oldStorePath); statErr == nil && stat.IsDir() {
					if rmErr := os.RemoveAll(oldStorePath); rmErr != nil {
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

	// 每次 push 前重新生成 script.json 并提交脚本目录改动，
	// 确保 sourceScriptDir 仓库存在、工作区内容已提交且包含最新 script.json。
	if err := h.writeScriptJSONAndCommit(c.Request.Context(), script.ID, sourceScriptDir, fmt.Sprintf("publish script %s", script.ScriptID)); err != nil {
		c.Error(errors.NewInternalServerError("failed to prepare script files for publish").WithDetails(err.Error()))
		return
	}

	// store 目录是一个本地裸仓库，脚本目录作为 origin remote push 到它（等价 git push 到本地仓库）。
	if _, repoErr := utils.EnsureBareGitRepo(storePath); repoErr != nil {
		c.Error(errors.NewInternalServerError("failed to prepare store git repository").WithDetails(repoErr.Error()))
		return
	}
	if pushErr := utils.PushDirToRepo(c.Request.Context(), sourceScriptDir, storePath); pushErr != nil {
		c.Error(errors.NewInternalServerError("failed to push script to store").WithDetails(pushErr.Error()))
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
	storeDir := resolveStoreDir(h.cfg, store)
	if storeDir == "" {
		c.Error(errors.NewValidationError("store path is empty"))
		return
	}

	workflowJSONPath, err := resolveStoreWorkflowJSONPath(storeDir)
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
	// 修改创建时间为当前时间，避免覆盖原有的创建时间
	installWorkflow.CreatedAt = utils.GetCurrentTime()
	installWorkflow.UpdatedAt = utils.GetCurrentTime()

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
			sourceScriptDir := filepath.Join(storeDir, "script", scriptID)
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
	createMode := strings.EqualFold(strings.TrimSpace(c.Query("create")), "true")
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
	storeDir := resolveStoreDir(h.cfg, store)
	if storeDir == "" {
		c.Error(errors.NewValidationError("store path is empty"))
		return
	}

	// 目标 script_id：create=true 时安装为新脚本，使用新的 uuid；否则沿用 store 记录的
	// script_id（本地发布时 store.PathName 即 script_id）。
	scriptID := strings.TrimSpace(store.PathName)
	if createMode {
		scriptID = uuid.NewString()
	} else if scriptID == "" || strings.Contains(scriptID, "/") {
		// 远程下载的 store：PathName 是 <owner>/<repo>，真实 script_id 需要从 store 内的 script.json 读取。
		scriptID = readScriptIDFromStoreDir(storeDir)
	}
	if scriptID == "" {
		c.Error(errors.NewValidationError("script_id is required in script.json"))
		return
	}

	targetScriptDir := utils.GetScriptFileDir(h.cfg.Storage.BaseDir, project.ProjectID, scriptID)

	// store 是裸仓库：目标脚本目录已有仓库则 pull（fetch + reset --hard）覆盖本地，否则 clone 出工作区。
	if syncErr := utils.SyncWorktreeFromRepo(c.Request.Context(), targetScriptDir, storeDir); syncErr != nil {
		c.Error(errors.NewInternalServerError("failed to sync script files from store").WithDetails(syncErr.Error()))
		return
	}

	// 从同步后的脚本目录读取 script.json 导入数据库。
	payload, readErr := readScriptJSONFromDir(targetScriptDir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			c.Error(errors.NewNotFoundError("script.json not found in store"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to read script json").WithDetails(readErr.Error()))
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
	installScript.ScriptID = scriptID
	installScript.ProjectID = project.ID
	installScript.StoreID = store.ID
	if installScript.ComponentType == "" {
		installScript.ComponentType = "script"
	}
	if strings.TrimSpace(store.URL) != "" {
		installScript.URL = store.URL
	}
	if strings.TrimSpace(store.Version) != "" {
		installScript.Version = store.Version
	}
	if strings.TrimSpace(store.Message) != "" {
		installScript.Message = store.Message
	}
	installScript.CreatedAt = utils.GetCurrentTime()
	installScript.UpdatedAt = utils.GetCurrentTime()

	if createMode {
		installScript.ComponentName = fmt.Sprintf("%s_Copy", installScript.ComponentName)
		installScript.StoreID = 0
		if err := h.workflowService.CreateScript(c.Request.Context(), installScript); err != nil {
			c.Error(errors.NewInternalServerError("failed to install script").WithDetails(err.Error()))
			return
		}
	} else {
		existingScript, err := h.workflowService.ExistsScriptInProjectByScriptID(c.Request.Context(), project.ID, scriptID)
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
	}

	c.JSON(http.StatusOK, gin.H{
		"message":             "success",
		"script_id":           installScript.ScriptID,
		"installed_script_id": installScript.ID,
	})
}

// readScriptIDFromStoreDir 读取 store 目录内 script.json 的 script_id。
//
// 仅用于远程下载的 store（普通工作区仓库）；本地发布的 store 是裸仓库，没有工作区文件，
// 其 script_id 直接取自 store.PathName。
func readScriptIDFromStoreDir(storeDir string) string {
	scriptJSONPath, err := resolveStoreScriptJSONPath(storeDir)
	if err != nil {
		return ""
	}
	content, err := os.ReadFile(scriptJSONPath)
	if err != nil {
		return ""
	}
	payload := &types.ScriptJSONExportResponse{}
	if err := json.Unmarshal(content, payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.ScriptID)
}

// readScriptJSONFromDir 读取脚本目录下的 script.json 并解析为导出结构。
func readScriptJSONFromDir(scriptDir string) (*types.ScriptJSONExportResponse, error) {
	content, err := os.ReadFile(filepath.Join(scriptDir, scriptJSONFileName))
	if err != nil {
		return nil, err
	}
	payload := &types.ScriptJSONExportResponse{}
	if err := json.Unmarshal(content, payload); err != nil {
		return nil, err
	}
	return payload, nil
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

	directPath := filepath.Join(storePath, scriptJSONFileName)
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
		if strings.EqualFold(info.Name(), scriptJSONFileName) {
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

// func buildStorePathNameFromURL(rawURL string) (string, error) {
// 	rawURL = strings.TrimSpace(rawURL)
// 	if rawURL == "" {
// 		return "", fmt.Errorf("url is empty")
// 	}

// 	parts := strings.Split(rawURL, "/")
// 	cleanParts := make([]string, 0, len(parts))
// 	for _, p := range parts {
// 		p = strings.TrimSpace(p)
// 		if p == "" || strings.Contains(p, ":") {
// 			continue
// 		}
// 		cleanParts = append(cleanParts, p)
// 	}
// 	if len(cleanParts) < 2 {
// 		return "", fmt.Errorf("invalid url: %s", rawURL)
// 	}

// 	owner := cleanParts[len(cleanParts)-2]
// 	repo := strings.TrimSuffix(cleanParts[len(cleanParts)-1], ".git")
// 	if owner == "" || repo == "" {
// 		return "", fmt.Errorf("invalid url: %s", rawURL)
// 	}

// 	return filepath.Join(owner, repo), nil
// }

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
