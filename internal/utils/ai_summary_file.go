package utils

import (
	"os"
	"path/filepath"
)

// ReadAISummaryFile 读取摘要正文文件（见 GetAISummaryFile）。
// 文件不存在时返回空串（摘要尚未生成或已删除），其它读取错误原样返回。
func ReadAISummaryFile(baseDir string, projectID int64, summaryID int64) (string, error) {
	data, err := os.ReadFile(GetAISummaryFile(baseDir, projectID, summaryID))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(data), nil
}

// WriteAISummaryFile 覆写摘要正文文件（见 GetAISummaryFile）。
// 文件所在目录不存在时自动创建。
func WriteAISummaryFile(baseDir string, projectID int64, summaryID int64, content string) error {
	path := GetAISummaryFile(baseDir, projectID, summaryID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

// DeleteAISummaryFile 删除摘要正文文件（见 GetAISummaryFile）。
// 文件不存在时不报错。
func DeleteAISummaryFile(baseDir string, projectID int64, summaryID int64) error {
	if err := os.Remove(GetAISummaryFile(baseDir, projectID, summaryID)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
