package handler

import (
	"encoding/json"
	stderrs "errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	git "github.com/go-git/go-git/v5"
	"github.com/gobravedev/gobrave/internal/config"
	"github.com/gobravedev/gobrave/internal/errors"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"github.com/gobravedev/gobrave/internal/utils"
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
		storePath := strings.TrimSpace(storeItem.Path)
		if storePath != "" {
			if h.cfg == nil || h.cfg.Storage == nil || strings.TrimSpace(h.cfg.Storage.BaseDir) == "" {
				c.Error(errors.NewInternalServerError("storage base dir is not configured"))
				return
			}

			storeRoot := filepath.Join(strings.TrimSpace(h.cfg.Storage.BaseDir), "store")
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

	pathName, err := buildStorePathNameFromGitURL(repoURL)
	if err != nil {
		c.Error(errors.NewValidationError("invalid git url").WithDetails(err.Error()))
		return
	}

	storeRoot := filepath.Join(strings.TrimSpace(h.cfg.Storage.BaseDir), "store")
	targetPath := filepath.Join(storeRoot, pathName)
	targetPath, err = utils.SafePathUnderBase(storeRoot, targetPath)
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

	publishURLs, err := buildPublishURLsJSON(pathName)
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to build publish urls").WithDetails(err.Error()))
		return
	}

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
		storeName = pathName
	}
	origin := strings.TrimSpace(req.Origin)
	if origin == "" {
		origin = "remote"
	}

	item := &types.Store{
		StoreType:   storeType,
		Name:        storeName,
		Origin:      origin,
		URL:         repoURL,
		Status:      "running",
		Path:        targetPath,
		PathName:    pathName,
		Category:    strings.TrimSpace(req.Category),
		Tags:        tagsJSON,
		Img:         strings.TrimSpace(req.Img),
		PublishURLs: publishURLs,
		Version:     strings.TrimSpace(req.Version),
		Message:     strings.TrimSpace(req.Message),
		Log:         fmt.Sprintf("clone %s %s", repoURL, targetPath),
	}

	if err := h.storeService.CreateStore(c.Request.Context(), item); err != nil {
		handleDataError(c, err, "failed to create store record")
		return
	}

	_, cloneErr := git.PlainCloneContext(c.Request.Context(), targetPath, false, &git.CloneOptions{
		URL: repoURL,
	})
	if cloneErr != nil {
		item.Status = "failed"
		item.Message = cloneErr.Error()
		item.Log = cloneErr.Error()
		if updateErr := h.storeService.UpdateStore(c.Request.Context(), item); updateErr != nil {
			c.Error(errors.NewInternalServerError("git clone failed and failed to update store status").WithDetails(fmt.Sprintf("clone err: %v; update err: %v", cloneErr, updateErr)))
			return
		}
		c.Error(errors.NewInternalServerError("git clone failed").WithDetails(cloneErr.Error()))
		return
	}

	if metadataErr := hydrateStoreMetadataFromStoreFiles(item); metadataErr != nil {
		item.Status = "failed"
		item.Message = metadataErr.Error()
		item.Log = metadataErr.Error()
		if updateErr := h.storeService.UpdateStore(c.Request.Context(), item); updateErr != nil {
			c.Error(errors.NewInternalServerError("failed to parse store metadata and failed to update store status").WithDetails(fmt.Sprintf("metadata err: %v; update err: %v", metadataErr, updateErr)))
			return
		}
		c.Error(errors.NewInternalServerError("failed to parse store metadata").WithDetails(metadataErr.Error()))
		return
	}

	item.Status = "done"
	item.Log = "clone completed"
	if err := h.storeService.UpdateStore(c.Request.Context(), item); err != nil {
		handleDataError(c, err, "failed to update store status")
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"store_id":       item.ID,
		"already_exists": false,
		"path":           item.Path,
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

	targetPath := strings.TrimSpace(item.Path)
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

	wt, err := repo.Worktree()
	if err != nil {
		c.Error(errors.NewInternalServerError("failed to open git worktree").WithDetails(err.Error()))
		return
	}

	pullErr := wt.PullContext(c.Request.Context(), &git.PullOptions{RemoteName: "origin"})
	if pullErr != nil && !stderrs.Is(pullErr, git.NoErrAlreadyUpToDate) {
		item.Status = "done"
		item.Log = pullErr.Error()
		item.Message = pullErr.Error()
		if updateErr := h.storeService.UpdateStore(c.Request.Context(), item); updateErr != nil {
			c.Error(errors.NewInternalServerError("git pull failed and failed to update store info").WithDetails(fmt.Sprintf("pull err: %v; update err: %v", pullErr, updateErr)))
			return
		}
		c.Error(errors.NewInternalServerError("git pull failed").WithDetails(pullErr.Error()))
		return
	}

	if metadataErr := hydrateStoreMetadataFromStoreFiles(item); metadataErr != nil {
		item.Log = metadataErr.Error()
		item.Message = metadataErr.Error()
		if updateErr := h.storeService.UpdateStore(c.Request.Context(), item); updateErr != nil {
			c.Error(errors.NewInternalServerError("failed to parse store metadata and failed to update store info").WithDetails(fmt.Sprintf("metadata err: %v; update err: %v", metadataErr, updateErr)))
			return
		}
		c.Error(errors.NewInternalServerError("failed to parse store metadata").WithDetails(metadataErr.Error()))
		return
	}

	item.Status = "done"
	if stderrs.Is(pullErr, git.NoErrAlreadyUpToDate) {
		item.Log = "already up to date"
		item.Message = "already up to date"
	} else {
		item.Log = "pull completed"
		item.Message = "pull completed"
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

func buildStorePathNameFromGitURL(rawURL string) (string, error) {
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

func hydrateStoreMetadataFromStoreFiles(item *types.Store) error {
	if item == nil {
		return fmt.Errorf("store item is nil")
	}

	switch strings.ToLower(strings.TrimSpace(item.StoreType)) {
	case "workflow":
		workflowJSONPath, err := resolveStoreWorkflowJSONPath(item.Path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(workflowJSONPath)
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
		item.Version = firstNonEmptyString(
			stringValueFromMap(payload.Workflow, "version"),
			item.Version,
		)
		item.Message = firstNonEmptyString(
			stringValueFromMap(payload.Workflow, "message"),
			item.Message,
		)
		return nil

	case "script":
		scriptJSONPath, err := resolveStoreScriptJSONPath(item.Path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(scriptJSONPath)
		if err != nil {
			return err
		}
		payload := &types.ScriptJSONExportResponse{}
		if err := json.Unmarshal(content, payload); err != nil {
			return err
		}

		item.Name = firstNonEmptyString(
			stringValueFromMap(payload.Script, "component_name"),
			stringValueFromMap(payload.Script, "name"),
			item.Name,
		)
		item.Category = firstNonEmptyString(
			stringValueFromMap(payload.Script, "category"),
			item.Category,
		)
		item.Version = firstNonEmptyString(
			stringValueFromMap(payload.Script, "version"),
			item.Version,
		)
		item.Message = firstNonEmptyString(
			stringValueFromMap(payload.Script, "message"),
			item.Message,
		)
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
