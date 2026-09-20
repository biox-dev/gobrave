package utils

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// IOSchemaFileName 是脚本目录下 IO schema 的文件名。
// IOSchema 不再作为 script 的数据库字段持久化，io_schema.json 是唯一数据源：
// 写侧只有 SaveScript 落盘，读侧统一通过本文件的函数读取。
const IOSchemaFileName = "io_schema.json"

// ScriptIOSchemaPath 返回脚本目录下 io_schema.json 的绝对路径。
// io_schema.json 位于脚本目录根部，路径只取决于 baseDir / projectID / scriptID，
// 与脚本类型（决定主文件名）无关。
func ScriptIOSchemaPath(baseDir, projectID, scriptID string) string {
	return filepath.Join(GetScriptFileDir(baseDir, projectID, scriptID), IOSchemaFileName)
}

// WriteScriptIOSchema 将 io_schema 内容格式化（两空格缩进）后写入脚本目录下的 io_schema.json。
//
// 格式化走 utils.FormatJSONIndent：只重排空白，保留原始键顺序与数字字面量，
// 这样前端压缩过的 JSON 落盘后是可读、易 diff 的格式（git 提交时 diff 才稳定）。
// 内容为空（含纯空白）时不写入（保留磁盘上已有文件），因此不会用空值覆盖历史 schema；
// 内容不是合法 JSON 时返回错误且不落盘，避免把非法内容固化到磁盘。
func WriteScriptIOSchema(baseDir, projectID, scriptID, content string) error {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	formatted, err := FormatJSONIndent([]byte(content))
	if err != nil {
		return fmt.Errorf("invalid io_schema json: %w", err)
	}
	return WriteFileEnsureDir(ScriptIOSchemaPath(baseDir, projectID, scriptID), formatted)
}

// ReadScriptIOSchemaFile 读取脚本目录下 io_schema.json 的原始内容。
// 文件不存在时返回 nil（不视为错误），便于调用方按“无 schema”处理。
func ReadScriptIOSchemaFile(baseDir, projectID, scriptID string) ([]byte, error) {
	data, err := os.ReadFile(ScriptIOSchemaPath(baseDir, projectID, scriptID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// ReadScriptIOSchema 读取并解析脚本目录下的 io_schema.json。
// 文件不存在或内容为空时返回空 map（不视为错误）；内容非法 JSON 时返回错误。
func ReadScriptIOSchema(baseDir, projectID, scriptID string) (map[string]any, error) {
	data, err := ReadScriptIOSchemaFile(baseDir, projectID, scriptID)
	if err != nil || len(data) == 0 {
		return map[string]any{}, err
	}
	result := make(map[string]any)
	if err := json.Unmarshal(data, &result); err != nil {
		return map[string]any{}, err
	}
	return result, nil
}
