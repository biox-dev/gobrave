package handler

import (
	stderrs "errors"
	"net/http"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// GitHandler 暴露组件目录（脚本/工作流本地工作区仓库）的只读 git 能力。
//
// 目前只有「查看本地变化 diff」两个接口（script / workflow），
// 后续的 git 操作（提交历史 git log、单文件 diff、回滚等）都在本 handler 里扩展：
// 目录定位复用下方 resolveXxxTarget，读操作复用 internal/utils 里的 git 能力，
// 不在 WorkflowHandler 里继续堆 git 相关逻辑。
//
// 目录布局的推导与 GetScriptById / GetWorkflowById 完全一致：
//
//	本地目录 = utils.GetScriptFileDir / utils.GetWorkflowFileDir(baseDir, project.ProjectID, <uuid>)
//	store 目录 = utils.GetWorkflowOrScriptStoreDir(baseDir, store.PathName)（未发布时为空）
//
// 注意这里用的是 project.project_id（字符串）而非 script/workflow 的 ProjectID（int64 外键）。
type GitHandler struct {
	workflowService interfaces.WorkflowService
	projectService  interfaces.ProjectService
	storeService    interfaces.StoreService
	cfg             *config.Config
}

func NewGitHandler(workflowService interfaces.WorkflowService,
	projectService interfaces.ProjectService,
	storeService interfaces.StoreService,
	cfg *config.Config) *GitHandler {
	return &GitHandler{
		workflowService: workflowService,
		projectService:  projectService,
		storeService:    storeService,
		cfg:             cfg,
	}
}

// gitComponentTarget 是一个组件的 git 视图位置。
type gitComponentTarget struct {
	// Entity 组件类型：script / workflow。
	Entity string
	// ID 组件 uuid（script.script_id / workflow.workflow_id），用于前端展示与回显。
	ID string
	// LocalDir 本地工作区仓库目录（可能尚未初始化为 git 仓库）。
	LocalDir string
	// StoreDir store（发布目标）目录，未发布时为空。
	StoreDir string
}

// gitDiffResponse 是本地变化 diff 接口的响应。
//
// 内嵌 utils.GitDiffResult（repo_dir / initialized / head_commit / has_changes /
// worktree / unpublished）并额外带上组件标识与实时 git 同步状态，
// 便于前端一次请求就拿到「是否有变化 + 变化内容 + 同步状态」。
type gitDiffResponse struct {
	Entity string `json:"entity"`
	ID     string `json:"id"`
	utils.GitDiffResult
	GitState *utils.GitSyncState `json:"git_state,omitempty"`
}

// storageBaseDir 返回配置中的 storage.base_dir（cfg 可能为 nil）。
func (h *GitHandler) storageBaseDir() string {
	if h.cfg == nil || h.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(h.cfg.Storage.BaseDir)
}

// resolveScriptTarget 按脚本主键解析出本地目录与 store 目录。
func (h *GitHandler) resolveScriptTarget(c *gin.Context, scriptID int64) (*gitComponentTarget, error) {
	script, err := h.workflowService.GetScriptByID(c.Request.Context(), scriptID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.NewNotFoundError("script not found")
		}
		return nil, errors.NewInternalServerError("failed to get script").WithDetails(err.Error())
	}

	project, err := h.projectService.GetProjectByID(c.Request.Context(), script.ProjectID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.NewNotFoundError("project not found")
		}
		return nil, errors.NewInternalServerError("failed to get project").WithDetails(err.Error())
	}

	storeDir, err := h.storeDirByID(c, script.StoreID)
	if err != nil {
		return nil, err
	}

	return &gitComponentTarget{
		Entity:   "script",
		ID:       script.ScriptID,
		LocalDir: utils.GetScriptFileDir(h.storageBaseDir(), project.ProjectID, script.ScriptID),
		StoreDir: storeDir,
	}, nil
}

// resolveWorkflowTarget 按工作流主键解析出本地目录与 store 目录。
func (h *GitHandler) resolveWorkflowTarget(c *gin.Context, workflowID int64) (*gitComponentTarget, error) {
	workflow, err := h.workflowService.GetWorkflowByID(c.Request.Context(), workflowID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.NewNotFoundError("workflow not found")
		}
		return nil, errors.NewInternalServerError("failed to get workflow").WithDetails(err.Error())
	}

	project, err := h.projectService.GetProjectByID(c.Request.Context(), workflow.ProjectID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.NewNotFoundError("project not found")
		}
		return nil, errors.NewInternalServerError("failed to get project").WithDetails(err.Error())
	}

	storeDir, err := h.storeDirByID(c, workflow.StoreID)
	if err != nil {
		return nil, err
	}

	return &gitComponentTarget{
		Entity:   "workflow",
		ID:       workflow.WorkflowID,
		LocalDir: utils.GetWorkflowFileDir(h.storageBaseDir(), project.ProjectID, workflow.WorkflowID),
		StoreDir: storeDir,
	}, nil
}

// storeDirByID 解析 store 目录；storeID 为 0（从未发布）时返回空字符串。
func (h *GitHandler) storeDirByID(c *gin.Context, storeID int64) (string, error) {
	if storeID == 0 {
		return "", nil
	}
	store, err := h.storeService.GetStoreByID(c.Request.Context(), storeID)
	if err != nil {
		return "", errors.NewInternalServerError("failed to get store").WithDetails(err.Error())
	}
	if store == nil {
		return "", nil
	}
	return utils.GetWorkflowOrScriptStoreDir(h.storageBaseDir(), store.PathName), nil
}

// respondGitDiff 汇总本地变化并返回。
func respondGitDiff(c *gin.Context, target *gitComponentTarget) {
	diff := utils.ReadGitDiff(target.LocalDir, target.StoreDir)
	gitState := utils.ReadGitSyncState(target.LocalDir, target.StoreDir)

	c.JSON(http.StatusOK, gitDiffResponse{
		Entity:        target.Entity,
		ID:            target.ID,
		GitDiffResult: diff,
		GitState:      &gitState,
	})
}

// GetScriptDiff godoc
// @Summary      获取脚本本地变化 diff
// @Description  按脚本主键定位脚本目录（本地 git 仓库），返回工作区相对 HEAD 的未提交改动（含未跟踪文件）以及本地相对 store 的未发布提交差异；非 git 仓库时 initialized 为 false 且无变化
// @Tags         Git
// @Produce      json
// @Param        scriptId  path      string                true  "脚本主键 ID"
// @Success      200       {object}  handler.gitDiffResponse
// @Failure      400       {object}  errors.AppError
// @Failure      401       {object}  errors.AppError
// @Failure      404       {object}  errors.AppError
// @Failure      500       {object}  errors.AppError
// @Security     Bearer
// @Router       /script/{scriptId}/git-diff [get]
func (h *GitHandler) GetScriptDiff(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}
	if h.storageBaseDir() == "" {
		c.Error(errors.NewInternalServerError("storage base dir is not configured"))
		return
	}

	scriptID, err := parseScriptIDParam(c)
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	target, err := h.resolveScriptTarget(c, scriptID)
	if err != nil {
		c.Error(err)
		return
	}
	respondGitDiff(c, target)
}

// GetWorkflowDiff godoc
// @Summary      获取工作流本地变化 diff
// @Description  按工作流主键定位 workflow 目录（本地 git 仓库），返回工作区相对 HEAD 的未提交改动（含未跟踪文件）以及本地相对 store 的未发布提交差异；非 git 仓库时 initialized 为 false 且无变化
// @Tags         Git
// @Produce      json
// @Param        workflowId  path      string                true  "工作流主键 ID"
// @Success      200         {object}  handler.gitDiffResponse
// @Failure      400         {object}  errors.AppError
// @Failure      401         {object}  errors.AppError
// @Failure      404         {object}  errors.AppError
// @Failure      500         {object}  errors.AppError
// @Security     Bearer
// @Router       /workflow/{workflowId}/git-diff [get]
func (h *GitHandler) GetWorkflowDiff(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}
	if h.storageBaseDir() == "" {
		c.Error(errors.NewInternalServerError("storage base dir is not configured"))
		return
	}

	workflowID, err := parseWorkflowIDParam(c)
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	target, err := h.resolveWorkflowTarget(c, workflowID)
	if err != nil {
		c.Error(err)
		return
	}
	respondGitDiff(c, target)
}
