package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
)

var projectIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// RegisterProjectDocsRoute registers the project docs entry route.
//
// The only path parameter is a ProjectReport ID; the owning project id is
// resolved from the report and the docs are served from that report's doc
// source directory (see utils.GetProjectDocDir):
// - GET /docs/:reportid            -> {projectDocBookDir}/index.html
// - GET /docs/:reportid/*filepath  -> {projectDocBookDir}/*filepath
func RegisterProjectDocsRoute(r *gin.Engine, cfg *config.Config, projectService interfaces.ProjectService) {
	resolveDocPath := func(c *gin.Context) (string, int, bool) {
		if cfg == nil || cfg.Storage == nil {
			return "", http.StatusNotFound, false
		}

		baseDir := strings.TrimSpace(cfg.Storage.BaseDir)
		if baseDir == "" {
			return "", http.StatusNotFound, false
		}

		if projectService == nil {
			return "", http.StatusInternalServerError, false
		}

		reportID, err := strconv.ParseInt(strings.TrimSpace(c.Param("reportid")), 10, 64)
		if err != nil || reportID <= 0 {
			return "", http.StatusBadRequest, false
		}

		report, err := projectService.GetProjectReportByID(c.Request.Context(), reportID)
		if err != nil || report == nil {
			return "", http.StatusNotFound, false
		}

		projectID := strings.TrimSpace(report.ProjectID)
		if projectID == "" || !projectIDPattern.MatchString(projectID) {
			return "", http.StatusBadRequest, false
		}

		projectDocBookDir := utils.GetProjectDocBookDir(baseDir, projectID, strconv.FormatInt(report.ID, 10))
		if strings.TrimSpace(projectDocBookDir) == "" {
			return "", http.StatusNotFound, false
		}

		relFile := strings.TrimSpace(c.Param("filepath"))
		relFile = strings.TrimPrefix(relFile, "/")
		if relFile == "" {
			relFile = "index.html"
		}

		targetPath, err := utils.SafePathUnderBase(baseDir, filepath.Join(projectDocBookDir, relFile))
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

	r.GET("/docs/:reportid", func(c *gin.Context) {
		targetPath, status, ok := resolveDocPath(c)
		if !ok {
			c.AbortWithStatus(status)
			return
		}
		c.File(targetPath)
	})

	r.GET("/docs/:reportid/*filepath", func(c *gin.Context) {
		targetPath, status, ok := resolveDocPath(c)
		if !ok {
			c.AbortWithStatus(status)
			return
		}
		c.File(targetPath)
	})
}
