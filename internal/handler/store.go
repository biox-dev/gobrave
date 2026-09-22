package handler

import (
	"context"
	"encoding/json"
	stderrs "errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/exportcodec"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
	git "github.com/go-git/go-git/v5"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type StoreHandler struct {
	storeService interfaces.StoreService
	cfg          *config.Config
}

type storeByStoreIDQuery struct {
	StoreID string `form:"store_id" binding:"required"`
}

type storePageRequest struct {
	types.Pagination
	Query types.StorePageQuery `json:"query" binding:"required"`
}

type downloadStoreRequest struct {
	URL       string `json:"url" binding:"required"`
	StoreType string `json:"store_type"`
	Name      string `json:"name"`
	Origin    string `json:"origin"`
	Category  string `json:"category"`
	Img       string `json:"img"`
	Version   string `json:"version"`
	Message   string `json:"message"`
	Tags      any    `json:"tags"`
}

func NewStoreHandler(storeService interfaces.StoreService, cfg *config.Config) *StoreHandler {
	return &StoreHandler{storeService: storeService, cfg: cfg}
}

func (h *StoreHandler) CreateStore(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.Store
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	if err := h.storeService.CreateStore(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to create store")
		return
	}

	c.JSON(http.StatusOK, req)
}

func (h *StoreHandler) GetStore(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.storeService.GetStoreByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get store")
		return
	}

	fillStorePath(h.cfg, item)

	c.JSON(http.StatusOK, item)
}

func (h *StoreHandler) GetStoreByStoreID(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req storeByStoreIDQuery
	if err := c.ShouldBindQuery(&req); err != nil {
		c.Error(errors.NewValidationError("invalid query parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.storeService.GetStoreByStoreID(c.Request.Context(), req.StoreID)
	if err != nil {
		handleDataError(c, err, "failed to get store by store id")
		return
	}

	fillStorePath(h.cfg, item)

	c.JSON(http.StatusOK, item)
}

func (h *StoreHandler) UpdateStore(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req types.Store
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if req.ID == 0 {
		c.Error(errors.NewValidationError("id is required"))
		return
	}

	if err := h.storeService.UpdateStore(c.Request.Context(), &req); err != nil {
		handleDataError(c, err, "failed to update store")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "store updated successfully"})
}

func (h *StoreHandler) DeleteStore(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	storeItem, err := h.storeService.GetStoreByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get store")
		return
	}

	if storeItem != nil {
		storePath := resolveStoreDir(h.cfg, storeItem)
		if storePath != "" {
			storeRoot := utils.GetStoreDir(strings.TrimSpace(h.cfg.Storage.BaseDir))
			safePath, pathErr := utils.SafePathUnderBase(storeRoot, storePath)
			if pathErr != nil {
				c.Error(errors.NewValidationError("invalid store path").WithDetails(pathErr.Error()))
				return
			}

			if _, statErr := os.Stat(safePath); statErr == nil {
				if rmErr := os.RemoveAll(safePath); rmErr != nil {
					c.Error(errors.NewInternalServerError("failed to delete store directory").WithDetails(rmErr.Error()))
					return
				}
			} else if !os.IsNotExist(statErr) {
				c.Error(errors.NewInternalServerError("failed to inspect store directory").WithDetails(statErr.Error()))
				return
			}
		}
	}

	if err := h.storeService.DeleteStore(c.Request.Context(), req.ID); err != nil {
		handleDataError(c, err, "failed to delete store")
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "store deleted successfully"})
}

func (h *StoreHandler) ListStore(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	items, err := h.storeService.ListStore(c.Request.Context())
	if err != nil {
		handleDataError(c, err, "failed to list store")
		return
	}

	for _, item := range items {
		fillStorePath(h.cfg, item)
	}

	c.JSON(http.StatusOK, items)
}

func (h *StoreHandler) PageStore(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req storePageRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}
	if storeType := req.Query.GetStoreType(); storeType != "workflow" && storeType != "script" {
		c.Error(errors.NewValidationError("query.store_type must be workflow or script"))
		return
	}

	result, err := h.storeService.PageStore(c.Request.Context(), userID, &req.Pagination, &req.Query)
	if err != nil {
		handleDataError(c, err, "failed to page store")
		return
	}

	fillStorePathForPage(h.cfg, result)

	c.JSON(http.StatusOK, gin.H{
		"data":      result.Data,
		"total":     result.Total,
		"page":      result.Page,
		"page_size": result.PageSize,
	})
}

func (h *StoreHandler) DownloadStore(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req downloadStoreRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	repoURL := strings.TrimSpace(req.URL)
	if repoURL == "" {
		c.Error(errors.NewValidationError("url is required"))
		return
	}

	if h.cfg == nil || h.cfg.Storage == nil || strings.TrimSpace(h.cfg.Storage.BaseDir) == "" {
		c.Error(errors.NewInternalServerError("storage base dir is not configured"))
		return
	}

	findStore, err := h.storeService.GetStoreByURL(c.Request.Context(), repoURL)
	if err == nil && findStore != nil {
		c.JSON(http.StatusOK, gin.H{
			"store_id":       findStore.ID,
			"already_exists": true,
			"message":        "success",
		})
		return
	}
	if err != nil && !stderrs.Is(err, gorm.ErrRecordNotFound) {
		handleDataError(c, err, "failed to query store by url")
		return
	}

	// store 目录名不再由仓库地址推导，统一用随机标识：目录名与上游仓库解耦，
	// 既不暴露 owner/repo，也不会因仓库改名或同一个 owner/repo 结两次而互相污染。
	// 只生成一次，后续 ReDownloadStore 不再重新生成（它复用库里的 path_name）。
	pathName, err := utils.GenerateStorePathName()
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to generate store path name").WithDetails(err.Error()))
		return
	}

	// storeRoot := utils.GetStoreDir(strings.TrimSpace(h.cfg.Storage.BaseDir))
	targetPath := utils.GetWorkflowOrScriptStoreDir(h.cfg.Storage.BaseDir, pathName)
	targetPath, err = utils.SafePathUnderBase(h.cfg.Storage.BaseDir, targetPath)
	if err != nil {
		c.Error(errors.NewValidationError("invalid target path").WithDetails(err.Error()))
		return
	}

	if stat, statErr := os.Stat(targetPath); statErr == nil && stat.IsDir() {
		entries, readErr := os.ReadDir(targetPath)
		if readErr != nil {
			c.Error(errors.NewInternalServerError("failed to inspect existing store directory").WithDetails(readErr.Error()))
			return
		}
		if len(entries) > 0 {
			c.Error(errors.NewConflictError("store directory already exists and is not empty"))
			return
		}
	} else if statErr != nil && !os.IsNotExist(statErr) {
		c.Error(errors.NewInternalServerError("failed to inspect store directory").WithDetails(statErr.Error()))
		return
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		c.Error(errors.NewInternalServerError("failed to prepare store directory").WithDetails(err.Error()))
		return
	}

	// publish_urls 仍描述来源仓库本身（同一仓库的 ssh / https 两种地址），
	// 由 git 地址推导，与随机目录名无关。
	repoPath, err := repoPathFromGitURL(repoURL)
	if err != nil {
		c.Error(errors.NewValidationError("invalid git url").WithDetails(err.Error()))
		return
	}

	// publishURLs, err := buildPublishURLsJSON(repoPath)
	// if err != nil {
	// 	c.Error(errors.NewInternalServerError("failed to build publish urls").WithDetails(err.Error()))
	// 	return
	// }

	tagsJSON, err := buildStoreTagsJSON(req.Tags)
	if err != nil {
		c.Error(errors.NewValidationError("invalid tags").WithDetails(err.Error()))
		return
	}

	storeType := strings.ToLower(strings.TrimSpace(req.StoreType))
	// if storeType == "" {
	// 	storeType = "workflow"
	// }
	if storeType != "workflow" && storeType != "script" {
		c.Error(errors.NewValidationError("store_type must be workflow or script"))
		return
	}

	storeName := strings.TrimSpace(req.Name)
	if storeName == "" {
		storeName = repoPath
	}
	origin := strings.TrimSpace(req.Origin)
	if origin == "" {
		origin = "remote"
	}

	item := &types.Store{
		StoreType: storeType,
		Name:      storeName,
		Origin:    origin,
		URL:       repoURL,
		// Status:    "running",
		PathName: pathName,
		Category: strings.TrimSpace(req.Category),
		Tags:     tagsJSON,
		Img:      strings.TrimSpace(req.Img),
		// PublishURLs: publishURLs,
		// Version:     strings.TrimSpace(req.Version),
		// Message: strings.TrimSpace(req.Message),
		Log: fmt.Sprintf("clone %s %s", repoURL, targetPath),
	}

	if err := h.storeService.CreateStore(c.Request.Context(), item); err != nil {
		handleDataError(c, err, "failed to create store record")
		return
	}

	// 远程 store 与本地 publish 的 store 保持同一种形态：裸仓库（没有工作区），
	// 目录本身就是 git 目录，安装/取封面等统一从 git 对象里读。
	_, cloneErr := git.PlainCloneContext(c.Request.Context(), targetPath, true, &git.CloneOptions{
		URL: repoURL,
	})
	if cloneErr != nil {
		// item.Status = "failed"
		// item.Message = cloneErr.Error()
		item.Log = cloneErr.Error()
		if updateErr := h.storeService.UpdateStore(c.Request.Context(), item); updateErr != nil {
			c.Error(errors.NewInternalServerError("git clone failed and failed to update store status").WithDetails(fmt.Sprintf("clone err: %v; update err: %v", cloneErr, updateErr)))
			return
		}
		c.Error(errors.NewInternalServerError("git clone failed").WithDetails(cloneErr.Error()))
		return
	}

	if metadataErr := hydrateStoreMetadataFromStoreFiles(targetPath, item); metadataErr != nil {
		// item.Status = "failed"
		// item.Message = metadataErr.Error()
		item.Log = metadataErr.Error()
		if updateErr := h.storeService.UpdateStore(c.Request.Context(), item); updateErr != nil {
			c.Error(errors.NewInternalServerError("failed to parse store metadata and failed to update store status").WithDetails(fmt.Sprintf("metadata err: %v; update err: %v", metadataErr, updateErr)))
			return
		}
		c.Error(errors.NewInternalServerError("failed to parse store metadata").WithDetails(metadataErr.Error()))
		return
	}

	// item.Status = "done"
	item.Log = "clone completed"
	if err := h.storeService.UpdateStore(c.Request.Context(), item); err != nil {
		handleDataError(c, err, "failed to update store status")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"store_id":       item.ID,
		"already_exists": false,
		"path":           targetPath,
		"path_name":      item.PathName,
		"message":        "success",
	})
}

func (h *StoreHandler) ReDownloadStore(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	var req idBody
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(errors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	item, err := h.storeService.GetStoreByID(c.Request.Context(), req.ID)
	if err != nil {
		handleDataError(c, err, "failed to get store")
		return
	}

	targetPath := resolveStoreDir(h.cfg, item)
	if targetPath == "" {
		c.Error(errors.NewValidationError("store path is empty"))
		return
	}
	if stat, statErr := os.Stat(targetPath); statErr != nil {
		if os.IsNotExist(statErr) {
			c.Error(errors.NewNotFoundError("store directory not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to inspect store directory").WithDetails(statErr.Error()))
		return
	} else if !stat.IsDir() {
		c.Error(errors.NewValidationError("store path is not a directory"))
		return
	}

	repo, err := git.PlainOpen(targetPath)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to open git repository").WithDetails(err.Error()))
		return
	}

	pullErr := pullStoreRepo(c.Request.Context(), repo, targetPath)
	if pullErr != nil && !stderrs.Is(pullErr, git.NoErrAlreadyUpToDate) {
		// item.Status = "done"
		item.Log = pullErr.Error()
		// item.Message = pullErr.Error()
		if updateErr := h.storeService.UpdateStore(c.Request.Context(), item); updateErr != nil {
			c.Error(errors.NewInternalServerError("git pull failed and failed to update store info").WithDetails(fmt.Sprintf("pull err: %v; update err: %v", pullErr, updateErr)))
			return
		}
		c.Error(errors.NewInternalServerError("git pull failed").WithDetails(pullErr.Error()))
		return
	}

	if metadataErr := hydrateStoreMetadataFromStoreFiles(targetPath, item); metadataErr != nil {
		item.Log = metadataErr.Error()
		// item.Message = metadataErr.Error()
		if updateErr := h.storeService.UpdateStore(c.Request.Context(), item); updateErr != nil {
			c.Error(errors.NewInternalServerError("failed to parse store metadata and failed to update store info").WithDetails(fmt.Sprintf("metadata err: %v; update err: %v", metadataErr, updateErr)))
			return
		}
		c.Error(errors.NewInternalServerError("failed to parse store metadata").WithDetails(metadataErr.Error()))
		return
	}

	// item.Status = "done"
	if stderrs.Is(pullErr, git.NoErrAlreadyUpToDate) {
		item.Log = "already up to date"
		// item.Message = "already up to date"
	} else {
		item.Log = "pull completed"
		// item.Message = "pull completed"
	}

	if err := h.storeService.UpdateStore(c.Request.Context(), item); err != nil {
		handleDataError(c, err, "failed to update store")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"store_id":   item.ID,
		"up_to_date": stderrs.Is(pullErr, git.NoErrAlreadyUpToDate),
		"message":    "success",
	})
}

// pullStoreRepo 把 store 仓库更新到远端最新提交。
//
// store 是裸仓库（本地 publish 与远程 clone 都是裸仓库，没有工作区），
// 裸仓库不能用 Worktree().Pull，改为 fetch origin 后把本地分支指向同名远端分支；
// 历史遗留的普通仓库仍走 PullContext。两者都以 git.NoErrAlreadyUpToDate 表示无更新。
func pullStoreRepo(ctx context.Context, repo *git.Repository, repoPath string) error {
	if repo == nil {
		return fmt.Errorf("git repository is nil")
	}
	if cfg, cfgErr := repo.Config(); cfgErr == nil && cfg.Core.IsBare {
		return utils.FetchBareRepoFromOrigin(ctx, repoPath)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	return wt.PullContext(ctx, &git.PullOptions{RemoteName: "origin"})
}

// repoPathFromGitURL 从 git 地址（ssh 或 http(s)）里取出 "<owner>/<repo>"。
//
// 只用于派生「仓库自身」的信息（当前是 publish_urls 里的 github / gitee 地址建议，
// 以及下载 store 的默认展示名），不再用来给 store 目录命名：
// 目录名统一走 utils.GenerateStorePathName（见 types.Store.PathName 的注释）。
func repoPathFromGitURL(rawURL string) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", fmt.Errorf("url is empty")
	}

	pathValue := ""
	if strings.HasPrefix(rawURL, "git@") && strings.Contains(rawURL, ":") {
		sshParts := strings.SplitN(rawURL, ":", 2)
		if len(sshParts) != 2 {
			return "", fmt.Errorf("invalid ssh git url: %s", rawURL)
		}
		pathValue = sshParts[1]
	} else {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return "", err
		}
		if parsed.Path == "" {
			return "", fmt.Errorf("invalid git url: %s", rawURL)
		}
		pathValue = parsed.Path
	}

	pathValue = strings.TrimSpace(strings.TrimPrefix(pathValue, "/"))
	pathValue = strings.TrimSuffix(pathValue, ".git")
	parts := strings.Split(pathValue, "/")
	cleanParts := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		cleanParts = append(cleanParts, p)
	}
	if len(cleanParts) < 2 {
		return "", fmt.Errorf("invalid git url: %s", rawURL)
	}

	owner := cleanParts[len(cleanParts)-2]
	repo := cleanParts[len(cleanParts)-1]
	if owner == "" || repo == "" {
		return "", fmt.Errorf("invalid git url: %s", rawURL)
	}

	return filepath.Join(owner, repo), nil
}

func buildStoreTagsJSON(v any) (datatypes.JSON, error) {
	if v == nil {
		return datatypes.JSON([]byte("[]")), nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(b))
	if trimmed == "" || trimmed == "null" {
		return datatypes.JSON([]byte("[]")), nil
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("tags is not valid json")
	}
	return datatypes.JSON(b), nil
}

// resolveStoreDir 推导 store 的绝对目录：storage.base_dir/store/<path_name>。
//
// store 表不再保存绝对路径（Path 字段已删除），所有需要目录的位置都必须
// 经由这里从 PathName 解析，这样 base_dir 变更只需拷贝目录。
func resolveStoreDir(cfg *config.Config, item *types.Store) string {
	if item == nil || cfg == nil || cfg.Storage == nil {
		return ""
	}
	baseDir := strings.TrimSpace(cfg.Storage.BaseDir)
	pathName := strings.TrimSpace(item.PathName)
	if baseDir == "" || pathName == "" {
		return ""
	}
	return utils.GetWorkflowOrScriptStoreDir(baseDir, pathName)
}

// fillStorePath 把 store 的绝对目录写进响应用字段 store_path。
//
// 目录由 PathName 经 GetWorkflowOrScriptStoreDir 解析，不落库，
// 因此 base_dir 变更后返回结果自动跟着变。
func fillStorePath(cfg *config.Config, item *types.Store) {
	if item == nil {
		return
	}
	item.StorePath = resolveStoreDir(cfg, item)
}

// fillStorePathForPage 为分页结果里的每条 store 记录回填 store_path。
func fillStorePathForPage(cfg *config.Config, result *types.PageResult) {
	if result == nil {
		return
	}
	dtos, ok := result.Data.([]*types.StoreDTO)
	if !ok {
		return
	}
	for _, dto := range dtos {
		if dto == nil {
			continue
		}
		fillStorePath(cfg, &dto.Store)
	}
}

// hydrateStoreMetadataFromStoreFiles 用 store 仓库 HEAD 提交里的导出文件回填 store 元数据。
//
// store 是裸仓库（本地 publish 与远程 clone 都是裸仓库），没有工作区文件，
// 因此 workflow.json / script.json 都从 git 对象里读（readStoreExportJSON），
// 不能用 os.ReadFile。
func hydrateStoreMetadataFromStoreFiles(storeDir string, item *types.Store) error {
	if item == nil {
		return fmt.Errorf("store item is nil")
	}
	storeDir = strings.TrimSpace(storeDir)
	if storeDir == "" {
		return fmt.Errorf("store directory is empty")
	}

	switch strings.ToLower(strings.TrimSpace(item.StoreType)) {
	case "workflow":
		content, err := readStoreExportJSON(storeDir, exportcodec.WorkflowJSONFileName)
		if err != nil {
			return err
		}
		payload := &types.WorkflowJSONExportResponse{}
		if err := json.Unmarshal(content, payload); err != nil {
			return err
		}

		item.Name = firstNonEmptyString(
			stringValueFromMap(payload.Workflow, "name"),
			stringValueFromMap(payload.Workflow, "component_name"),
			item.Name,
		)
		item.Category = firstNonEmptyString(
			stringValueFromMap(payload.Workflow, "category"),
			item.Category,
		)
		// item.Version = firstNonEmptyString(
		// 	stringValueFromMap(payload.Workflow, "version"),
		// 	item.Version,
		// )
		item.Img = firstNonEmptyString(
			stringValueFromMap(payload.Workflow, "img"),
			item.Img,
		)
		// item.Message = firstNonEmptyString(
		// 	stringValueFromMap(payload.Workflow, "message"),
		// 	item.Message,
		// )
		return nil

	case "script":
		content, err := readStoreExportJSON(storeDir, exportcodec.ScriptJSONFileName)
		if err != nil {
			return err
		}
		payload := &types.ScriptJSONExportResponse{}
		if err := json.Unmarshal(content, payload); err != nil {
			return err
		}
		item.Img = firstNonEmptyString(
			stringValueFromMap(payload.Script, "img"),
			item.Img,
		)

		item.Name = firstNonEmptyString(
			stringValueFromMap(payload.Script, "component_name"),
			stringValueFromMap(payload.Script, "name"),
			item.Name,
		)
		item.Category = firstNonEmptyString(
			stringValueFromMap(payload.Script, "category"),
			item.Category,
		)
		// item.Version = firstNonEmptyString(
		// 	stringValueFromMap(payload.Script, "version"),
		// 	item.Version,
		// )
		// item.Message = firstNonEmptyString(
		// 	stringValueFromMap(payload.Script, "message"),
		// 	item.Message,
		// )
		return nil
	}

	return fmt.Errorf("unsupported store_type: %s", item.StoreType)
}

func stringValueFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}
