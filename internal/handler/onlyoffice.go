package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/utils"
)

var nonFilenameChars = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

type onlyOfficeCallbackPayload struct {
	Status   int    `json:"status"`
	URL      string `json:"url"`
	Key      string `json:"key"`
	FileType string `json:"filetype"`
	Title    string `json:"title"`
	Path     string `json:"path"`
	Userdata string `json:"userdata"`
}

func RegisterOnlyOfficeRoutes(r *gin.Engine, proxyHandler *ProxyHandler) {
	r.POST("/go-onlyoffice/callback", handleOnlyOfficeCallback)
	r.Any("/onlyoffice", proxyHandler.OnlyOfficeProxy)
	r.Any("/onlyoffice/*proxyPath", proxyHandler.OnlyOfficeProxy)
}

func handleOnlyOfficeCallback(c *gin.Context) {
	var payload onlyOfficeCallbackPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		logger.Warnf(c.Request.Context(), "[OnlyOffice] invalid callback payload: %v", err)
		c.JSON(http.StatusOK, gin.H{"error": 1})
		return
	}

	logger.Infof(
		c.Request.Context(),
		"[OnlyOffice] callback received: status=%d key=%s title=%s hasURL=%t path=%s",
		payload.Status,
		payload.Key,
		payload.Title,
		strings.TrimSpace(payload.URL) != "",
		strings.TrimSpace(payload.Path),
	)

	if !shouldPersistOnlyOfficeFile(payload.Status) {
		c.JSON(http.StatusOK, gin.H{"error": 0})
		return
	}

	if strings.TrimSpace(payload.URL) == "" {
		logger.Warnf(c.Request.Context(), "[OnlyOffice] callback status %d without download url", payload.Status)
		c.JSON(http.StatusOK, gin.H{"error": 1})
		return
	}

	targetPath, err := resolveOnlyOfficeTargetPath(c, payload)
	if err != nil {
		logger.Warnf(c.Request.Context(), "[OnlyOffice] invalid target path: %v", err)
		c.JSON(http.StatusOK, gin.H{"error": 1})
		return
	}

	if err := persistOnlyOfficeDocument(c.Request.Context(), payload, targetPath); err != nil {
		logger.Errorf(c.Request.Context(), "[OnlyOffice] save callback failed: %v", err)
		c.JSON(http.StatusOK, gin.H{"error": 1})
		return
	}

	c.JSON(http.StatusOK, gin.H{"error": 0})
}

func shouldPersistOnlyOfficeFile(status int) bool {
	return status == 2 || status == 3 || status == 6 || status == 7
}

func persistOnlyOfficeDocument(ctx context.Context, payload onlyOfficeCallbackPayload, targetPath string) error {
	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, payload.URL, nil)
	if err != nil {
		return err
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status when downloading callback file: %d", resp.StatusCode)
	}

	filePath, err := resolveOnlyOfficeSaveFilePath(payload, targetPath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(filePath), 0o755); err != nil {
		return err
	}

	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return err
	}

	logger.Infof(ctx, "[OnlyOffice] saved document: %s", filePath)
	return nil
}

func resolveOnlyOfficeTargetPath(c *gin.Context, payload onlyOfficeCallbackPayload) (string, error) {
	for _, candidate := range []string{c.Query("path"), payload.Path, parseOnlyOfficePathFromUserdata(payload.Userdata)} {
		if path := strings.TrimSpace(candidate); path != "" {
			return normalizeOnlyOfficeTargetPath(path)
		}
	}
	return "", nil
}

func parseOnlyOfficePathFromUserdata(userdata string) string {
	userdata = strings.TrimSpace(userdata)
	if userdata == "" {
		return ""
	}

	if strings.HasPrefix(userdata, "{") {
		var data map[string]any
		if err := json.Unmarshal([]byte(userdata), &data); err == nil {
			for _, key := range []string{"path", "filePath", "savePath"} {
				if v, ok := data[key].(string); ok && strings.TrimSpace(v) != "" {
					return strings.TrimSpace(v)
				}
			}
		}
	}

	return userdata
}

func normalizeOnlyOfficeTargetPath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}

	baseDir, err := utils.ResolveExternalPath("onlyoffice")
	if err != nil {
		return "", err
	}
	safePath, err := utils.SafePathUnderBase(baseDir, filepath.Join(baseDir, path))
	if err != nil {
		return "", err
	}
	return safePath, nil
}

func resolveOnlyOfficeSaveFilePath(payload onlyOfficeCallbackPayload, targetPath string) (string, error) {
	if strings.TrimSpace(targetPath) == "" {
		saveDir, err := utils.ResolveExternalPath("onlyoffice")
		if err != nil {
			return "", err
		}
		return filepath.Join(saveDir, buildOnlyOfficeFilename(payload)), nil
	}

	if strings.HasSuffix(targetPath, string(filepath.Separator)) {
		return filepath.Join(targetPath, buildOnlyOfficeFilename(payload)), nil
	}
	return targetPath, nil
}

func buildOnlyOfficeFilename(payload onlyOfficeCallbackPayload) string {
	name := strings.TrimSpace(payload.Title)
	if name == "" {
		name = strings.TrimSpace(payload.Key)
	}
	if name == "" {
		name = "onlyoffice_" + time.Now().Format("20060102_150405")
	}

	ext := strings.TrimPrefix(strings.TrimSpace(payload.FileType), ".")
	if ext != "" && !strings.Contains(strings.ToLower(name), "."+strings.ToLower(ext)) {
		name += "." + ext
	}

	name = nonFilenameChars.ReplaceAllString(name, "_")
	name = strings.Trim(name, "._")
	if name == "" {
		name = "onlyoffice_" + time.Now().Format("20060102_150405")
	}
	return name
}
