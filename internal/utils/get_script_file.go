package utils

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DefaultProjectReportFilename 是项目报告工作目录下约定的输出文件名。
// 定义在 utils 中是为了让报告路径推导（GetProjectReportOutputFile）不反向依赖 types 包
// （types 已经 import utils，反向引用会形成循环）。
const DefaultProjectReportFilename = "output.md"

// projectDirName 将项目的数值主键格式化为存储目录名。
func projectDirName(projectID int64) string {
	return strconv.FormatInt(projectID, 10)
}

// GetScriptFileDir 返回脚本目录（<base>/data/<project>/pipeline/script/<scriptID>）。
// 目录内含主文件 / io_schema.json / README.md，一次性取全部路径请用 ScriptLayout。
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

// EnsureProjectDir 返回项目数据目录（<base>/data/<projectID>），不存在则创建。
func EnsureProjectDir(baseDir string, projectID int64) (string, error) {
	dir := GetProjectDir(baseDir, projectID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// GetAgentSessionDir 返回某会话的 Agent 工作目录：<workspaceDir>/sessions/<sessionID>。
//
// workspaceDir 是项目数据目录（见 GetProjectDir / EnsureProjectDir）。
func GetAgentSessionDir(workspaceDir string, sessionID int64) string {
	return filepath.Join(workspaceDir, "sessions", projectDirName(sessionID))
}

// EnsureAgentSessionDir 返回某会话的 Agent 工作目录，不存在则创建。
func EnsureAgentSessionDir(workspaceDir string, sessionID int64) (string, error) {
	dir := GetAgentSessionDir(workspaceDir, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

func GetProjectReportDir(baseDir string, projectID int64, reportID string) string {
	projectDir := GetProjectDir(baseDir, projectID)
	return filepath.Join(projectDir, "report", reportID)

}

// GetProjectReportOutputFile 返回某个 ProjectReport 目录下的正文文件路径
// （<reportDir>/output.md）。目录为空时返回空串，避免调用方拿到悬空的 "output.md"。
func GetProjectReportOutputFile(reportDir string) string {
	reportDir = strings.TrimSpace(reportDir)
	if reportDir == "" {
		return ""
	}
	return filepath.Join(reportDir, DefaultProjectReportFilename)
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

// GetSharedDatasetDir 返回共享数据集目录（<base>/shared/dataset/<datasetID>）。
//
// 共享数据集不挂在任何项目下，多个项目绑定同一个 dataset 时读写的是同一份文件，
// 因此目录里没有 project 这一段。是否共享由 types.Dataset.Shared 决定，
// 调用方统一走 Dataset.WorkspaceDir，不要直接拼这个路径。
func GetSharedDatasetDir(baseDir string, datasetID int64) string {
	return filepath.Join(baseDir, "shared", "dataset", fmt.Sprintf("%d", datasetID))
}

// GetProjectPackageRoot 返回项目包目录根（<base>/data/<projectID>/package）。
// 各语言子目录为 <root>/<R|python|conda>-<templateID>，是否共享由
// types.ContainerTemplate.UseSharedDir 决定，调用方统一走 ContainerTemplate.PackageDir。
func GetProjectPackageRoot(baseDir string, projectID int64) string {
	return filepath.Join(GetProjectDir(baseDir, projectID), "package")
}

// GetSharedPackageRoot 返回共享包目录根（<base>/shared/package）。
//
// 共享包目录不挂在任何项目下，多个项目使用同一份语言包（R / python / conda）；
// 是否共享由 types.ContainerTemplate.UseSharedDir 决定，调用方统一走
// ContainerTemplate.PackageDir，不要直接拼这个路径。
func GetSharedPackageRoot(baseDir string) string {
	return filepath.Join(baseDir, "shared", "package")
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

// mainFileByScriptType 是「脚本类型 → 主文件名」的唯一映射（ScriptLayout 使用）。
//
// 不要再在其它包里复制一份：dag/prepare 曾经有一份只支持 r/python/shell、
// 且默认回退成 main.sh 的副本，导致 jupyter / qmd 脚本在节点运行期软链到不存在的文件。
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
