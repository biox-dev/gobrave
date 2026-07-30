package utils

import (
	"fmt"
	"path/filepath"
	"strings"
)

func GetScriptFile(baseDir, projectID, scriptType, scriptID string) (string, string, error) {
	resolvedScriptID := strings.TrimSpace(scriptID)
	if resolvedScriptID == "" {
		resolvedScriptID = strings.TrimSpace(scriptID)
	}

	// return "pipeline/script/" + resolvedScriptID, mainFileByScriptType(scriptType), nil
	scriptDir := GetScriptDir(baseDir, projectID)
	return filepath.Join(scriptDir, scriptID), mainFileByScriptType(scriptType), nil
}
func GetScriptFileDir(baseDir, projectId, scriptID string) string {
	scriptDir := GetScriptDir(baseDir, projectId)
	return filepath.Join(scriptDir, scriptID)
}
func GetScriptDir(baseDir, projectId string) string {
	return filepath.Join(baseDir, "data", projectId, "pipeline", "script")
}
func GetWorkflowDir(baseDir, projectId string) string {
	return filepath.Join(baseDir, "data", projectId, "pipeline", "workflow")
}
func GetAnalysisDir(baseDir, projectId string, workflow int64) string {
	return filepath.Join(baseDir, "data", projectId, "analysis", fmt.Sprintf("%d", workflow))
}
func GetAnalysisNodeDir(baseDir, projectId, scriptID string) string {
	return filepath.Join(baseDir, "data", projectId, "analysis_node", scriptID)
}

func GetProjectDir(baseDir, projectId string) string {
	return filepath.Join(baseDir, "data", projectId)
}

func GetProjectDocDir(baseDir, projectId string) string {
	return filepath.Join(baseDir, "data", projectId, "docs", "src")
}
func mainFileByScriptType(scriptType string) string {
	switch strings.ToLower(strings.TrimSpace(scriptType)) {
	case "r":
		return "main.R"
	case "python":
		return "main.py"
	case "shell":
		return "main.sh"
	case "jupyter":
		return "main.ipynb"
	case "qmd":
		return "main.qmd"
	default:
		return "main.R"
	}
}
