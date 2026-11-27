package container

import (
	"context"
	"encoding/json"
	stderrs "errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/exportcodec"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/google/uuid"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

// 本文件实现「组件仓库（store）」的 Agent 能力，供内置工具
// page_store / publish_workflow / publish_script / install_workflow / install_script 使用。
//
// 口径与 handler 对应端点保持一致（internal/handler/store.go 的 PageStore、
// internal/handler/workflow_publish.go 的 PublishWorkflow / PublishScript /
// InstallWorkflow / InstallScript）：
//
//   - store 是裸仓库：读取 workflow.json / script.json 必须走 git 对象
//     （utils.FindFileInGitRepo + utils.ReadFileFromGitRepo），不能用 os.ReadFile；
//   - store 目录名（PathName）由 utils.GenerateStorePathName 生成一次并在重新发布时沿用；
//   - 安装目标目录由导出文件里的 workflow_id / script_id 决定，不来自 PathName。
//
// 与 handler 的分工：handler 负责 HTTP 入参绑定与错误码映射，本实现把这些业务步骤
// 直接暴露成工具端口，避免工具层重新拼装 service 调用。

// storeTools 同时实现 runtimecontext 的 StoreBrowser / StoreComponentPublisher /
// StoreComponentInstaller，由 container 单点装配到 builtin.Deps。
type storeTools struct {
	storeSvc    interfaces.StoreService
	workflowSvc interfaces.WorkflowService
	projectSvc  interfaces.ProjectService
	codecs      *exportcodec.Registry
	cfg         *config.Config
}

// newStoreTools 构建「组件仓库查询 / 发布 / 安装」执行器。
func newStoreTools(
	storeSvc interfaces.StoreService,
	workflowSvc interfaces.WorkflowService,
	projectSvc interfaces.ProjectService,
	codecs *exportcodec.Registry,
	cfg *config.Config,
) *storeTools {
	return &storeTools{
		storeSvc:    storeSvc,
		workflowSvc: workflowSvc,
		projectSvc:  projectSvc,
		codecs:      codecs,
		cfg:         cfg,
	}
}

// PageStore 分页查询 store，并回填 store 的本地目录与标签。
func (s *storeTools) PageStore(ctx context.Context, userID int64, in runtimecontext.StorePageInput) (runtimecontext.StorePageResult, error) {
	if userID <= 0 {
		return runtimecontext.StorePageResult{}, fmt.Errorf("page_store: user_id is required")
	}
	if s.storeSvc == nil {
		return runtimecontext.StorePageResult{}, fmt.Errorf("page_store: store service is not configured")
	}
	storeType := strings.ToLower(strings.TrimSpace(in.StoreType))
	if storeType != "workflow" && storeType != "script" {
		return runtimecontext.StorePageResult{}, fmt.Errorf("page_store: store_type must be workflow or script")
	}

	pagination := &types.Pagination{Page: in.Page, PageSize: in.PageSize}
	query := &types.StorePageQuery{
		StoreType: storeType,
		Keywords:  strings.TrimSpace(in.Keywords),
		SortBy:    strings.TrimSpace(in.SortBy),
		SortOrder: strings.TrimSpace(in.SortOrder),
	}
	result, err := s.storeSvc.PageStore(ctx, userID, pagination, query)
	if err != nil {
		return runtimecontext.StorePageResult{}, fmt.Errorf("page_store: failed to query stores: %w", err)
	}

	out := runtimecontext.StorePageResult{
		Total:    result.Total,
		Page:     result.Page,
		PageSize: result.PageSize,
		Stores:   make([]runtimecontext.StoreItem, 0),
	}
	dtos, _ := result.Data.([]*types.StoreDTO)
	for _, dto := range dtos {
		if dto == nil {
			continue
		}
		out.Stores = append(out.Stores, s.toStoreItem(&dto.Store, dto.Installed, dto.InstalledID))
	}
	return out, nil
}

// toStoreItem 把 store 记录转成工具返回的摘要（含绝对目录、标签列表与仓库里的 description）。
//
// description 不从数据库读（store 表没有该列），而是与 handler.PageStore 同口径地
// 从 store 裸仓库 HEAD 的 workflow.json / script.json 里取；读不到时为空串。
func (s *storeTools) toStoreItem(store *types.Store, installed bool, installedID string) runtimecontext.StoreItem {
	storePath := s.storeDir(store)
	return runtimecontext.StoreItem{
		StoreID:     store.ID,
		StoreType:   store.StoreType,
		Name:        store.Name,
		Description: exportcodec.StoreDescription(storePath, store.StoreType),
		Origin:      store.Origin,
		Category:    store.Category,
		Tags:        decodeStoreTags(store.Tags),
		PathName:    store.PathName,
		StorePath:   storePath,
		Installed:   installed,
		InstalledID: installedID,
	}
}

// decodeStoreTags 把 store.tags（JSON 列）解码成字符串列表。
//
// 历史数据既有 JSON 字符串数组，也可能是任意 JSON；无法解码成字符串列表时，
// 按元素 fmt.Sprint 兜底，保证工具输出稳定可读。
func decodeStoreTags(raw datatypes.JSON) []string {
	if len(raw) == 0 {
		return nil
	}
	var tags []string
	if err := json.Unmarshal(raw, &tags); err == nil {
		return tags
	}
	var anyTags []any
	if err := json.Unmarshal(raw, &anyTags); err != nil {
		return nil
	}
	out := make([]string, 0, len(anyTags))
	for _, v := range anyTags {
		if v == nil {
			continue
		}
		if text := strings.TrimSpace(fmt.Sprint(v)); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// PublishWorkflow 把工作流发布到 store（与 handler.PublishWorkflow 同口径）。
func (s *storeTools) PublishWorkflow(ctx context.Context, in runtimecontext.StorePublishWorkflowInput) (runtimecontext.StorePublishResult, error) {
	if in.ProjectID <= 0 {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: project_id is required")
	}
	if in.WorkflowID <= 0 {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: workflow_id is required")
	}
	if s.workflowSvc == nil || s.storeSvc == nil || s.projectSvc == nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: required services are not configured")
	}
	baseDir := s.storageBaseDir()
	if baseDir == "" {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: storage.base_dir is empty")
	}

	workflow, err := s.workflowSvc.GetWorkflowByID(ctx, in.WorkflowID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: workflow %d not found", in.WorkflowID)
		}
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: failed to get workflow %d: %w", in.WorkflowID, err)
	}
	if workflow == nil || workflow.ID == 0 {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: workflow %d not found", in.WorkflowID)
	}
	if workflow.ProjectID != in.ProjectID {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: workflow %d does not belong to project %d", in.WorkflowID, in.ProjectID)
	}

	project, err := s.projectSvc.GetProjectByID(ctx, workflow.ProjectID)
	if err != nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: failed to get project %d: %w", workflow.ProjectID, err)
	}
	if project == nil || project.ID == 0 {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: project %d not found", workflow.ProjectID)
	}

	existingStore, pathName, err := s.resolveStorePathNameForPublish(ctx, workflow.StoreID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: store not found")
		}
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: failed to prepare store path name: %w", err)
	}
	storePath := utils.GetWorkflowOrScriptStoreDir(baseDir, pathName)

	store := &types.Store{
		StoreType: "workflow",
		Name:      workflow.Name,
		Origin:    "local",
		PathName:  pathName,
		Category:  workflow.Category,
		Tags:      workflow.Tags,
		Img:       workflow.Img,
	}
	if err := os.MkdirAll(storePath, 0o755); err != nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: failed to create store path: %w", err)
	}

	if existingStore != nil {
		if err := s.cleanupLegacyStoreDir(baseDir, existingStore.PathName, pathName); err != nil {
			return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: failed to clean old store path: %w", err)
		}
		store.ID = existingStore.ID
		if err := s.storeSvc.UpdateStore(ctx, store); err != nil {
			return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: failed to update store: %w", err)
		}
	} else {
		if err := s.storeSvc.CreateStore(ctx, store); err != nil {
			return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: failed to create store: %w", err)
		}
		workflow.StoreID = store.ID
	}
	if err := s.workflowSvc.UpdateWorkflow(ctx, workflow); err != nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: failed to update workflow publish info: %w", err)
	}

	workflowSourceDir := utils.GetWorkflowFileDir(baseDir, project.ID, workflow.WorkflowID)
	codec, err := s.writeCodec()
	if err != nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: %w", err)
	}
	message := strings.TrimSpace(in.Message)
	if message == "" {
		message = fmt.Sprintf("publish workflow %s", workflow.WorkflowID)
	}
	if _, err := codec.WriteCommitWorkflowFiles(ctx, exportcodec.WorkflowWriteRequest{
		WorkflowPK:    workflow.ID,
		ProjectID:     project.ID,
		BaseDir:       baseDir,
		WorkflowDir:   workflowSourceDir,
		CommitMessage: message,
	}); err != nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: failed to prepare workflow files: %w", err)
	}

	if _, err := utils.EnsureBareGitRepo(storePath); err != nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: failed to prepare store git repository: %w", err)
	}
	pushed, err := utils.PushDirToRepo(ctx, workflowSourceDir, storePath)
	if err != nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_workflow: failed to push workflow to store: %w", err)
	}

	return runtimecontext.StorePublishResult{
		StoreID:     store.ID,
		StoreType:   store.StoreType,
		StoreName:   store.Name,
		PathName:    pathName,
		StorePath:   storePath,
		Pushed:      pushed,
		UpToDate:    !pushed,
		ComponentID: workflow.WorkflowID,
	}, nil
}

// PublishScript 把脚本发布到 store（与 handler.PublishScript 同口径）。
func (s *storeTools) PublishScript(ctx context.Context, in runtimecontext.StorePublishScriptInput) (runtimecontext.StorePublishResult, error) {
	if in.ProjectID <= 0 {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: project_id is required")
	}
	if in.ScriptID <= 0 {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: script_id is required")
	}
	if s.workflowSvc == nil || s.storeSvc == nil || s.projectSvc == nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: required services are not configured")
	}
	baseDir := s.storageBaseDir()
	if baseDir == "" {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: storage.base_dir is empty")
	}

	script, err := s.workflowSvc.GetScriptByID(ctx, in.ScriptID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: script %d not found", in.ScriptID)
		}
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: failed to get script %d: %w", in.ScriptID, err)
	}
	if script == nil || script.ID == 0 {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: script %d not found", in.ScriptID)
	}
	if script.ProjectID != in.ProjectID {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: script %d does not belong to project %d", in.ScriptID, in.ProjectID)
	}

	project, err := s.projectSvc.GetProjectByID(ctx, script.ProjectID)
	if err != nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: failed to get project %d: %w", script.ProjectID, err)
	}
	if project == nil || project.ID == 0 {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: project %d not found", script.ProjectID)
	}

	sourceScriptDir := utils.GetScriptFileDir(baseDir, project.ID, script.ScriptID)

	existingStore, pathName, err := s.resolveStorePathNameForPublish(ctx, script.StoreID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: store not found")
		}
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: failed to prepare store path name: %w", err)
	}
	storePath := utils.GetWorkflowOrScriptStoreDir(baseDir, pathName)

	store := &types.Store{
		StoreType: "script",
		Name:      script.Name,
		Origin:    "local",
		PathName:  pathName,
		Category:  script.Category,
		Tags:      nil,
		Img:       script.Img,
	}
	if script.Tags != "" && json.Valid([]byte(script.Tags)) {
		store.Tags = datatypes.JSON(script.Tags)
	}

	if err := os.MkdirAll(storePath, 0o755); err != nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: failed to create store path: %w", err)
	}

	if existingStore != nil {
		if err := s.cleanupLegacyStoreDir(baseDir, existingStore.PathName, pathName); err != nil {
			return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: failed to clean old store path: %w", err)
		}
		store.ID = existingStore.ID
		if err := s.storeSvc.UpdateStore(ctx, store); err != nil {
			return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: failed to update store: %w", err)
		}
	} else {
		if err := s.storeSvc.CreateStore(ctx, store); err != nil {
			return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: failed to create store: %w", err)
		}
		script.StoreID = store.ID
	}
	if err := s.workflowSvc.UpdateScript(ctx, script); err != nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: failed to update script publish info: %w", err)
	}

	codec, err := s.writeCodec()
	if err != nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: %w", err)
	}
	message := strings.TrimSpace(in.Message)
	if message == "" {
		message = fmt.Sprintf("publish script %s", script.ScriptID)
	}
	if _, err := codec.WriteCommitScriptFiles(ctx, exportcodec.ScriptWriteRequest{
		ScriptPK:      script.ID,
		ScriptDir:     sourceScriptDir,
		CommitMessage: message,
	}); err != nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: failed to prepare script files: %w", err)
	}

	if _, err := utils.EnsureBareGitRepo(storePath); err != nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: failed to prepare store git repository: %w", err)
	}
	pushed, err := utils.PushDirToRepo(ctx, sourceScriptDir, storePath)
	if err != nil {
		return runtimecontext.StorePublishResult{}, fmt.Errorf("publish_script: failed to push script to store: %w", err)
	}

	return runtimecontext.StorePublishResult{
		StoreID:     store.ID,
		StoreType:   store.StoreType,
		StoreName:   store.Name,
		PathName:    pathName,
		StorePath:   storePath,
		Pushed:      pushed,
		UpToDate:    !pushed,
		ComponentID: script.ScriptID,
	}, nil
}

// InstallWorkflow 从 store 安装工作流（与 handler.InstallWorkflow 同口径）。
func (s *storeTools) InstallWorkflow(ctx context.Context, in runtimecontext.StoreInstallWorkflowInput) (runtimecontext.StoreInstallResult, error) {
	if in.ProjectID <= 0 {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: project_id is required")
	}
	if in.StoreID <= 0 {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: store_id is required")
	}
	if s.storeSvc == nil || s.projectSvc == nil {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: required services are not configured")
	}
	baseDir := s.storageBaseDir()
	if baseDir == "" {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: storage.base_dir is empty")
	}

	project, err := s.projectSvc.GetProjectByID(ctx, in.ProjectID)
	if err != nil {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: failed to get project %d: %w", in.ProjectID, err)
	}
	if project == nil || project.ID == 0 {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: project %d not found", in.ProjectID)
	}

	store, err := s.requireStore(ctx, "install_workflow", in.StoreID, "workflow")
	if err != nil {
		return runtimecontext.StoreInstallResult{}, err
	}
	storeDir := s.storeDir(store)
	if storeDir == "" {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: store path is empty")
	}

	content, err := s.readStoreExportJSON(storeDir, exportcodec.WorkflowJSONFileName)
	if err != nil {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: failed to read workflow.json from store: %w", err)
	}
	codec, err := s.codecForFile(content)
	if err != nil {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: %w", err)
	}
	payload, err := codec.DecodeWorkflow(content)
	if err != nil {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: failed to parse workflow.json: %w", err)
	}
	workflowID := strings.TrimSpace(payload.WorkflowID)
	if workflowID == "" {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: workflow_id is required in workflow.json")
	}

	targetDir := utils.GetWorkflowFileDir(baseDir, project.ID, workflowID)
	if err := utils.SyncWorktreeFromRepo(ctx, targetDir, storeDir); err != nil {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: failed to sync workflow files from store: %w", err)
	}

	raw, err := os.ReadFile(filepath.Join(targetDir, exportcodec.WorkflowJSONFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: workflow.json not found in store")
		}
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: failed to read workflow.json: %w", err)
	}
	codec, err = s.codecForFile(raw)
	if err != nil {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: %w", err)
	}

	result, err := codec.InstallWorkflow(ctx, exportcodec.WorkflowInstallRequest{
		Raw:         raw,
		ProjectID:   project.ID,
		StoreID:     store.ID,
		BaseDir:     baseDir,
		WorkflowDir: targetDir,
	})
	if err != nil {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_workflow: failed to install workflow: %w", err)
	}

	return runtimecontext.StoreInstallResult{
		StoreID:                          store.ID,
		StorePath:                        storeDir,
		TargetDir:                        targetDir,
		ComponentID:                      result.WorkflowID,
		InstalledID:                      result.InstalledWorkflowID,
		InstalledScriptCount:             result.InstalledScriptCount,
		ContainerTemplateSpecCount:       result.ContainerTemplateSpecCount,
		ContainerTemplateDefinitionCount: result.ContainerTemplateDefinitionCount,
	}, nil
}

// InstallScript 从 store 安装脚本（与 handler.InstallScript 同口径）。
func (s *storeTools) InstallScript(ctx context.Context, in runtimecontext.StoreInstallScriptInput) (runtimecontext.StoreInstallResult, error) {
	if in.ProjectID <= 0 {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: project_id is required")
	}
	if in.StoreID <= 0 {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: store_id is required")
	}
	if s.storeSvc == nil || s.projectSvc == nil {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: required services are not configured")
	}
	baseDir := s.storageBaseDir()
	if baseDir == "" {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: storage.base_dir is empty")
	}

	project, err := s.projectSvc.GetProjectByID(ctx, in.ProjectID)
	if err != nil {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: failed to get project %d: %w", in.ProjectID, err)
	}
	if project == nil || project.ID == 0 {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: project %d not found", in.ProjectID)
	}

	store, err := s.requireStore(ctx, "install_script", in.StoreID, "script")
	if err != nil {
		return runtimecontext.StoreInstallResult{}, err
	}
	storeDir := s.storeDir(store)
	if storeDir == "" {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: store path is empty")
	}

	// 目标 script_id：create=true 时作为副本新增（新 uuid），否则从 store 的
	// script.json 读取；store.PathName 只是随机目录名，不承载 script_id。
	scriptID := ""
	if in.CreateMode {
		scriptID = uuid.NewString()
	} else {
		content, err := s.readStoreExportJSON(storeDir, exportcodec.ScriptJSONFileName)
		if err != nil {
			return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: failed to read script.json from store: %w", err)
		}
		codec, err := s.codecForFile(content)
		if err != nil {
			return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: %w", err)
		}
		payload, err := codec.DecodeScript(content)
		if err != nil {
			return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: failed to parse script.json: %w", err)
		}
		scriptID = strings.TrimSpace(payload.ScriptID)
	}
	if scriptID == "" {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: script_id is required in script.json")
	}

	targetDir := utils.GetScriptFileDir(baseDir, project.ID, scriptID)
	if err := utils.SyncWorktreeFromRepo(ctx, targetDir, storeDir); err != nil {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: failed to sync script files from store: %w", err)
	}

	raw, err := os.ReadFile(filepath.Join(targetDir, exportcodec.ScriptJSONFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: script.json not found in store")
		}
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: failed to read script.json: %w", err)
	}
	codec, err := s.codecForFile(raw)
	if err != nil {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: %w", err)
	}

	result, err := codec.InstallScript(ctx, exportcodec.ScriptInstallRequest{
		Raw:        raw,
		ProjectID:  project.ID,
		StoreID:    store.ID,
		ScriptID:   scriptID,
		CreateMode: in.CreateMode,
	})
	if err != nil {
		return runtimecontext.StoreInstallResult{}, fmt.Errorf("install_script: failed to install script: %w", err)
	}

	return runtimecontext.StoreInstallResult{
		StoreID:                          store.ID,
		StorePath:                        storeDir,
		TargetDir:                        targetDir,
		ComponentID:                      result.ScriptID,
		InstalledID:                      result.InstalledScriptID,
		ContainerTemplateSpecCount:       result.ContainerTemplateSpecCount,
		ContainerTemplateDefinitionCount: result.ContainerTemplateDefinitionCount,
	}, nil
}

// requireStore 读取 store 记录并校验组件类型。
//
// 类型校验在工具侧完成：把脚本 store 当工作流安装（反之亦然）只会在解析导出文件时
// 报出难以理解的错误，提前拒绝并给出可操作的提示更清晰。
func (s *storeTools) requireStore(ctx context.Context, toolName string, storeID int64, wantType string) (*types.Store, error) {
	store, err := s.storeSvc.GetStoreByID(ctx, storeID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%s: store %d not found", toolName, storeID)
		}
		return nil, fmt.Errorf("%s: failed to get store %d: %w", toolName, storeID, err)
	}
	if store == nil || store.ID == 0 {
		return nil, fmt.Errorf("%s: store %d not found", toolName, storeID)
	}
	if storeType := strings.ToLower(strings.TrimSpace(store.StoreType)); storeType != wantType {
		return nil, fmt.Errorf("%s: store %d is a %s store, not %s", toolName, storeID, storeType, wantType)
	}
	return store, nil
}

// resolveStorePathNameForPublish 决定本次发布使用的 store 目录标识（types.Store.PathName）。
//
// 与 handler 同口径：storeID==0（首次发布）时生成随机串；否则沿用既有记录的 PathName，
// 只有历史行 PathName 为空时才补一个随机值。existingStore 为 nil 表示还没有 store 记录。
func (s *storeTools) resolveStorePathNameForPublish(ctx context.Context, storeID int64) (*types.Store, string, error) {
	if storeID == 0 {
		pathName, err := utils.GenerateStorePathName()
		if err != nil {
			return nil, "", err
		}
		return nil, pathName, nil
	}

	existingStore, err := s.storeSvc.GetStoreByID(ctx, storeID)
	if err != nil {
		return nil, "", err
	}
	if existingStore == nil {
		return nil, "", gorm.ErrRecordNotFound
	}

	pathName := strings.TrimSpace(existingStore.PathName)
	if pathName == "" {
		pathName, err = utils.GenerateStorePathName()
		if err != nil {
			return nil, "", err
		}
	}
	return existingStore, pathName, nil
}

// cleanupLegacyStoreDir 在发布时清理旧规则（workflow_id / script_id）留下的 store 目录。
//
// 只有目录名确实变化时才删除，避免 base_dir/store 下残留历史产物；删除失败返回错误，
// 与 handler 的行为一致（此时 store 记录还没更新，重试即可）。
func (s *storeTools) cleanupLegacyStoreDir(baseDir, oldPathName, newPathName string) error {
	oldPathName = strings.TrimSpace(oldPathName)
	if oldPathName == "" || oldPathName == strings.TrimSpace(newPathName) {
		return nil
	}
	oldDir := utils.GetWorkflowOrScriptStoreDir(baseDir, oldPathName)
	if stat, err := os.Stat(oldDir); err == nil && stat.IsDir() {
		return os.RemoveAll(oldDir)
	}
	return nil
}

// storeDir 返回 store 裸仓库的绝对目录（storage.base_dir/store/<path_name>）。
func (s *storeTools) storeDir(store *types.Store) string {
	if store == nil {
		return ""
	}
	baseDir := s.storageBaseDir()
	pathName := strings.TrimSpace(store.PathName)
	if baseDir == "" || pathName == "" {
		return ""
	}
	return utils.GetWorkflowOrScriptStoreDir(baseDir, pathName)
}

// readStoreExportJSON 读取 store 仓库 HEAD 提交里的导出文件内容。
//
// store 是裸仓库，没有工作区文件，必须从 git 对象里读；文件位置不固定，
// 由 utils.FindFileInGitRepo 按「根目录优先、其次任意层级（忽略大小写）」查找。
func (s *storeTools) readStoreExportJSON(storeDir, fileName string) ([]byte, error) {
	relPath, err := utils.FindFileInGitRepo(storeDir, fileName)
	if err != nil {
		return nil, err
	}
	return utils.ReadFileFromGitRepo(storeDir, relPath)
}

// writeCodec 返回写侧 Codec（当前版本）。
func (s *storeTools) writeCodec() (exportcodec.Codec, error) {
	return s.codec(exportcodec.CurrentVersion)
}

// codecForFile 按导出文件顶层 version 取读侧 Codec；version 为空按当前版本读回。
func (s *storeTools) codecForFile(raw []byte) (exportcodec.Codec, error) {
	version, err := exportcodec.PeekVersion(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid export file json: %w", err)
	}
	if version == "" {
		version = exportcodec.CurrentVersion
	}
	return s.codec(version)
}

// codec 从注入的 Registry 取版本对应的 Codec。
func (s *storeTools) codec(version string) (exportcodec.Codec, error) {
	if s.codecs == nil {
		return nil, fmt.Errorf("export codec registry is not configured")
	}
	if codec := s.codecs.Get(version); codec != nil {
		return codec, nil
	}
	return nil, fmt.Errorf("%w: %s", exportcodec.ErrUnsupportedVersion, version)
}

// storageBaseDir 返回去空白后的 storage.base_dir（未配置时为空串）。
func (s *storeTools) storageBaseDir() string {
	if s.cfg == nil || s.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(s.cfg.Storage.BaseDir)
}
