package handler

import (
	stderrs "errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 组件封面（script / workflow 的 img 列）约定：
//
//   - 文件落在组件自身目录里：
//     script   -> {base}/data/{project_id}/pipeline/script/{component_id}
//     workflow -> {base}/data/{project_id}/pipeline/workflow/{relation_id}
//   - 文件名统一为 image.<ext>，ext 取原图后缀（由文件头嗅探得到）。
//   - DB 的 img 列只保存这个文件名（不带任何路径），完整地址由 ID 推导：
//     GET /api/v1/script/{id}/image
//     GET /api/v1/workflow/{id}/image
//   - 图片缺失时不返回 404，而是返回占位图，前端 <img> 不会出现裂图。
const componentImageBaseName = "image"

// componentImageKind 区分 script / workflow 两类组件封面，同时用于拼出对外 URL。
type componentImageKind string

const (
	componentImageKindScript   componentImageKind = "script"
	componentImageKindWorkflow componentImageKind = "workflow"
)

// placeholderComponentImageSVG 在组件还没有封面（或封面文件丢失）时返回。
const placeholderComponentImageSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="240" height="160" viewBox="0 0 240 160" role="img" aria-label="No image">
  <rect width="240" height="160" fill="#f5f5f5"/>
  <rect x="0.5" y="0.5" width="239" height="159" fill="none" stroke="#d9d9d9"/>
  <g fill="none" stroke="#bfbfbf" stroke-width="3" stroke-linejoin="round" stroke-linecap="round">
    <rect x="84" y="54" width="72" height="52" rx="5"/>
    <path d="M87 100l20-22 15 16 10-9 18 19"/>
  </g>
  <circle cx="122" cy="70" r="5" fill="#bfbfbf"/>
  <text x="120" y="132" font-family="Helvetica, Arial, sans-serif" font-size="14" fill="#8c8c8c" text-anchor="middle">No Image</text>
</svg>`

// parseComponentIDParam 解析组件主键，只接受 int64（正数）。
// 优先取路径参数，其次兼容表单/查询串里的 id，方便 curl 与表单调试。
func parseComponentIDParam(c *gin.Context, pathParam string) (int64, error) {
	raw := strings.TrimSpace(c.Param(pathParam))
	if raw == "" {
		raw = strings.TrimSpace(c.PostForm("id"))
	}
	if raw == "" {
		raw = strings.TrimSpace(c.Query("id"))
	}
	if raw == "" {
		return 0, fmt.Errorf("%s is required", pathParam)
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("%s must be a positive int64, got %q", pathParam, raw)
	}
	return id, nil
}

// resolveComponentImageExt 读取文件头判断图片类型，返回后缀并把 reader 复位以便随后落盘。
func resolveComponentImageExt(src io.ReadSeeker) (string, error) {
	buf := make([]byte, 512)
	n, err := io.ReadFull(src, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", fmt.Errorf("failed to read upload file: %w", err)
	}

	contentType := http.DetectContentType(buf[:n])
	ext, ok := imageMimeToExt[contentType]
	if !ok {
		return "", fmt.Errorf("unsupported image type: %s", contentType)
	}

	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return "", fmt.Errorf("failed to rewind upload file: %w", err)
	}
	return ext, nil
}

// saveComponentImage 校验 multipart 表单里名为 file 的图片，落盘到 dir，
// 返回统一后的文件名（image.<ext>）。同目录下旧的 image.* 会被清掉，保证只有一张封面。
func saveComponentImage(c *gin.Context, baseDir, dir string) (string, error) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return "", errors.NewValidationError("missing upload file").WithDetails(err.Error())
	}
	if fileHeader.Size <= 0 {
		return "", errors.NewValidationError("empty upload file")
	}
	if fileHeader.Size > maxImageUploadSize {
		return "", errors.NewValidationError("image is too large").WithDetails("max size is 10MB")
	}

	src, err := fileHeader.Open()
	if err != nil {
		return "", errors.NewInternalServerError("failed to open upload file").WithDetails(err.Error())
	}
	defer src.Close()

	ext, err := resolveComponentImageExt(src)
	if err != nil {
		return "", errors.NewValidationError(err.Error())
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", errors.NewInternalServerError("failed to create image directory").WithDetails(err.Error())
	}

	removeComponentImageFiles(dir)

	filename := componentImageBaseName + ext
	targetPath, err := utils.SafePathUnderBase(baseDir, filepath.Join(dir, filename))
	if err != nil {
		return "", errors.NewValidationError("invalid image path").WithDetails(err.Error())
	}
	if err := c.SaveUploadedFile(fileHeader, targetPath); err != nil {
		return "", errors.NewInternalServerError("failed to save upload file").WithDetails(err.Error())
	}
	return filename, nil
}

// removeComponentImageFiles 删除目录下旧的 image.* 封面，忽略不存在的目录。
func removeComponentImageFiles(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == componentImageBaseName || strings.HasPrefix(name, componentImageBaseName+".") {
			_ = os.Remove(filepath.Join(dir, name))
		}
	}
}

// resolveComponentImageFile 返回目录内可用的封面文件名：
// 优先使用 img 列里保存的文件名（要求是纯文件名），否则回退到目录里的 image.*。
func resolveComponentImageFile(dir, img string) string {
	name := strings.TrimSpace(img)
	if name != "" && !strings.ContainsAny(name, `/\`) && !strings.Contains(name, "..") {
		if info, err := os.Stat(filepath.Join(dir, name)); err == nil && !info.IsDir() {
			return name
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == componentImageBaseName || strings.HasPrefix(name, componentImageBaseName+".") {
			return name
		}
	}
	return ""
}

// serveComponentImage 输出图片流；文件缺失时返回占位图。
func serveComponentImage(c *gin.Context, baseDir, dir, img string) {
	filename := resolveComponentImageFile(dir, img)
	if filename == "" {
		servePlaceholderImage(c)
		return
	}

	targetPath, err := utils.SafePathUnderBase(baseDir, filepath.Join(dir, filename))
	if err != nil {
		servePlaceholderImage(c)
		return
	}

	// 文件名固定（image.<ext>），内容会被覆盖，必须禁用强缓存。
	c.Header("Cache-Control", "no-cache")
	c.File(targetPath)
}

// servePlaceholderImage 返回内置的占位图，避免前端出现裂图。
func servePlaceholderImage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "image/svg+xml; charset=utf-8", []byte(placeholderComponentImageSVG))
}

// componentImageAPIPath 返回可直接用于 <img src> 的接口路径（含 /api/v1 前缀）。
func componentImageAPIPath(kind componentImageKind, id int64) string {
	return fmt.Sprintf("/api/v1/%s/%d/image", kind, id)
}

// resolveScriptImageDir 通过 script.ProjectID（DB 自增）找到项目，拼出脚本目录。
func (h *WorkflowHandler) resolveScriptImageDir(c *gin.Context, projectID int64, scriptID string) (string, error) {
	if strings.TrimSpace(scriptID) == "" {
		return "", errors.NewValidationError("script component_id is empty")
	}

	project, err := h.projectService.GetProjectByID(c.Request.Context(), projectID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return "", errors.NewNotFoundError("project not found")
		}
		return "", errors.NewInternalServerError("failed to get project").WithDetails(err.Error())
	}
	return utils.GetScriptFileDir(h.cfg.Storage.BaseDir, project.ProjectID, scriptID), nil
}

// resolveWorkflowImageDir 通过 workflow.ProjectID（DB 自增）找到项目，拼出 workflow 目录。
func (h *WorkflowHandler) resolveWorkflowImageDir(c *gin.Context, projectID int64, workflowID string) (string, error) {
	if strings.TrimSpace(workflowID) == "" {
		return "", errors.NewValidationError("workflow relation_id is empty")
	}

	project, err := h.projectService.GetProjectByID(c.Request.Context(), projectID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			return "", errors.NewNotFoundError("project not found")
		}
		return "", errors.NewInternalServerError("failed to get project").WithDetails(err.Error())
	}
	return utils.GetWorkflowFileDir(h.cfg.Storage.BaseDir, project.ProjectID, workflowID), nil
}

// UploadScriptImage godoc
// @Summary      上传脚本组件封面
// @Description  multipart/form-data 上传（字段名 file）。保存到脚本目录下的 image.<原图后缀>，
// @Description  并把纯文件名写入 pipeline_components.img。scriptId 为 int64 主键。
// @Tags         工作流
// @Accept       mpfd
// @Produce      json
// @Param        scriptId  path      int64                  true  "脚本主键 ID（int64）"
// @Param        file      formData  file                   true  "图片文件"
// @Success      200       {object}  map[string]interface{} "上传成功，返回 url/img/name"
// @Failure      400       {object}  errors.AppError        "参数错误"
// @Failure      401       {object}  errors.AppError        "未认证"
// @Failure      404       {object}  errors.AppError        "脚本或项目不存在"
// @Failure      500       {object}  errors.AppError        "服务器错误"
// @Security     Bearer
// @Router       /script/{scriptId}/image [post]
func (h *WorkflowHandler) UploadScriptImage(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	scriptID, err := parseComponentIDParam(c, "scriptId")
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	script, err := h.workflowService.GetScriptByID(c.Request.Context(), scriptID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("script component not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to query script component").WithDetails(err.Error()))
		return
	}

	scriptDir, err := h.resolveScriptImageDir(c, script.ProjectID, script.ScriptID)
	if err != nil {
		c.Error(err)
		return
	}

	filename, err := saveComponentImage(c, h.cfg.Storage.BaseDir, scriptDir)
	if err != nil {
		c.Error(err)
		return
	}

	script.Img = filename
	// UpdateScript 是「整体替换」语义，这里传的是刚从库里读出的完整记录，只改了 Img。
	if err := h.workflowService.UpdateScript(c.Request.Context(), script); err != nil {
		c.Error(errors.NewInternalServerError("failed to update script image").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"url":  componentImageAPIPath(componentImageKindScript, scriptID),
		"img":  filename,
		"name": filename,
	})
}

// UploadWorkflowImage godoc
// @Summary      上传工作流封面
// @Description  multipart/form-data 上传（字段名 file）。保存到 workflow 目录下的 image.<原图后缀>，
// @Description  并把纯文件名写入 pipeline_components_relation.img。workflowId 为 int64 主键。
// @Tags         工作流
// @Accept       mpfd
// @Produce      json
// @Param        workflowId  path      int64                  true  "工作流主键 ID（int64）"
// @Param        file        formData  file                   true  "图片文件"
// @Success      200         {object}  map[string]interface{} "上传成功，返回 url/img/name"
// @Failure      400         {object}  errors.AppError        "参数错误"
// @Failure      401         {object}  errors.AppError        "未认证"
// @Failure      404         {object}  errors.AppError        "工作流或项目不存在"
// @Failure      500         {object}  errors.AppError        "服务器错误"
// @Security     Bearer
// @Router       /workflow/{workflowId}/image [post]
func (h *WorkflowHandler) UploadWorkflowImage(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	workflowID, err := parseComponentIDParam(c, "workflowId")
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	workflow, err := h.workflowService.GetWorkflowByID(c.Request.Context(), workflowID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("workflow not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to query workflow").WithDetails(err.Error()))
		return
	}

	workflowDir, err := h.resolveWorkflowImageDir(c, workflow.ProjectID, workflow.WorkflowID)
	if err != nil {
		c.Error(err)
		return
	}

	filename, err := saveComponentImage(c, h.cfg.Storage.BaseDir, workflowDir)
	if err != nil {
		c.Error(err)
		return
	}

	workflow.Img = filename
	// UpdateWorkflow 同样是「整体替换」语义，传完整记录 + 新 Img。
	if err := h.workflowService.UpdateWorkflow(c.Request.Context(), workflow); err != nil {
		c.Error(errors.NewInternalServerError("failed to update workflow image").WithDetails(err.Error()))
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"url":  componentImageAPIPath(componentImageKindWorkflow, workflowID),
		"img":  filename,
		"name": filename,
	})
}

// GetScriptImage godoc
// @Summary      获取脚本组件封面图片流
// @Description  根据 int64 主键读取脚本目录下的封面图片；图片不存在时返回内置占位图（200 + image/svg+xml）。
// @Tags         工作流
// @Produce      image/svg+xml
// @Param        scriptId  path  int64  true  "脚本主键 ID（int64）"
// @Success      200  {file}    binary  "图片流或占位图"
// @Failure      400  {object}  errors.AppError  "参数错误"
// @Failure      401  {object}  errors.AppError  "未认证"
// @Failure      404  {object}  errors.AppError  "脚本不存在"
// @Failure      500  {object}  errors.AppError  "服务器错误"
// @Security     Bearer
// @Router       /script/{scriptId}/image [get]
func (h *WorkflowHandler) GetScriptImage(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	scriptID, err := parseComponentIDParam(c, "scriptId")
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	script, err := h.workflowService.GetScriptByID(c.Request.Context(), scriptID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("script component not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to query script component").WithDetails(err.Error()))
		return
	}

	scriptDir, err := h.resolveScriptImageDir(c, script.ProjectID, script.ScriptID)
	if err != nil {
		// 项目被删掉时没必要报 500，直接给占位图即可。
		servePlaceholderImage(c)
		return
	}

	serveComponentImage(c, h.cfg.Storage.BaseDir, scriptDir, script.Img)
}

// GetWorkflowImage godoc
// @Summary      获取工作流封面图片流
// @Description  根据 int64 主键读取 workflow 目录下的封面图片；图片不存在时返回内置占位图（200 + image/svg+xml）。
// @Tags         工作流
// @Produce      image/svg+xml
// @Param        workflowId  path  int64  true  "工作流主键 ID（int64）"
// @Success      200  {file}    binary  "图片流或占位图"
// @Failure      400  {object}  errors.AppError  "参数错误"
// @Failure      401  {object}  errors.AppError  "未认证"
// @Failure      404  {object}  errors.AppError  "工作流不存在"
// @Failure      500  {object}  errors.AppError  "服务器错误"
// @Security     Bearer
// @Router       /workflow/{workflowId}/image [get]
func (h *WorkflowHandler) GetWorkflowImage(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	workflowID, err := parseComponentIDParam(c, "workflowId")
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	workflow, err := h.workflowService.GetWorkflowByID(c.Request.Context(), workflowID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("workflow not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to query workflow").WithDetails(err.Error()))
		return
	}

	workflowDir, err := h.resolveWorkflowImageDir(c, workflow.ProjectID, workflow.WorkflowID)
	if err != nil {
		servePlaceholderImage(c)
		return
	}

	serveComponentImage(c, h.cfg.Storage.BaseDir, workflowDir, workflow.Img)
}

// ---------- store 封面 ----------
//
// store（发布产物）的封面位置固定为：{base_dir}/store/{path_name}/{store_type}/{img}。
// Img 只存纯文件名；path_name/Img 为空、路径越界或文件不存在时一律返回内置占位图。

// GetStoreImage godoc
// @Summary      获取商店封面图片流
// @Description  根据 int64 主键读取商店封面，位置固定为 {base_dir}/store/{path_name}/{store_type}/{img}；
// @Description  文件不存在或路径非法时返回内置占位图（200 + image/svg+xml）。
// @Tags         商店
// @Produce      image/svg+xml
// @Param        storeId  path  int64  true  "商店主键 ID（int64）"
// @Success      200  {file}    binary  "图片流或占位图"
// @Failure      400  {object}  errors.AppError  "参数错误"
// @Failure      401  {object}  errors.AppError  "未认证"
// @Failure      404  {object}  errors.AppError  "商店不存在"
// @Failure      500  {object}  errors.AppError  "服务器错误"
// @Security     Bearer
// @Router       /store/{storeId}/image [get]
func (h *StoreHandler) GetStoreImage(c *gin.Context) {
	if _, ok := getCurrentUserID(c); !ok {
		return
	}

	storeID, err := parseComponentIDParam(c, "storeId")
	if err != nil {
		c.Error(errors.NewValidationError(err.Error()))
		return
	}

	store, err := h.storeService.GetStoreByID(c.Request.Context(), storeID)
	if err != nil {
		if stderrs.Is(err, gorm.ErrRecordNotFound) {
			c.Error(errors.NewNotFoundError("store not found"))
			return
		}
		c.Error(errors.NewInternalServerError("failed to query store").WithDetails(err.Error()))
		return
	}

	if h.cfg == nil || h.cfg.Storage == nil {
		servePlaceholderImage(c)
		return
	}

	// store 不落库绝对路径，目录由 PathName 相对 base_dir 推导。
	storePath := utils.GetWorkflowOrScriptStoreDir(h.cfg.Storage.BaseDir, store.PathName)
	img := strings.TrimSpace(store.Img)
	if strings.TrimSpace(store.PathName) == "" || img == "" {
		servePlaceholderImage(c)
		return
	}

	// 固定位置 {path}/{store_type}/{img}；SafePathUnderBase 挡掉 img 里的 ../ 越界写法。
	targetPath, err := utils.SafePathUnderBase(storePath, filepath.Join(storePath, strings.TrimSpace(store.StoreType), img))
	if err != nil {
		servePlaceholderImage(c)
		return
	}
	if info, statErr := os.Stat(targetPath); statErr != nil || info.IsDir() {
		servePlaceholderImage(c)
		return
	}

	c.Header("Cache-Control", "no-cache")
	c.File(targetPath)
}
