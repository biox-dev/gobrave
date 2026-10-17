package utils

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/builtin"
)

// 内置提示词目录：仓库根目录下的 builtin/prompts。
//
// 内置 Profile 的 SystemPrompt（pongo2 模板）不再硬编码在 Go 代码里，而是以
// “一个文件 = 一个提示词”的形式放在该目录下：文件名即提示词名（不含 .md 后缀，
// 例如 builtin/prompts/default.md → BuiltinPromptDefault）。
// 修改提示词只需改文件，无需改代码；提示词名与文件名的映射见 internal/agent/profile.go。
//
// 同一份文件既会被 go:embed 编进二进制（builtin.PromptFiles），也会被运行时从
// 磁盘优先读取，因此部署时可直接覆盖磁盘文件而无需重新构建。
const BuiltinPromptDir = "builtin/prompts"

// BuiltinPromptFileName 返回内置提示词名对应的文件名（name + ".md"）。
func BuiltinPromptFileName(name string) string {
	return strings.TrimSpace(name) + ".md"
}

// BuiltinPromptPath 返回内置提示词文件的绝对路径。
// baseDir 为项目根目录（工作目录）；为空时按当前工作目录解析。
func BuiltinPromptPath(baseDir, name string) string {
	return filepath.Join(baseDir, BuiltinPromptDir, BuiltinPromptFileName(name))
}

// ReadBuiltinPromptFile 读取内置提示词文件的原始内容。
//
// 读取顺序（两级回退）：
//  1. 先读磁盘 BuiltinPromptPath(baseDir, name)：允许运维在部署目录直接覆盖提示词；
//  2. 磁盘上不存在时，再读编译进二进制的 builtin.PromptFiles（go:embed），
//     保证单文件 / Docker 部署（镜像里不含 builtin/ 目录）也能正常工作。
//
// 两处都不存在时返回空串（不视为错误），调用方可以据此回退到空提示词；
// 文件存在但读取失败时返回错误，避免把 IO 故障静默当成“提示词为空”。
func ReadBuiltinPromptFile(baseDir, name string) (string, error) {
	return readPromptFile(BuiltinPromptPath(baseDir, name), name)
}

// ReadBuiltinPrompt 读取并规整内置提示词（去除首尾空白）。
//
// 文件不存在或内容为空白时返回空串；落盘文件带结尾换行，TrimSpace 后与
// 原来硬编码的 SystemPrompt 字符串保持一致，避免提示词末尾多余空行漂移。
func ReadBuiltinPrompt(baseDir, name string) (string, error) {
	content, err := ReadBuiltinPromptFile(baseDir, name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(content), nil
}

// WriteBuiltinPrompt 覆盖写入内置提示词文件，目录不存在时自动创建。
//
// 供工具 / 管理脚本批量导出、修正提示词使用；运行期读取走 ReadBuiltinPrompt。
func WriteBuiltinPrompt(baseDir, name, content string) error {
	return WriteFileEnsureDir(BuiltinPromptPath(baseDir, name), []byte(content))
}

// readPromptFile 先读指定路径的提示词文件，不存在时回退到编译进二进制的内嵌副本；
// 两处都不存在时返回空串（不视为错误）。
func readPromptFile(filePath, name string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		// 磁盘上没有：回退到 go:embed 内嵌的提示词。
		embedded, embedErr := builtin.PromptFiles.ReadFile(
			path.Join(builtin.PromptFSRoot, BuiltinPromptFileName(name)),
		)
		if embedErr != nil {
			if errors.Is(embedErr, fs.ErrNotExist) {
				return "", nil
			}
			return "", embedErr
		}
		return string(embedded), nil
	}
	return string(data), nil
}
