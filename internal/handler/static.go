package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/utils"
)

var projectIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// RegisterProjectDocsRoute registers project docs entry route.
// It maps:
// - GET /docs/:projectid to {base_dir}/data/{projectid}/docs/index.html
// - GET /docs/:projectid/*filepath to {base_dir}/data/{projectid}/docs/*filepath
func RegisterProjectDocsRoute(r *gin.Engine, cfg *config.Config) {
	resolveDocPath := func(c *gin.Context) (string, int, bool) {
		if cfg == nil || cfg.Storage == nil {
			return "", http.StatusNotFound, false
		}

		baseDir := strings.TrimSpace(cfg.Storage.BaseDir)
		if baseDir == "" {
			return "", http.StatusNotFound, false
		}

		projectID := strings.TrimSpace(c.Param("projectid"))
		if projectID == "" || !projectIDPattern.MatchString(projectID) {
			return "", http.StatusBadRequest, false
		}

		relFile := strings.TrimSpace(c.Param("filepath"))
		relFile = strings.TrimPrefix(relFile, "/")
		if relFile == "" {
			relFile = "index.html"
		}

		targetPath, err := utils.SafePathUnderBase(baseDir, filepath.Join(baseDir, "data", projectID, "docs", "book", relFile))
		if err != nil {
			return "", http.StatusBadRequest, false
		}

		info, err := os.Stat(targetPath)
		if err != nil {
			if os.IsNotExist(err) && relFile == "index.html" {
				if mkErr := os.MkdirAll(filepath.Dir(targetPath), 0755); mkErr != nil {
					return "", http.StatusInternalServerError, false
				}

				defaultHTML := "<!doctype html><html><head><meta charset=\"utf-8\"><title>Docs</title></head><body><h1>Project Docs</h1></body></html>\n"
				if writeErr := os.WriteFile(targetPath, []byte(defaultHTML), 0644); writeErr != nil {
					return "", http.StatusInternalServerError, false
				}

				info, err = os.Stat(targetPath)
				if err != nil {
					return "", http.StatusInternalServerError, false
				}
			} else {
				return "", http.StatusNotFound, false
			}
		}

		if info.IsDir() {
			return "", http.StatusNotFound, false
		}

		return targetPath, http.StatusOK, true
	}

	r.GET("/docs/:projectid", func(c *gin.Context) {
		targetPath, status, ok := resolveDocPath(c)
		if !ok {
			c.AbortWithStatus(status)
			return
		}
		c.File(targetPath)
	})

	r.GET("/docs/:projectid/*filepath", func(c *gin.Context) {
		targetPath, status, ok := resolveDocPath(c)
		if !ok {
			c.AbortWithStatus(status)
			return
		}
		c.File(targetPath)
	})
}
