package utils

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DagDefinitionFileName 是工作流目录下 DAG 定义的文件名。
//
// dag_definition 不再作为数据库字段持久化，dag_definition.json 是唯一数据源：
// 保存侧（SaveWorkflow / SaveWorkflowDag）把它写入工作流目录（GetWorkflowFileDir），
// 读取侧统一通过 ReadWorkflowDagDefinitionFile / ReadWorkflowDagDefinition 从文件获取。
const DagDefinitionFileName = "dag_definition.json"

// WorkflowDagDefinitionPath 返回工作流目录下 dag_definition.json 的绝对路径。
// 路径只取决于 baseDir / projectID / workflowID（都参与目录拼接）。
func WorkflowDagDefinitionPath(baseDir, projectID, workflowID string) string {
	return filepath.Join(GetWorkflowFileDir(baseDir, projectID, workflowID), DagDefinitionFileName)
}

// WriteWorkflowDagDefinition 将 dag_definition 内容格式化（两空格缩进）后写入工作流目录下的
// dag_definition.json（详见 writeDagDefinitionFile）。
func WriteWorkflowDagDefinition(baseDir, projectID, workflowID, content string) error {
	return writeDagDefinitionFile(WorkflowDagDefinitionPath(baseDir, projectID, workflowID), content)
}

// ReadWorkflowDagDefinitionFile 读取工作流目录下 dag_definition.json 的原始内容，文件不存在时返回 nil。
func ReadWorkflowDagDefinitionFile(baseDir, projectID, workflowID string) ([]byte, error) {
	return readDagDefinitionFile(WorkflowDagDefinitionPath(baseDir, projectID, workflowID))
}

// ReadWorkflowDagDefinition 读取并解析工作流目录下的 dag_definition.json，缺失 / 空内容返回空 map。
func ReadWorkflowDagDefinition(baseDir, projectID, workflowID string) (map[string]any, error) {
	return readDagDefinition(WorkflowDagDefinitionPath(baseDir, projectID, workflowID))
}

// writeDagDefinitionFile 将 dag_definition 内容格式化（两空格缩进）后写入指定路径。
//
// 格式化走 utils.FormatJSONIndent：只重排空白，保留原始键顺序与数字字面量，
// 这样前端压缩过的 JSON 落盘后是可读、易 diff 的格式（git 提交时 diff 才稳定）。
// 内容为空（含纯空白）时不写入（保留磁盘上已有文件），因此局部保存不会用空值覆盖历史 DAG；
// 内容不是合法 JSON 时返回错误且不落盘，避免把非法内容固化到磁盘。
func writeDagDefinitionFile(path, content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	formatted, err := FormatJSONIndent([]byte(content))
	if err != nil {
		return fmt.Errorf("invalid dag_definition json: %w", err)
	}
	return WriteFileEnsureDir(path, formatted)
}

// readDagDefinitionFile 读取指定路径下 dag_definition.json 的原始内容。
// 文件不存在时返回 nil（不视为错误），便于调用方按“无 DAG 定义”处理。
func readDagDefinitionFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// readDagDefinition 读取并解析指定路径下的 dag_definition.json。
// 文件不存在或内容为空时返回空 map（不视为错误）；内容非法 JSON 时返回错误。
func readDagDefinition(path string) (map[string]any, error) {
	data, err := readDagDefinitionFile(path)
	if err != nil || len(data) == 0 {
		return map[string]any{}, err
	}
	result := make(map[string]any)
	if err := json.Unmarshal(data, &result); err != nil {
		return map[string]any{}, err
	}
	return result, nil
}
