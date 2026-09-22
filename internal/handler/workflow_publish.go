package handler

import (
	"context"
	"encoding/json"
	stderrs "errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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

// resolveStorePathNameForPublish 决定本次发布使用的 store 目录标识（types.Store.PathName）。
//
// PathName 只在首次发布（storeID == 0）时由 utils.GenerateStorePathName 生成的随机串决定；
// 重新发布一律沿用既有 store 记录的 PathName，保证 store 目录（裸仓库位置）不漂移。
// 返回的 existingStore 为 nil 表示还没有 store 记录（走创建分支）。
// 历史数据的 PathName 可能仍是旧规则（workflow_id / script_id / "<owner>/<repo>"），
// 一律原样沿用，不做改写。
func (h *WorkflowHandler) resolveStorePathNameForPublish(ctx context.Context, storeID int64) (*types.Store, string, error) {
	if storeID == 0 {
		pathName, err := utils.GenerateStorePathName()
		if err != nil {
			return nil, "", err
		}
		return nil, pathName, nil
	}

	existingStore, err := h.storeService.GetStoreByID(ctx, storeID)
	if err != nil {
		return nil, "", err
	}
	if existingStore == nil {
		return nil, "", gorm.ErrRecordNotFound
	}

	pathName := strings.TrimSpace(existingStore.PathName)
	if pathName == "" {
		// 极端情况：历史行没有 PathName（旧版本落库缺失）。目录名缺了就无法定位裸仓库，
		// 这里补一个随机值，下次更新起就固定下来了。
		pathName, err = utils.GenerateStorePathName()
		if err != nil {
			return nil, "", err
		}
	}
	return existingStore, pathName, nil
}

type PublishWorkflowRequest struct {
	WorkflowID int64 `json:"workflow_id,string"`
	// Url        string `json:"url"`
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

	// store 目录名（PathName）与业务 ID 解耦：首次发布生成随机标识，重新发布沿用既有值，
	// 这样目录名不会随 workflow_id 变化而漂移。
	existingStore, pathName, err := h.resolveStorePathNameForPublish(c.Request.Context(), workflow.StoreID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("store not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to prepare store path name").WithDetails(err.Error()))
		return
	}
	storePath := utils.GetWorkflowOrScriptStoreDir(h.cfg.Storage.BaseDir, pathName)

	// publishURLsJSON, err := buildPublishURLsJSON(workflow.WorkflowID)
	// if err != nil {
	// 	c.Error(errors.NewInternalServerError("failed to build publish urls").WithDetails(err.Error()))
	// 	return
	// }

	store := &types.Store{
		// StoreID:     workflow.WorkflowID,
		StoreType: "workflow",
		Name:      workflow.Name,
		Origin:    "local",
		// URL:         req.Url,
		// Status:   "done",
		PathName: pathName,
		Category: workflow.Category,
		Tags:     workflow.Tags,
		Img:      workflow.Img,
		// PublishURLs: publishURLsJSON,
		// Version:     req.Version,
		// Message: req.Message,
	}

	if err := os.MkdirAll(storePath, 0o755); err != nil {
		c.Error(errors.NewInternalServerError("failed to create store path").WithDetails(err.Error()))
		return
	}

	if existingStore != nil {
		// 历史数据的 PathName 可能还是旧规则（workflow_id）；目录名一旦变化就清掉遗留目录，
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

	// workflow.URL = req.Url
	// workflow.Version = req.Version
	// workflow.Message = req.Message
	if err := h.workflowService.UpdateWorkflow(c.Request.Context(), workflow); err != nil {
		c.Error(errors.NewInternalServerError("failed to update workflow publish info").WithDetails(err.Error()))
		return
	}

	// workflow 目录是发布的数据源：workflow.json 与脚本目录快照由 Codec.WriteCommitWorkflowFiles
	// 统一生成、落盘并提交，再 push 到 store 裸仓库。
	workflowSourceDir := utils.GetWorkflowFileDir(h.cfg.Storage.BaseDir, project.ProjectID, workflow.WorkflowID)
	codec, err := h.exportCodecForWrite()
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to resolve export codec").WithDetails(err.Error()))
		return
	}
	if req.Message == "" {
		req.Message = fmt.Sprintf("publish workflow %s", workflow.WorkflowID)
	}
	if _, err := codec.WriteCommitWorkflowFiles(c.Request.Context(), exportcodec.WorkflowWriteRequest{
		WorkflowPK:    workflow.ID,
		ProjectID:     project.ProjectID,
		BaseDir:       h.storageBaseDir(),
		WorkflowDir:   workflowSourceDir,
		CommitMessage: req.Message,
	}); err != nil {
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
	ScriptID int64 `json:"script_id,string"`
	// Url      string `json:"url"`
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

	// 脚本目录是发布的数据源：script.json 与 git 提交由 Codec.WriteCommitScriptFiles
	// 在 push 前统一生成，这里不再校验文件是否存在。
	sourceScriptDir := utils.GetScriptFileDir(h.cfg.Storage.BaseDir, project.ProjectID, script.ScriptID)

	// store 目录名（PathName）与业务 ID 解耦：首次发布生成随机标识，重新发布沿用既有值，
	// 这样目录名不会随 script_id 变化而漂移。
	existingStore, pathName, err := h.resolveStorePathNameForPublish(c.Request.Context(), script.StoreID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("store not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to prepare store path name").WithDetails(err.Error()))
		return
	}
	storePath := utils.GetWorkflowOrScriptStoreDir(h.cfg.Storage.BaseDir, pathName)

	// publishURLsJSON, err := buildPublishURLsJSON(script.ScriptID)
	// if err != nil {
	// 	c.Error(errors.NewInternalServerError("failed to build publish urls").WithDetails(err.Error()))
	// 	return
	// }

	store := &types.Store{
		StoreType: "script",
		Name:      script.ComponentName,
		Origin:    "local",
		// URL:         req.Url,
		// Status:   "done",
		PathName: pathName,
		Category: script.Category,
		Tags:     nil,
		Img:      script.Img,
		// PublishURLs: publishURLsJSON,
		// Version:     req.Version,
		// Message: req.Message,
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

	if existingStore != nil {
		// 历史数据的 PathName 可能还是旧规则（script_id）；目录名一旦变化就清掉遗留目录。
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

	// script.URL = req.Url
	// script.Version = req.Version
	// script.Message = req.Message
	if err := h.workflowService.UpdateScript(c.Request.Context(), script); err != nil {
		c.Error(errors.NewInternalServerError("failed to update script publish info").WithDetails(err.Error()))
		return
	}

	// 每次 push 前重新生成 script.json 并提交脚本目录改动，
	// 确保 sourceScriptDir 仓库存在、工作区内容已提交且包含最新 script.json。
	codec, err := h.exportCodecForWrite()
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to resolve export codec").WithDetails(err.Error()))
		return
	}
	if req.Message == "" {
		req.Message = fmt.Sprintf("publish script %s", script.ScriptID)
	}
	if _, err := codec.WriteCommitScriptFiles(c.Request.Context(), exportcodec.ScriptWriteRequest{
		ScriptPK:      script.ID,
		ScriptDir:     sourceScriptDir,
		CommitMessage: req.Message,
	}); err != nil {
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

	// 目标 workflow_id 一律从 store 内的 workflow.json 读取：store.PathName 只是随机目录名
	// （见 utils.GenerateStorePathName），不再承载 workflow_id。
	workflowID := h.readWorkflowIDFromStoreDir(storeDir)
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

	// 从同步后的 workflow 目录读取 workflow.json 原始内容，按文件顶层 version 取读侧 Codec。
	// 解析、容器资产与 workflow/script 落库、脚本快照还原都在 Codec.InstallWorkflow 内完成
	// （它内部调用 DecodeWorkflow），这样安装逻辑与格式版本绑定在一起。
	codec, raw, readErr := h.readWorkflowRawFromDir(targetWorkflowDir)
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

	result, installErr := codec.InstallWorkflow(c.Request.Context(), exportcodec.WorkflowInstallRequest{
		Raw:         raw,
		ProjectID:   project.ID,
		ProjectCode: project.ProjectID,
		StoreID:     store.ID,
		// StoreMessage: store.Message,
		BaseDir:     h.storageBaseDir(),
		WorkflowDir: targetWorkflowDir,
	})
	if installErr != nil {
		if stderrs.Is(installErr, exportcodec.ErrWorkflowIDRequired) {
			c.Error(errors.NewValidationError("workflow_id is required in workflow.json"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to install workflow").WithDetails(installErr.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":                                       "success",
		"workflow_id":                                   result.WorkflowID,
		"installed_workflow_id":                         result.InstalledWorkflowID,
		"installed_script_count":                        result.InstalledScriptCount,
		"installed_container_image_count":               result.ContainerImageCount,
		"installed_container_template_spec_count":       result.ContainerTemplateSpecCount,
		"installed_container_template_definition_count": result.ContainerTemplateDefinitionCount,
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

	// 目标 script_id：create=true 时安装为新脚本，使用新的 uuid；否则从 store 内的
	// script.json 读取（store.PathName 只是随机目录名，不再承载 script_id）。
	scriptID := ""
	if createMode {
		scriptID = uuid.NewString()
	} else {
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

	// 从同步后的脚本目录读取 script.json 原始内容，按文件顶层 version 取读侧 Codec。
	// 解析与落库（容器资产 + script 行）都在 Codec.InstallScript 内完成（它内部调用 DecodeScript）。
	codec, raw, readErr := h.readScriptRawFromDir(targetScriptDir)
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

	result, installErr := codec.InstallScript(c.Request.Context(), exportcodec.ScriptInstallRequest{
		Raw:       raw,
		ProjectID: project.ID,
		StoreID:   store.ID,
		// StoreMessage: store.Message,
		ScriptID:   scriptID,
		CreateMode: createMode,
	})
	if installErr != nil {
		if stderrs.Is(installErr, exportcodec.ErrScriptIDRequired) {
			c.Error(errors.NewValidationError("script_id is required in script.json"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to install script").WithDetails(installErr.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"message":                         "success",
		"script_id":                       result.ScriptID,
		"installed_script_id":             result.InstalledScriptID,
		"installed_container_image_count": result.ContainerImageCount,
		"installed_container_template_spec_count":       result.ContainerTemplateSpecCount,
		"installed_container_template_definition_count": result.ContainerTemplateDefinitionCount,
	})
}

// readScriptIDFromStoreDir 读取 store 仓库内 script.json 的 script_id。
//
// store.PathName 只是随机目录名，不承载 script_id，因此所有 store（本地发布与远程下载）
// 的真实 script_id 都从这里读取；store 是裸仓库，没有工作区文件，内容从 git 对象里读
// （见 readStoreExportJSON）。
func (h *WorkflowHandler) readScriptIDFromStoreDir(storeDir string) string {
	content, err := readStoreExportJSON(storeDir, exportcodec.ScriptJSONFileName)
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

// readScriptRawFromDir 读取脚本目录下的 script.json 原始内容，并按文件顶层 version 取读侧 Codec。
//
// 只读取不解析：解析与落库由 Codec.InstallScript 完成（它内部调用 DecodeScript），
// 避免同一次安装把文件解析两遍。
func (h *WorkflowHandler) readScriptRawFromDir(scriptDir string) (exportcodec.Codec, []byte, error) {
	content, err := os.ReadFile(filepath.Join(scriptDir, exportcodec.ScriptJSONFileName))
	if err != nil {
		return nil, nil, err
	}
	codec, err := h.exportCodecForFile(content)
	if err != nil {
		return nil, nil, err
	}
	return codec, content, nil
}

// readWorkflowIDFromStoreDir 读取 store 仓库内 workflow.json 的 workflow_id。
//
// store.PathName 只是随机目录名，不承载 workflow_id，因此所有 store（本地发布与远程下载）
// 的真实 workflow_id 都从这里读取；store 是裸仓库，没有工作区文件，内容从 git 对象里读
// （见 readStoreExportJSON）。
func (h *WorkflowHandler) readWorkflowIDFromStoreDir(storeDir string) string {
	content, err := readStoreExportJSON(storeDir, exportcodec.WorkflowJSONFileName)
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

// readWorkflowRawFromDir 读取 workflow 目录下的 workflow.json 原始内容，并按文件顶层 version 取读侧 Codec。
//
// 只读取不解析：解析与落库由 Codec.InstallWorkflow 完成（它内部调用 DecodeWorkflow）。
func (h *WorkflowHandler) readWorkflowRawFromDir(workflowDir string) (exportcodec.Codec, []byte, error) {
	content, err := os.ReadFile(filepath.Join(workflowDir, exportcodec.WorkflowJSONFileName))
	if err != nil {
		return nil, nil, err
	}
	codec, err := h.exportCodecForFile(content)
	if err != nil {
		return nil, nil, err
	}
	return codec, content, nil
}

// readStoreExportJSON 读取 store 仓库 HEAD 提交里的导出文件（workflow.json / script.json）内容。
//
// store 是裸仓库（本地 publish 与远程 clone 都是裸仓库），没有工作区文件，
// 只能从 git 对象里读（utils.ReadFileFromGitRepo）；文件在仓库内的位置不固定，
// 由 utils.FindFileInGitRepo 按「根目录优先、其次任意层级（忽略大小写）」查找。
func readStoreExportJSON(storeDir, fileName string) ([]byte, error) {
	relPath, err := utils.FindFileInGitRepo(storeDir, fileName)
	if err != nil {
		return nil, err
	}
	return utils.ReadFileFromGitRepo(storeDir, relPath)
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

// func buildPublishURLsJSON(pathName string) (datatypes.JSON, error) {
// 	publishURLs := []map[string]string{
// 		{
// 			"name":  "github",
// 			"ssh":   fmt.Sprintf("git@github.com:%s.git", pathName),
// 			"https": fmt.Sprintf("https://github.com/%s.git", pathName),
// 		},
// 		{
// 			"name":  "gitee",
// 			"ssh":   fmt.Sprintf("git@gitee.com:%s.git", pathName),
// 			"https": fmt.Sprintf("https://gitee.com/%s.git", pathName),
// 		},
// 	}

// 	b, err := json.Marshal(publishURLs)
// 	if err != nil {
// 		return nil, err
// 	}
// 	return datatypes.JSON(b), nil
// }
