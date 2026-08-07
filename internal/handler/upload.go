package handler

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gobravedev/gobrave/internal/config"
	appErrors "github.com/gobravedev/gobrave/internal/errors"
	"github.com/gobravedev/gobrave/internal/utils"
)

const (
	maxImageUploadSize = int64(10 << 20) // 10 MiB
)

var imageMimeToExt = map[string]string{
	"image/png":  ".png",
	"image/jpeg": ".jpg",
	"image/webp": ".webp",
	"image/gif":  ".gif",
}

type UploadHandler struct {
	config *config.Config
}

func NewUploadHandler(cfg *config.Config) *UploadHandler {
	return &UploadHandler{config: cfg}
}

// UploadImage handles image file upload for project report markdown.
// @Summary      上传项目报告图片
// @Description  上传图片并返回可直接用于 Markdown 的图片 URL
// @Tags         项目
// @Accept       mpfd
// @Produce      json
// @Param        file  formData  file  true  "图片文件"
// @Success      200   {object}  map[string]interface{} "上传成功"
// @Failure      400   {object}  appErrors.AppError      "参数错误"
// @Failure      500   {object}  appErrors.AppError      "服务器错误"
// @Security     Bearer
// @Router       /project/upload-image [post]
func (h *UploadHandler) UploadImage(c *gin.Context) {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		c.Error(appErrors.NewValidationError("missing upload file").WithDetails(err.Error()))
		return
	}

	if fileHeader.Size <= 0 {
		c.Error(appErrors.NewValidationError("empty upload file"))
		return
	}
	if fileHeader.Size > maxImageUploadSize {
		c.Error(appErrors.NewValidationError("image is too large").WithDetails("max size is 10MB"))
		return
	}

	src, err := fileHeader.Open()
	if err != nil {
		c.Error(appErrors.NewInternalServerError("failed to open upload file").WithDetails(err.Error()))
		return
	}
	defer src.Close()

	buf := make([]byte, 512)
	readSize, err := io.ReadFull(src, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		c.Error(appErrors.NewValidationError("failed to read upload file").WithDetails(err.Error()))
		return
	}

	contentType := http.DetectContentType(buf[:readSize])
	ext, ok := imageMimeToExt[contentType]
	if !ok {
		c.Error(appErrors.NewValidationError("unsupported image type").WithDetails(contentType))
		return
	}

	absImageDir := utils.ResolveImageDir(h.config.Storage.BaseDir)

	relativeDateDir := filepath.Join(
		time.Now().UTC().Format("2006"),
		time.Now().UTC().Format("01"),
		time.Now().UTC().Format("02"),
	)
	targetDir := filepath.Join(absImageDir, relativeDateDir)
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		c.Error(appErrors.NewInternalServerError("failed to create image directory").WithDetails(err.Error()))
		return
	}

	filename := fmt.Sprintf("%d-%s%s", time.Now().UTC().UnixMilli(), randomHex(8), ext)
	targetPath := filepath.Join(targetDir, filename)
	if err := c.SaveUploadedFile(fileHeader, targetPath); err != nil {
		c.Error(appErrors.NewInternalServerError("failed to save upload file").WithDetails(err.Error()))
		return
	}

	publicPath := "/images/" + strings.TrimPrefix(filepath.ToSlash(filepath.Join(relativeDateDir, filename)), "/")
	c.JSON(http.StatusOK, gin.H{
		"url":  publicPath,
		"name": filename,
		"size": fileHeader.Size,
	})
}

func randomHex(bytesLen int) string {
	b := make([]byte, bytesLen)
	if _, err := rand.Read(b); err != nil {
		return "randfallback"
	}
	return hex.EncodeToString(b)
}
