package utils

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
)

// MarshalJSONIndent 把 value 序列化为两空格缩进的 JSON 字节。
// 项目内所有「写盘的 JSON」都走这里，保证导出文件 / schema 文件 / 参数文件的缩进风格一致。
func MarshalJSONIndent(value any) ([]byte, error) {
	return json.MarshalIndent(value, "", "  ")
}

// FormatJSONIndent 把原始 JSON 文本重新缩进为两空格缩进。
//
// 与 MarshalJSONIndent 的区别：这里只重排空白，**不改动任何 token 字面量**，
// 因此能保留原始键顺序、数字写法（如 1e7 不会被改写成 1e+07）与字符串转义，
// 适合「已经是 JSON 文本、只想格式化」的场景；内容非法 JSON 时返回错误。
func FormatJSONIndent(raw []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteFileEnsureDir 把 data 写入 path（权限 0o644），父目录不存在时自动创建（0o755）。
func WriteFileEnsureDir(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// WriteJSONFile 把 value 以两空格缩进的 JSON 写入 path，父目录不存在时自动创建。
// 调用方不必先建目录；序列化失败时不落盘。
func WriteJSONFile(path string, value any) error {
	data, err := MarshalJSONIndent(value)
	if err != nil {
		return err
	}
	return WriteFileEnsureDir(path, data)
}
