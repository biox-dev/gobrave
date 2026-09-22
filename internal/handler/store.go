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

// reDownloadStoreRequest 是「检查更新」（/store/redownload）的入参。
//
// store 裸仓库上可以配置多个 remote（origin / github / gitee ...，见 utils.ReadGitRemotes），
// 因此要拉取哪个上游由 RemoteName 指定；为空时回退 origin（下载 store 时 clone 生成的默认上游），
// 与旧调用方（只传 id）保持兼容。
type reDownloadStoreRequest struct {
	ID         int64  `json:"id,string" binding:"required"`
	RemoteName string `json:"remote_name"`
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
	fillStoreRemotes(h.cfg, item)

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
	fillStoreRemotes(h.cfg, item)

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

	// store 表不再保存 url：来源地址现在只存在于 store 裸仓库的 remote 配置里
	// （下载时 clone 生成的 origin），按 remote 地址比对实现「同一个仓库不重复下载」。
	findStore, err := h.findStoreByRemoteURL(c.Request.Context(), repoURL)
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

	// ssh 地址必须显式带凭据（go-git 不会像 OpenSSH 那样自动读默认私钥，
	// 不带凭据时会报 “error creating SSH agent”）：在写库之前先解析，
	// 避免链接失败却在库里留下一条无意义的 store 记录。
	auth, authErr := utils.ResolveGitAuth(repoURL)
	if authErr != nil {
		c.Error(errors.NewInternalServerError("failed to resolve git credentials").WithDetails(authErr.Error()))
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
		URL:  repoURL,
		Auth: auth,
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

	var req reDownloadStoreRequest
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

	// 指定从哪个 remote 拉取：store 仓库上可能配置了多个远端（origin / github / gitee ...），
	// 请求未指定时回退 origin；remote 不存在直接返回 400，而不是让 go-git 报底层错误。
	remoteName, err := resolveStoreRemoteName(targetPath, req.RemoteName)
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	pullErr := pullStoreRepo(c.Request.Context(), repo, targetPath, remoteName)
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
		"remote":     remoteName,
		"up_to_date": stderrs.Is(pullErr, git.NoErrAlreadyUpToDate),
		"message":    "success",
	})
}

// resolveStoreRemoteName 校验「检查更新」要用的 remote 名：
// 为空时回退 origin（git.DefaultRemoteName），并确认该 remote 确实配置在 store 仓库上。
// 仓库没有任何 remote（例如只在本地发布过）时给出可读提示，而不是把 go-git 的
// "remote not found" 当 500 抛出。
func resolveStoreRemoteName(repoPath, requested string) (string, error) {
	name := strings.TrimSpace(requested)
	if name == "" {
		name = git.DefaultRemoteName
	}

	remotes := utils.ReadGitRemotes(repoPath)
	available := make([]string, 0, len(remotes))
	for _, remote := range remotes {
		if remote.Name == name {
			return name, nil
		}
		available = append(available, remote.Name)
	}

	if len(available) == 0 {
		return "", fmt.Errorf("store repository has no remote: nothing to update from")
	}
	return "", fmt.Errorf("remote %q is not configured on the store repository (available: %s)", name, strings.Join(available, ", "))
}

// pullStoreRepo 把 store 仓库从指定 remote 更新到远端最新提交。
//
// store 是裸仓库（本地 publish 与远程 clone 都是裸仓库，没有工作区），
// 裸仓库不能用 Worktree().Pull，改为 fetch <remoteName> 后把本地分支指向同名远端分支；
// 历史遗留的普通仓库仍走 PullContext。两者都以 git.NoErrAlreadyUpToDate 表示无更新。
func pullStoreRepo(ctx context.Context, repo *git.Repository, repoPath, remoteName string) error {
	if repo == nil {
		return fmt.Errorf("git repository is nil")
	}
	if cfg, cfgErr := repo.Config(); cfgErr == nil && cfg.Core.IsBare {
		return utils.FetchBareRepoFromRemote(ctx, repoPath, remoteName)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return err
	}

	// 远端是 ssh 地址时需要显式带凭据，否则 go-git 会回落到 ssh-agent 并报出
	// “error creating SSH agent”（见 utils.ResolveGitAuth 的说明）。
	auth, err := utils.ResolveGitAuth(utils.RemoteURL(repo, remoteName))
	if err != nil {
		return err
	}
	return wt.PullContext(ctx, &git.PullOptions{RemoteName: remoteName, Auth: auth})
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

// fillStoreRemotes 把 store 裸仓库上配置的 git remote 列表写进响应的 remotes 字段。
//
// 与 store_path 一样是响应专属字段（gorm:"-"，不落库）：发布到远程只写仓库的 remote
// 配置，所以这里每次从磁盘实时读取，保证前端看到的 remote 与仓库真实状态一致。
func fillStoreRemotes(cfg *config.Config, item *types.Store) {
	if item == nil {
		return
	}
	item.Remotes = utils.ReadGitRemotes(resolveStoreDir(cfg, item))
}

// findStoreByRemoteURL 在已有 store 中按 remote 地址查找记录，用于下载去重。
//
// store 表已删除 url 列：来源仓库地址现在只存在于 store 裸仓库的 git remote 配置里
// （下载时 clone 生成的 origin，以及发布到远程时添加的 github / gitee remote），
// 因此逐个仓库比对 remote 地址（trim 后精确匹配，语义与旧的 GetStoreByURL 一致）。
// 没有命中时返回 gorm.ErrRecordNotFound，与旧实现的错误语义保持一致。
func (h *StoreHandler) findStoreByRemoteURL(ctx context.Context, repoURL string) (*types.Store, error) {
	repoURL = strings.TrimSpace(repoURL)
	if repoURL == "" {
		return nil, gorm.ErrRecordNotFound
	}

	items, err := h.storeService.ListStore(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if item == nil {
			continue
		}
		for _, remote := range utils.ReadGitRemotes(resolveStoreDir(h.cfg, item)) {
			for _, configured := range remote.URLs {
				if configured == repoURL {
					return item, nil
				}
			}
		}
	}
	return nil, gorm.ErrRecordNotFound
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
