package utils

import (
	"fmt"
	"path/filepath"
	"strings"
)

// GetAnalysisURLPrefix converts an absolute analysis / analysis node output
// directory into the /data-analysis URL prefix that serves it.
//
// Storage paths are persisted relative to storage.base_dir and the router
// exposes <baseDir>/data at /data-analysis (see router.serveStatic), so the
// prefix is simply the output directory with the <baseDir>/data part stripped.
// Directories living outside <baseDir>/data are left untouched so the caller
// still gets a usable, if unusual, URL instead of an empty one.
func GetAnalysisURLPrefix(baseDir, outputDir string) string {
	outputDir = strings.TrimSpace(outputDir)
	dataDir := filepath.Join(strings.TrimSpace(baseDir), "data")
	// outputDir 去除 dataDir 前缀
	if after, ok := strings.CutPrefix(outputDir, dataDir); ok {
		outputDir = after
	}

	return fmt.Sprintf("/data-analysis%s/", outputDir)
}
