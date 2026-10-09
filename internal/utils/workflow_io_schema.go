package utils

import "path/filepath"

// 工作流目录下的 io_schema.json：与脚本侧（script_io_schema.go）结构完全对称，
// 路径解析与落盘 / 读取原语统一走 io_schema.go，避免两处格式与容错逻辑漂移。
//
// io_schema.json 位于工作流目录（GetWorkflowFileDir）根部，
// 路径只取决于 baseDir / projectID / workflowID（均为字符串，注意 projectID 是
// project.project_id 而不是 workflow.ProjectID int64 外键）。

// WorkflowIOSchemaPath 返回工作流目录下 io_schema.json 的绝对路径。
func WorkflowIOSchemaPath(baseDir, projectID, workflowID string) string {
	return filepath.Join(GetWorkflowFileDir(baseDir, projectID, workflowID), IOSchemaFileName)
}

// WriteWorkflowIOSchema 将 io_schema 内容写入工作流目录下的 io_schema.json（详见 writeIOSchemaFile）。
func WriteWorkflowIOSchema(baseDir, projectID, workflowID, content string) error {
	return writeIOSchemaFile(WorkflowIOSchemaPath(baseDir, projectID, workflowID), content)
}

// ReadWorkflowIOSchemaFile 读取工作流目录下 io_schema.json 的原始内容，文件不存在时返回 nil。
func ReadWorkflowIOSchemaFile(baseDir, projectID, workflowID string) ([]byte, error) {
	return readIOSchemaFile(WorkflowIOSchemaPath(baseDir, projectID, workflowID))
}

// ReadWorkflowIOSchema 读取并解析工作流目录下的 io_schema.json，缺失 / 空内容返回空 map。
func ReadWorkflowIOSchema(baseDir, projectID, workflowID string) (map[string]any, error) {
	return readIOSchema(WorkflowIOSchemaPath(baseDir, projectID, workflowID))
}
