package utils

import (
	"path/filepath"
	"strings"
)

// GetAnalysisURLPrefix converts an absolute analysis / analysis node output
// directory into a data-relative path, e.g.
//
//	<baseDir>/data/34/analysis_node/100/200/output -> /34/analysis_node/100/200/output/
//
// Storage paths are persisted relative to storage.base_dir and the router
// exposes <baseDir>/data at /data-analysis (see router.serveStatic), but the
// /data-analysis part is deliberately NOT added here: it is added by the
// frontend, so the persisted markdown/URLs stay deployment-agnostic.
//
// Directories living outside <baseDir>/data are left untouched (normalised to
// a root-looking path with a trailing slash) so the caller still gets a
// usable, if unusual, path instead of an empty one.
func GetAnalysisURLPrefix(baseDir, outputDir string) string {
	outputDir = strings.TrimSpace(outputDir)
	if outputDir == "" {
		return ""
	}

	dataDir := filepath.Join(strings.TrimSpace(baseDir), "data")
	// outputDir 去除 dataDir 前缀，只保留 data 目录之后的部分。
	if after, ok := strings.CutPrefix(outputDir, dataDir); ok {
		outputDir = after
	}

	if !strings.HasPrefix(outputDir, "/") {
		outputDir = "/" + outputDir
	}
	return strings.TrimSuffix(outputDir, "/") + "/"
}
