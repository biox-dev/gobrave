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
	"github.com/biox-dev/gobrave/internal/exportcodec"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// exportCodecForWrite 返回写侧使用的 Codec：写侧总是写当前版本（exportcodec.CurrentVersion）。
func (h *WorkflowHandler) exportCodecForWrite() (exportcodec.Codec, error) {
	return h.exportCodec(exportcodec.CurrentVersion)
}

// exportCodecForFile 按导出文件（script.json / workflow.json）顶层的 version 取读侧 Codec。
//
// version 为空表示 v1 之前的历史产物（没有 version 字段），按当前版本读回，
// 这样老 store 的内容不需重新发布也能安装；解析不出 JSON 或版本未注册都返回错误，
// 由调用方决定转 400（未注册 → exportcodec.ErrUnsupportedVersion）还是 500。
func (h *WorkflowHandler) exportCodecForFile(raw []byte) (exportcodec.Codec, error) {
	version, err := exportcodec.PeekVersion(raw)
	if err != nil {
		return nil, err
	}
	if version == "" {
		version = exportcodec.CurrentVersion
	}
	return h.exportCodec(version)
}

// exportCodec 从注入的 Registry 取版本对应的 Codec。
//
// 未注册（含 Registry 未装配、版本号不认识）一律视为「不支持该文件版本」，
// 让调用方统一转 400 而不是把未知版本当成功解析。
func (h *WorkflowHandler) exportCodec(version string) (exportcodec.Codec, error) {
	if h.exportCodecs == nil {
		return nil, fmt.Errorf("export codec registry is not configured")
	}
	codec := h.exportCodecs.Get(version)
	if codec == nil {
		return nil, fmt.Errorf("%w: %s", exportcodec.ErrUnsupportedVersion, version)
	}
	return codec, nil
}

// writeScriptJSONAndCommit 生成 script.json 写入脚本目录，并确保脚本目录是一个 git 仓库、
// 把当前工作区改动提交为一个 commit（工作区无变更时不会产生空提交）。
//
// SaveScript 与 PublishScript 共用：保存时落盘并提交；发布时在 push 前再跑一次，
// 保证 sourceScriptDir 仓库存在、工作区内容已提交且 script.json 一定存在。
//
// 文件名、内容与目录布局由导出格式 Codec 决定（见 exportCodecForWrite / exportcodec/v1）；
// git 提交与格式版本无关，故留在 handler 这层复用。
//
// scriptPK 是 script 表主键（int64），scriptDir 是脚本目录绝对路径。
func (h *WorkflowHandler) writeScriptJSONAndCommit(ctx context.Context, scriptPK int64, scriptDir, commitMessage string) error {
	codec, err := h.exportCodecForWrite()
	if err != nil {
		return err
	}
	if _, err := codec.WriteScriptFiles(ctx, exportcodec.ScriptWriteRequest{
		ScriptPK:  scriptPK,
		ScriptDir: scriptDir,
	}); err != nil {
		return err
	}
	return h.commitDirChanges(scriptDir, commitMessage)
}

// writeWorkflowJSONAndCommit 按导出格式 Codec 生成 workflow.json 写入 workflow 目录，
// 同时按该版本的目录布局把 workflow 引用的脚本快照到 workflow 目录内
// （v1 为 <workflowDir>/script/<scriptID>，排除脚本目录自身的 .git），
// 最后确保 workflow 目录是一个 git 仓库并把当前工作区改动提交为一个 commit。
//
// SaveWorkflow 与 PublishWorkflow 共用：保存时落盘并提交；发布时在 push 前再跑一次，
// 保证 sourceWorkflowDir 仓库存在、工作区内容已提交且 workflow.json / 脚本文件都在。
//
// workflowPK 是 workflow 表主键（int64），projectID 是 project.project_id（字符串）。
func (h *WorkflowHandler) writeWorkflowJSONAndCommit(ctx context.Context, workflowPK int64, projectID, workflowDir, commitMessage string) error {
	baseDir := h.storageBaseDir()
	if baseDir == "" {
		return stderrs.New("storage base dir is not configured")
	}

	codec, err := h.exportCodecForWrite()
	if err != nil {
		return err
	}
	if _, err := codec.WriteWorkflowFiles(ctx, exportcodec.WorkflowWriteRequest{
		WorkflowPK:  workflowPK,
		ProjectID:   projectID,
		BaseDir:     baseDir,
		WorkflowDir: workflowDir,
	}); err != nil {
		return err
	}
	return h.commitDirChanges(workflowDir, commitMessage)
}

// commitDirChanges 确保 dir 是一个 git 仓库（不存在则初始化，默认分支 main），
// 并把当前工作区改动提交为一个 commit（无变更时不会产生空提交）。
//
// 与导出格式版本无关（任何版本的产物都只是目录里的文件），故由两个 writeXxxAndCommit 共用。
func (h *WorkflowHandler) commitDirChanges(dir, commitMessage string) error {
	repo, err := utils.EnsureGitRepo(dir)
	if err != nil {
		return fmt.Errorf("failed to init git repository %s: %w", dir, err)
	}
	gitUser, gitEmail := config.ResolveGitIdentity(h.cfg)
	if _, err := utils.CommitAll(repo, commitMessage, utils.GitIdentity{Name: gitUser, Email: gitEmail}); err != nil {
		return fmt.Errorf("failed to commit changes in %s: %w", dir, err)
	}
	return nil
}

type PublishWorkflowRequest struct {
	WorkflowID int64  `json:"workflow_id,string"`
	Url        string `json:"url"`
	// Version    string `json:"version"`
	Message string `json:"message"`
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
		// Version:     req.Version,
		Message: req.Message,
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
	// workflow.Version = req.Version
	workflow.Message = req.Message
	if err := h.workflowService.UpdateWorkflow(c.Request.Context(), workflow); err != nil {
		c.Error(errors.NewInternalServerError("failed to update workflow publish info").WithDetails(err.Error()))
		return
	}

	// workflow 目录是发布的数据源：workflow.json 与脚本目录快照由 writeWorkflowJSONAndCommit
	// 统一生成并提交，再 push 到 store 裸仓库。
	workflowSourceDir := utils.GetWorkflowFileDir(h.cfg.Storage.BaseDir, project.ProjectID, workflow.WorkflowID)
	if err := h.writeWorkflowJSONAndCommit(c.Request.Context(), workflow.ID, project.ProjectID, workflowSourceDir, fmt.Sprintf("publish workflow %s", workflow.WorkflowID)); err != nil {
		if stderrs.Is(err, interfaces.ErrInvalidDagDefinitionJSON) {
			c.Error(errors.NewValidationError("dag_definition is not valid JSON format"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to prepare workflow files for publish").WithDetails(err.Error()))
		return
	}

	// store 目录是一个本地裸仓库，workflow 目录作为 origin remote push 到它。
	if _, repoErr := utils.EnsureBareGitRepo(storePath); repoErr != nil {
		c.Error(errors.NewInternalServerError("failed to prepare store git repository").WithDetails(repoErr.Error()))
		return
	}
	if pushErr := utils.PushDirToRepo(c.Request.Context(), workflowSourceDir, storePath); pushErr != nil {
		c.Error(errors.NewInternalServerError("failed to push workflow to store").WithDetails(pushErr.Error()))
		return
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
	// Version  string `json:"version"`
	Message string `json:"message"`
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
		// Version:     req.Version,
		Message: req.Message,
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
	// script.Version = req.Version
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

	// 目标 workflow_id：本地发布时 store.PathName 即 workflow_id。
	workflowID := strings.TrimSpace(store.PathName)
	if workflowID == "" || strings.Contains(workflowID, "/") {
		// 远程下载的 store：PathName 是 <owner>/<repo>，真实 workflow_id 需要从 store 内的 workflow.json 读取。
		workflowID = h.readWorkflowIDFromStoreDir(storeDir)
	}
	if workflowID == "" {
		c.Error(errors.NewValidationError("workflow_id is required in workflow.json"))
		return
	}

	targetWorkflowDir := utils.GetWorkflowFileDir(h.cfg.Storage.BaseDir, project.ProjectID, workflowID)

	// store 是裸仓库：目标 workflow 目录已有仓库则 pull（fetch + reset --hard）覆盖本地，否则 clone 出工作区。
	if syncErr := utils.SyncWorktreeFromRepo(c.Request.Context(), targetWorkflowDir, storeDir); syncErr != nil {
		c.Error(errors.NewInternalServerError("failed to sync workflow files from store").WithDetails(syncErr.Error()))
		return
	}

	// 从同步后的 workflow 目录读取 workflow.json 导入数据库。
	// 文件顶层 version 决定用哪套 Codec 解析（见 readWorkflowJSONFromDir）；
	// 同一个 Codec 也用来解释该版本的目录布局（脚本快照目录）。
	codec, payload, readErr := h.readWorkflowJSONFromDir(targetWorkflowDir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			c.Error(errors.NewNotFoundError("workflow.json not found in store"))
			return
		}
		if stderrs.Is(readErr, exportcodec.ErrUnsupportedVersion) {
			c.Error(errors.NewValidationError("unsupported workflow.json version").WithDetails(readErr.Error()))
			return
		}
		c.Error(errors.NewInternalServerError("failed to read workflow json").WithDetails(readErr.Error()))
		return
	}
	if payload.WorkflowID == "" {
		c.Error(errors.NewValidationError("workflow_id is required in workflow.json"))
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
	// if strings.TrimSpace(store.Version) != "" {
	// 	installWorkflow.Version = store.Version
	// }
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
			scriptID = codec.ScriptIDFromExportScript(scriptMap)
		}
		if scriptID != "" {
			// 脚本文件随 workflow 目录一起从 store 同步到该版本约定的快照目录
			// （v1 为 <workflowDir>/script/<scriptID>），这里再原样还原到脚本目录。
			sourceScriptDir := codec.ScriptSnapshotDir(targetWorkflowDir, scriptID)
			targetScriptDir := utils.GetScriptFileDir(h.cfg.Storage.BaseDir, project.ProjectID, scriptID)
			if copyErr := utils.CopyDirReplace(sourceScriptDir, targetScriptDir); copyErr != nil {
				c.Error(errors.NewInternalServerError("failed to install script files").WithDetails(copyErr.Error()))
				return
			}
		}

		installedScriptCount++
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
		scriptID = h.readScriptIDFromStoreDir(storeDir)
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
	// 文件顶层 version 决定用哪套 Codec 解析（见 readScriptJSONFromDir）。
	_, payload, readErr := h.readScriptJSONFromDir(targetScriptDir)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			c.Error(errors.NewNotFoundError("script.json not found in store"))
			return
		}
		if stderrs.Is(readErr, exportcodec.ErrUnsupportedVersion) {
			c.Error(errors.NewValidationError("unsupported script.json version").WithDetails(readErr.Error()))
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
	// if strings.TrimSpace(store.Version) != "" {
	// 	installScript.Version = store.Version
	// }
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
// 该文件可能存在任意层级（resolveStoreScriptJSONPath 按根目录优先查找），
// 与 InstallScript 直接读目标脚本目录不同，故单独实现。
func (h *WorkflowHandler) readScriptIDFromStoreDir(storeDir string) string {
	scriptJSONPath, err := resolveStoreScriptJSONPath(storeDir)
	if err != nil {
		return ""
	}
	content, err := os.ReadFile(scriptJSONPath)
	if err != nil {
		return ""
	}
	codec, err := h.exportCodecForFile(content)
	if err != nil {
		return ""
	}
	payload, err := codec.DecodeScript(content)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(payload.ScriptID)
}

// readScriptJSONFromDir 读取脚本目录下的 script.json，按文件顶层 version 从 Registry 取 Codec 解析。
//
// 把命中的 Codec 一并返回：安装侧还要用它解释该版本的目录布局。
func (h *WorkflowHandler) readScriptJSONFromDir(scriptDir string) (exportcodec.Codec, *types.ScriptJSONExportResponse, error) {
	content, err := os.ReadFile(filepath.Join(scriptDir, exportcodec.ScriptJSONFileName))
	if err != nil {
		return nil, nil, err
	}
	codec, err := h.exportCodecForFile(content)
	if err != nil {
		return nil, nil, err
	}
	payload, err := codec.DecodeScript(content)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid %s: %w", exportcodec.ScriptJSONFileName, err)
	}
	return codec, payload, nil
}

// readWorkflowIDFromStoreDir 读取 store 目录内 workflow.json 的 workflow_id。
//
// 仅用于远程下载的 store（普通工作区仓库）；本地发布的 store 是裸仓库，没有工作区文件，
// 其 workflow_id 直接取自 store.PathName。
func (h *WorkflowHandler) readWorkflowIDFromStoreDir(storeDir string) string {
	workflowJSONPath, err := resolveStoreWorkflowJSONPath(storeDir)
	if err != nil {
		return ""
	}
	content, err := os.ReadFile(workflowJSONPath)
	if err != nil {
		return ""
	}
	codec, err := h.exportCodecForFile(content)
	if err != nil {
		return ""
	}
	payload, err := codec.DecodeWorkflow(content)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(payload.WorkflowID)
}

// readWorkflowJSONFromDir 读取 workflow 目录下的 workflow.json，按文件顶层 version 从 Registry 取 Codec 解析。
//
// 把命中的 Codec 一并返回：安装侧还要用它解释该版本的目录布局（脚本快照目录）。
func (h *WorkflowHandler) readWorkflowJSONFromDir(workflowDir string) (exportcodec.Codec, *types.WorkflowJSONExportResponse, error) {
	content, err := os.ReadFile(filepath.Join(workflowDir, exportcodec.WorkflowJSONFileName))
	if err != nil {
		return nil, nil, err
	}
	codec, err := h.exportCodecForFile(content)
	if err != nil {
		return nil, nil, err
	}
	payload, err := codec.DecodeWorkflow(content)
	if err != nil {
		return nil, nil, fmt.Errorf("invalid %s: %w", exportcodec.WorkflowJSONFileName, err)
	}
	return codec, payload, nil
}

func resolveStoreWorkflowJSONPath(storePath string) (string, error) {
	storePath = strings.TrimSpace(storePath)
	if storePath == "" {
		return "", fmt.Errorf("store path is empty")
	}

	directPath := filepath.Join(storePath, exportcodec.WorkflowJSONFileName)
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
		if strings.EqualFold(info.Name(), exportcodec.WorkflowJSONFileName) {
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

	directPath := filepath.Join(storePath, exportcodec.ScriptJSONFileName)
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
		if strings.EqualFold(info.Name(), exportcodec.ScriptJSONFileName) {
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
