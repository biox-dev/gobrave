package utils

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

// projectDirName 将项目的数值主键格式化为存储目录名。
func projectDirName(projectID int64) string {
	return strconv.FormatInt(projectID, 10)
}

func GetScriptFile(baseDir string, projectID int64, scriptType, scriptID string) (string, string, error) {
	resolvedScriptID := strings.TrimSpace(scriptID)
	if resolvedScriptID == "" {
		resolvedScriptID = strings.TrimSpace(scriptID)
	}

	// return "pipeline/script/" + resolvedScriptID, mainFileByScriptType(scriptType), nil
	scriptDir := GetScriptDir(baseDir, projectID)
	return filepath.Join(scriptDir, scriptID), mainFileByScriptType(scriptType), nil
}
func GetScriptFileDir(baseDir string, projectID int64, scriptID string) string {
	scriptDir := GetScriptDir(baseDir, projectID)
	return filepath.Join(scriptDir, scriptID)
}
func GetScriptDir(baseDir string, projectID int64) string {
	return filepath.Join(baseDir, "data", projectDirName(projectID), "pipeline", "script")
}
func GetWorkflowFileDir(baseDir string, projectID int64, workflowID string) string {
	workflowDir := GetWorkflowDir(baseDir, projectID)
	return filepath.Join(workflowDir, workflowID)
}
func GetWorkflowDir(baseDir string, projectID int64) string {
	return filepath.Join(baseDir, "data", projectDirName(projectID), "pipeline", "workflow")
}
func GetAnalysisDir(baseDir string, projectID int64, workflow int64) string {
	return filepath.Join(baseDir, "data", projectDirName(projectID), "analysis", fmt.Sprintf("%d", workflow))
}
func GetAnalysisNodeDir(baseDir string, projectID int64, scriptID string) string {
	return filepath.Join(baseDir, "data", projectDirName(projectID), "analysis_node", scriptID)
}

func GetAgentNodeDir(baseDir string, projectID int64, nodeID int64) string {
	return filepath.Join(baseDir, "data", projectDirName(projectID), "analysis_node", fmt.Sprint(nodeID))
}

func GetAnalysisNodeEnvFile(workspaceDir string) string {
	return filepath.Join(workspaceDir, "env.sh")
}

func GetProjectDir(baseDir string, projectID int64) string {
	return filepath.Join(baseDir, "data", projectDirName(projectID))
}

func GetProjectReportDir(baseDir string, projectID int64, reportID string) string {
	projectDir := GetProjectDir(baseDir, projectID)
	return filepath.Join(projectDir, "report", reportID)

}

func GetAISummaryDir(baseDir string, projectID int64, summaryID int64) string {
	projectDir := GetProjectDir(baseDir, projectID)
	return filepath.Join(projectDir, "AiSummary", fmt.Sprintf("%d", summaryID))
}

func GetAISummaryFile(baseDir string, projectID int64, summaryID int64) string {
	summaryDir := GetAISummaryDir(baseDir, projectID, summaryID)
	return filepath.Join(summaryDir, "summary.md")

}
func GetProjectLiteratureDir(baseDir string, projectID int64, literatureID string) string {
	projectDir := GetProjectDir(baseDir, projectID)
	return filepath.Join(projectDir, "literature", literatureID)
}

// GetProjectLiteratureFile 返回某个文献的全文文件路径：
// <baseDir>/data/<projectId>/literature/<literatureId>/literature.md。
func GetProjectLiteratureFile(baseDir string, projectID int64, literatureID string) string {
	return filepath.Join(GetProjectLiteratureDir(baseDir, projectID, literatureID), "literature.md")
}

// GetProjectDocDir 返回某个 ProjectReport 的文档源目录：
// <baseDir>/data/<projectId>/docs/<reportId>/src。
func GetProjectDocDir(baseDir string, projectID int64, reportID string) string {
	return filepath.Join(baseDir, "data", projectDirName(projectID), "docs", reportID, "src")
}

// GetProjectDocBookDir 返回某个 ProjectReport 的文档站点（构建产物）目录：
// <baseDir>/data/<projectId>/docs/<reportId>/book。
func GetProjectDocBookDir(baseDir string, projectID int64, reportID string) string {
	return filepath.Join(baseDir, "data", projectDirName(projectID), "docs", reportID, "book")
}

func GetAnalysisDebugDir(baseDir, analysisID, version string) string {
	return filepath.Join(baseDir, "debug", analysisID, version)
}

func GetAnalysisNodeCacheDir(workspaceDir string) string {
	return filepath.Join(workspaceDir, "cached")
}
func GetAnalysisNodeOutputDir(workspaceDir string) string {
	return filepath.Join(workspaceDir, "output")
}

func GetDatasetDir(baseDir string, projectID int64, datasetID int64) string {
	projectDir := GetProjectDir(baseDir, projectID)
	return filepath.Join(projectDir, "dataset", fmt.Sprintf("%d", datasetID))
}

func GetStoreDir(baseDir string) string {
	return filepath.Join(baseDir, "store")
}

// GetWorkflowOrScriptStoreDir resolves the absolute store directory of a
// published workflow/script or of a downloaded remote repo, from its
// types.Store.PathName (a random identifier generated once at store creation
// time; legacy rows may still hold a workflow/script id or "<owner>/<repo>").
//
// The single source of truth for the relative layout is RelStoreDir; keep both
// in sync by always deriving one from the other.
func GetWorkflowOrScriptStoreDir(baseDir, id string) string {
	return filepath.Join(baseDir, RelStoreDir(id))
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
