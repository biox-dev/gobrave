package utils

import "path/filepath"

// 脚本目录下的 io_schema.json：路径解析与落盘 / 读取原语见 io_schema.go，
// 本文件只负责把脚本目录（GetScriptFileDir）拼进来。
//
// io_schema.json 位于脚本目录根部，路径只取决于 baseDir / projectID / scriptID，
// 与脚本类型（决定主文件名）无关。

// ScriptIOSchemaPath 返回脚本目录下 io_schema.json 的绝对路径。
func ScriptIOSchemaPath(baseDir, projectID, scriptID string) string {
	return filepath.Join(GetScriptFileDir(baseDir, projectID, scriptID), IOSchemaFileName)
}

// WriteScriptIOSchema 将 io_schema 内容写入脚本目录下的 io_schema.json（详见 writeIOSchemaFile）。
func WriteScriptIOSchema(baseDir, projectID, scriptID, content string) error {
	return writeIOSchemaFile(ScriptIOSchemaPath(baseDir, projectID, scriptID), content)
}

// ReadScriptIOSchemaFile 读取脚本目录下 io_schema.json 的原始内容，文件不存在时返回 nil。
func ReadScriptIOSchemaFile(baseDir, projectID, scriptID string) ([]byte, error) {
	return readIOSchemaFile(ScriptIOSchemaPath(baseDir, projectID, scriptID))
}

// ReadScriptIOSchema 读取并解析脚本目录下的 io_schema.json，缺失 / 空内容返回空 map。
func ReadScriptIOSchema(baseDir, projectID, scriptID string) (map[string]any, error) {
	return readIOSchema(ScriptIOSchemaPath(baseDir, projectID, scriptID))
}
