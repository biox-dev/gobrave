package handler

import (
	stderrs "errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gobravedev/gobrave/internal/config"
	appErrors "github.com/gobravedev/gobrave/internal/errors"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
	"github.com/gobravedev/gobrave/internal/utils"
	"gorm.io/gorm"
)

type FileHandler struct {
	cfg            *config.Config
	projectService interfaces.ProjectService
}

func NewFileHandler(cfg *config.Config, projectService interfaces.ProjectService) *FileHandler {
	return &FileHandler{cfg: cfg, projectService: projectService}
}

type fileItem struct {
	Name     string `json:"name"`
	IsDir    bool   `json:"is_dir"`
	Size     *int64 `json:"size"`
	Modified int64  `json:"modified"`
	URL      string `json:"url"`
}

type listProjectDirResponse struct {
	Items     []fileItem `json:"items"`
	Dir       string     `json:"dir"`
	UrlPrefix string     `json:"url_prefix"`
	Total     int        `json:"total"`
	Page      int        `json:"page"`
	Limit     int        `json:"limit"`
}

func (h *FileHandler) ListProjectDir(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	scope := strings.ToLower(strings.TrimSpace(c.DefaultQuery("type", "data")))
	project, rootDir, err := h.resolveUserRootDir(c, userID, scope)
	prefix := fmt.Sprintf("/data-project/%s", project.ProjectID)

	if err != nil {
		c.Error(err)
		return
	}

	relPath, err := sanitizeRelativePath(c.DefaultQuery("path", ""))
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid path").WithDetails(err.Error()))
		return
	}

	targetDir, currentPath, err := resolvePathUnderRoot(rootDir, relPath)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid path").WithDetails(err.Error()))
		return
	}

	if err := ensureDirExists(targetDir, rootDir); err != nil {
		c.Error(err)
		return
	}

	keyword := strings.ToLower(strings.TrimSpace(c.DefaultQuery("keyword", "")))
	page := parsePositiveInt(c.DefaultQuery("page", "1"), 1)
	limit := parsePositiveInt(c.DefaultQuery("limit", "20"), 20)
	if limit > 200 {
		limit = 200
	}

	entries, err := os.ReadDir(targetDir)
	if err != nil {
		c.Error(appErrors.NewInternalServerError("failed to read directory").WithDetails(err.Error()))
		return
	}

	items := make([]fileItem, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if keyword != "" && !strings.Contains(strings.ToLower(name), keyword) {
			continue
		}

		info, statErr := entry.Info()
		if statErr != nil {
			continue
		}

		var size *int64
		if !entry.IsDir() {
			s := info.Size()
			size = &s
		}

		itemURL := joinURLPath(prefix, currentPath, name)

		items = append(items, fileItem{
			Name:     name,
			IsDir:    entry.IsDir(),
			Size:     size,
			Modified: info.ModTime().Unix(),
			URL:      itemURL,
		})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].IsDir != items[j].IsDir {
			return items[i].IsDir
		}
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})

	total := len(items)
	start := (page - 1) * limit
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}

	c.JSON(http.StatusOK, listProjectDirResponse{
		Items:     items[start:end],
		Dir:       currentPath,
		UrlPrefix: prefix,
		Total:     total,
		Page:      page,
		Limit:     limit,
	})
}

type createDirRequest struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

func (h *FileHandler) CreateDir(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req createDirRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(appErrors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	_, rootDir, err := h.resolveUserRootDir(c, userID, req.Type)
	if err != nil {
		c.Error(err)
		return
	}

	relPath, err := sanitizeRelativePath(req.Path)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid path").WithDetails(err.Error()))
		return
	}

	targetDir, currentPath, err := resolvePathUnderRoot(rootDir, relPath)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid path").WithDetails(err.Error()))
		return
	}

	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		c.Error(appErrors.NewInternalServerError("failed to create directory").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"path": currentPath})
}

type createFileRequest struct {
	Path      string `json:"path"`
	Name      string `json:"name" binding:"required"`
	Content   string `json:"content"`
	Overwrite bool   `json:"overwrite"`
	Type      string `json:"type"`
}

func (h *FileHandler) CreateFile(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req createFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(appErrors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	_, rootDir, err := h.resolveUserRootDir(c, userID, req.Type)
	if err != nil {
		c.Error(err)
		return
	}

	relPath, err := sanitizeRelativePath(req.Path)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid path").WithDetails(err.Error()))
		return
	}
	dirPath, _, err := resolvePathUnderRoot(rootDir, relPath)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid path").WithDetails(err.Error()))
		return
	}

	fileName, err := utils.SafeFileName(req.Name)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid file name").WithDetails(err.Error()))
		return
	}

	targetPath, err := utils.SafePathUnderBase(rootDir, filepath.Join(dirPath, fileName))
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid target path").WithDetails(err.Error()))
		return
	}

	if _, err := os.Stat(targetPath); err == nil && !req.Overwrite {
		c.Error(appErrors.NewBadRequestError("file already exists"))
		return
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		c.Error(appErrors.NewInternalServerError("failed to create parent directory").WithDetails(err.Error()))
		return
	}

	if err := os.WriteFile(targetPath, []byte(req.Content), 0o644); err != nil {
		c.Error(appErrors.NewInternalServerError("failed to create file").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"path": filepath.ToSlash(filepath.Join("/", relPath, fileName))})
}

type moveFileRequest struct {
	SourcePath string `json:"source_path" binding:"required"`
	TargetPath string `json:"target_path" binding:"required"`
	Overwrite  bool   `json:"overwrite"`
	Type       string `json:"type"`
}

func (h *FileHandler) Move(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req moveFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(appErrors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	_, rootDir, err := h.resolveUserRootDir(c, userID, req.Type)
	if err != nil {
		c.Error(err)
		return
	}

	srcRel, err := sanitizeRelativePath(req.SourcePath)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid source_path").WithDetails(err.Error()))
		return
	}
	dstRel, err := sanitizeRelativePath(req.TargetPath)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid target_path").WithDetails(err.Error()))
		return
	}

	sourcePath, srcPath, err := resolvePathUnderRoot(rootDir, srcRel)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid source_path").WithDetails(err.Error()))
		return
	}
	targetPath, dstPath, err := resolvePathUnderRoot(rootDir, dstRel)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid target_path").WithDetails(err.Error()))
		return
	}

	if srcPath == "/" {
		c.Error(appErrors.NewValidationError("cannot move root directory"))
		return
	}

	if _, err := os.Stat(sourcePath); err != nil {
		if os.IsNotExist(err) {
			c.Error(appErrors.NewNotFoundError("source path not found"))
			return
		}
		c.Error(appErrors.NewInternalServerError("failed to access source path").WithDetails(err.Error()))
		return
	}

	if strings.HasPrefix(targetPath+string(filepath.Separator), sourcePath+string(filepath.Separator)) {
		c.Error(appErrors.NewValidationError("cannot move directory into its own subdirectory"))
		return
	}

	if _, err := os.Stat(targetPath); err == nil {
		if !req.Overwrite {
			c.Error(appErrors.NewBadRequestError("target path already exists"))
			return
		}
		if err := os.RemoveAll(targetPath); err != nil {
			c.Error(appErrors.NewInternalServerError("failed to replace target path").WithDetails(err.Error()))
			return
		}
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		c.Error(appErrors.NewInternalServerError("failed to create target parent directory").WithDetails(err.Error()))
		return
	}

	if err := os.Rename(sourcePath, targetPath); err != nil {
		c.Error(appErrors.NewInternalServerError("failed to move file").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"source_path": srcPath, "target_path": dstPath})
}

type deleteFileRequest struct {
	Path string `json:"path" binding:"required"`
	Type string `json:"type"`
}

func (h *FileHandler) Delete(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var req deleteFileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.Error(appErrors.NewValidationError("invalid request parameters").WithDetails(err.Error()))
		return
	}

	_, rootDir, err := h.resolveUserRootDir(c, userID, req.Type)
	if err != nil {
		c.Error(err)
		return
	}

	relPath, err := sanitizeRelativePath(req.Path)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid path").WithDetails(err.Error()))
		return
	}

	targetPath, currentPath, err := resolvePathUnderRoot(rootDir, relPath)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid path").WithDetails(err.Error()))
		return
	}
	if currentPath == "/" {
		c.Error(appErrors.NewValidationError("cannot delete root directory"))
		return
	}

	if _, err := os.Stat(targetPath); err != nil {
		if os.IsNotExist(err) {
			c.Error(appErrors.NewNotFoundError("path not found"))
			return
		}
		c.Error(appErrors.NewInternalServerError("failed to access path").WithDetails(err.Error()))
		return
	}

	if err := os.RemoveAll(targetPath); err != nil {
		c.Error(appErrors.NewInternalServerError("failed to delete path").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{"path": currentPath})
}

func (h *FileHandler) Upload(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	scope := c.DefaultPostForm("type", "data")
	_, rootDir, err := h.resolveUserRootDir(c, userID, scope)
	if err != nil {
		c.Error(err)
		return
	}

	relPath, err := sanitizeRelativePath(c.DefaultPostForm("path", ""))
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid path").WithDetails(err.Error()))
		return
	}

	targetDir, currentPath, err := resolvePathUnderRoot(rootDir, relPath)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid path").WithDetails(err.Error()))
		return
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		c.Error(appErrors.NewInternalServerError("failed to create directory").WithDetails(err.Error()))
		return
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.Error(appErrors.NewValidationError("missing upload file").WithDetails(err.Error()))
		return
	}

	fileName, err := utils.SafeFileName(fileHeader.Filename)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid file name").WithDetails(err.Error()))
		return
	}

	targetPath, err := utils.SafePathUnderBase(rootDir, filepath.Join(targetDir, fileName))
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid target path").WithDetails(err.Error()))
		return
	}

	if _, err := os.Stat(targetPath); err == nil {
		c.Error(appErrors.NewBadRequestError("file already exists"))
		return
	}

	src, err := fileHeader.Open()
	if err != nil {
		c.Error(appErrors.NewInternalServerError("failed to open upload file").WithDetails(err.Error()))
		return
	}
	defer src.Close()

	dst, err := os.Create(targetPath)
	if err != nil {
		c.Error(appErrors.NewInternalServerError("failed to create target file").WithDetails(err.Error()))
		return
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		c.Error(appErrors.NewInternalServerError("failed to write file").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"path": filepath.ToSlash(filepath.Join(currentPath, fileName)),
		"name": fileName,
		"size": fileHeader.Size,
	})
}

func (h *FileHandler) Download(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	scope := strings.ToLower(strings.TrimSpace(c.DefaultQuery("type", "data")))
	_, rootDir, err := h.resolveUserRootDir(c, userID, scope)
	if err != nil {
		c.Error(err)
		return
	}

	relPath, err := sanitizeRelativePath(c.Query("path"))
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid path").WithDetails(err.Error()))
		return
	}
	if relPath == "" {
		c.Error(appErrors.NewValidationError("path is required"))
		return
	}

	filePath, _, err := resolvePathUnderRoot(rootDir, relPath)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid path").WithDetails(err.Error()))
		return
	}

	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			c.Error(appErrors.NewNotFoundError("file not found"))
			return
		}
		c.Error(appErrors.NewInternalServerError("failed to access file").WithDetails(err.Error()))
		return
	}
	if info.IsDir() {
		c.Error(appErrors.NewValidationError("path is a directory"))
		return
	}

	c.FileAttachment(filePath, filepath.Base(filePath))
}

type searchFileItem struct {
	Path     string `json:"path"`
	Name     string `json:"name"`
	IsDir    bool   `json:"is_dir"`
	Size     *int64 `json:"size"`
	Modified int64  `json:"modified"`
}

func (h *FileHandler) Search(c *gin.Context) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	keyword := strings.ToLower(strings.TrimSpace(c.Query("keyword")))
	if keyword == "" {
		c.Error(appErrors.NewValidationError("keyword is required"))
		return
	}

	scope := strings.ToLower(strings.TrimSpace(c.DefaultQuery("type", "data")))
	_, rootDir, err := h.resolveUserRootDir(c, userID, scope)
	if err != nil {
		c.Error(err)
		return
	}

	relPath, err := sanitizeRelativePath(c.DefaultQuery("path", ""))
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid path").WithDetails(err.Error()))
		return
	}
	startDir, _, err := resolvePathUnderRoot(rootDir, relPath)
	if err != nil {
		c.Error(appErrors.NewValidationError("invalid path").WithDetails(err.Error()))
		return
	}

	if err := ensureDirExists(startDir, rootDir); err != nil {
		c.Error(err)
		return
	}

	page := parsePositiveInt(c.DefaultQuery("page", "1"), 1)
	limit := parsePositiveInt(c.DefaultQuery("limit", "20"), 20)
	if limit > 200 {
		limit = 200
	}

	results := make([]searchFileItem, 0)
	walkErr := filepath.WalkDir(startDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == startDir {
			return nil
		}
		if !strings.Contains(strings.ToLower(d.Name()), keyword) {
			return nil
		}

		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}

		rel, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		rel = strings.TrimPrefix(rel, "./")

		var size *int64
		if !d.IsDir() {
			s := info.Size()
			size = &s
		}

		results = append(results, searchFileItem{
			Path:     "/" + strings.TrimPrefix(rel, "/"),
			Name:     d.Name(),
			IsDir:    d.IsDir(),
			Size:     size,
			Modified: info.ModTime().Unix(),
		})
		return nil
	})
	if walkErr != nil {
		c.Error(appErrors.NewInternalServerError("failed to search files").WithDetails(walkErr.Error()))
		return
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Modified > results[j].Modified
	})

	total := len(results)
	start := (page - 1) * limit
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}

	c.JSON(http.StatusOK, gin.H{
		"items": results[start:end],
		"total": total,
		"page":  page,
		"limit": limit,
	})
}

func (h *FileHandler) resolveUserRootDir(c *gin.Context, userID, scope string) (*types.Project, string, error) {
	if h.cfg == nil || h.cfg.Storage == nil {
		return nil, "", appErrors.NewInternalServerError("storage config is not available")
	}

	baseDir := strings.TrimSpace(h.cfg.Storage.BaseDir)
	if baseDir == "" {
		return nil, "", appErrors.NewInternalServerError("storage.base_dir is empty")
	}
	project, dir, err := h.projectService.GetActiveProjectDirByUserID(c.Request.Context(), userID, baseDir)
	if err != nil {
		return nil, "", mapProjectErr(err)
	}
	return project, dir, nil
}

func mapProjectErr(err error) error {
	if err == nil {
		return nil
	}
	if stderrs.Is(err, gorm.ErrRecordNotFound) {
		return appErrors.NewNotFoundError("active project not found")
	}
	return appErrors.NewInternalServerError("failed to resolve active project").WithDetails(err.Error())
}

func sanitizeRelativePath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "/" {
		return "", nil
	}

	raw = filepath.ToSlash(raw)
	raw = strings.TrimPrefix(raw, "/")
	parts := strings.Split(raw, "/")
	safe := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			return "", appErrors.NewValidationError("path traversal is not allowed")
		default:
			safe = append(safe, part)
		}
	}

	if len(safe) == 0 {
		return "", nil
	}

	return filepath.Join(safe...), nil
}

func resolvePathUnderRoot(rootDir, relativePath string) (string, string, error) {
	target := rootDir
	normalized := "/"
	if relativePath != "" {
		target = filepath.Join(rootDir, relativePath)
		normalized = "/" + filepath.ToSlash(relativePath)
	}

	safePath, err := utils.SafePathUnderBase(rootDir, target)
	if err != nil {
		return "", "", err
	}
	return safePath, normalized, nil
}

func ensureDirExists(targetDir, rootDir string) error {
	info, err := os.Stat(targetDir)
	if err == nil {
		if !info.IsDir() {
			return appErrors.NewValidationError("path is not a directory")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return appErrors.NewInternalServerError("failed to access directory").WithDetails(err.Error())
	}

	safeRoot, safeErr := utils.SafePathUnderBase(rootDir, rootDir)
	if safeErr != nil {
		return appErrors.NewValidationError("invalid root path").WithDetails(safeErr.Error())
	}
	if targetDir == safeRoot {
		if mkErr := os.MkdirAll(targetDir, 0o755); mkErr != nil {
			return appErrors.NewInternalServerError("failed to create root directory").WithDetails(mkErr.Error())
		}
		return nil
	}

	return appErrors.NewValidationError("invalid path")
}

func parsePositiveInt(raw string, fallback int) int {
	v, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}

func joinURLPath(parts ...string) string {
	cleanParts := make([]string, 0, len(parts))
	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p == "" || p == "/" {
			continue
		}
		cleanParts = append(cleanParts, strings.Trim(p, "/"))
	}

	if len(cleanParts) == 0 {
		return "/"
	}

	return "/" + strings.Join(cleanParts, "/")
}
