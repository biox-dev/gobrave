package utils

import (
	"os"
	"path/filepath"
)

// ReadmeFileName 是脚本目录 / 工作流目录下的说明文档文件名。
// README.md 是唯一数据源，不落库：写侧只有 SaveScriptReadme / SaveWorkflowReadme 落盘，
// 读侧统一通过本文件的函数读取。
const ReadmeFileName = "README.md"

// ScriptReadmePath 返回脚本目录（GetScriptFileDir）下 README.md 的绝对路径。
func ScriptReadmePath(baseDir, projectID, scriptID string) string {
	return filepath.Join(GetScriptFileDir(baseDir, projectID, scriptID), ReadmeFileName)
}

// WorkflowReadmePath 返回工作流目录（GetWorkflowFileDir）下 README.md 的绝对路径。
func WorkflowReadmePath(baseDir, projectID, workflowID string) string {
	return filepath.Join(GetWorkflowFileDir(baseDir, projectID, workflowID), ReadmeFileName)
}

// ReadScriptReadme 读取脚本目录下的 README.md。
// 文件不存在时返回 nil（不视为错误），便于调用方按“空 README”处理。
func ReadScriptReadme(baseDir, projectID, scriptID string) ([]byte, error) {
	return readReadmeFile(ScriptReadmePath(baseDir, projectID, scriptID))
}

// WriteScriptReadme 覆盖写入脚本目录下的 README.md，目录不存在时自动创建。
func WriteScriptReadme(baseDir, projectID, scriptID, content string) error {
	return writeReadmeFile(ScriptReadmePath(baseDir, projectID, scriptID), content)
}

// ReadWorkflowReadme 读取工作流目录下的 README.md。
// 文件不存在时返回 nil（不视为错误），便于调用方按“空 README”处理。
func ReadWorkflowReadme(baseDir, projectID, workflowID string) ([]byte, error) {
	return readReadmeFile(WorkflowReadmePath(baseDir, projectID, workflowID))
}

// WriteWorkflowReadme 覆盖写入工作流目录下的 README.md，目录不存在时自动创建。
func WriteWorkflowReadme(baseDir, projectID, workflowID, content string) error {
	return writeReadmeFile(WorkflowReadmePath(baseDir, projectID, workflowID), content)
}

// readReadmeFile 读取 README.md，文件不存在时返回 nil（不视为错误）。
func readReadmeFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// writeReadmeFile 覆盖写入 README.md，目录不存在时自动创建。
func writeReadmeFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
