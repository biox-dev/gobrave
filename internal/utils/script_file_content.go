package utils

import (
	"os"
	"path/filepath"
)

// ScriptMainFilePath 返回脚本主文件（main.R / main.py / main.sh / ...）的绝对路径。
// 主文件名由 script_type 决定，脚本目录与类型无关（与 io_schema.json 同目录）。
func ScriptMainFilePath(baseDir, projectID, scriptType, scriptID string) string {
	scriptDir, mainFile, _ := GetScriptFile(baseDir, projectID, scriptType, scriptID)
	return filepath.Join(scriptDir, mainFile)
}

// ReadScriptFileContent 读取脚本主文件内容。
// 文件不存在时返回 nil（不视为错误），便于调用方按“空脚本”处理。
func ReadScriptFileContent(baseDir, projectID, scriptType, scriptID string) ([]byte, error) {
	data, err := os.ReadFile(ScriptMainFilePath(baseDir, projectID, scriptType, scriptID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// WriteScriptFileContent 覆盖写入脚本主文件内容，目录不存在时自动创建。
func WriteScriptFileContent(baseDir, projectID, scriptType, scriptID, content string) error {
	path := ScriptMainFilePath(baseDir, projectID, scriptType, scriptID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
