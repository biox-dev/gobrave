package prepare

import (
	"errors"
	"os"
	"strings"
)

var (
	errEmptyCommandPath = errors.New("command path is empty")
	errEmptyCommand     = errors.New("command is empty")
)

// WriteCommandRunScript 把命令正文写入命令节点的 run.sh。
//
// 命令节点的命令不再落库（AnalysisNode 没有 Command 字段）：创建 / 更新入口
// （create_agent_analysis、update_agent_analysis、手动建节点）直接调用本函数写盘，
// Prepare 只负责目录、params.json 与 env.sh，不会覆盖 run.sh。
//
// 内容口径：去掉首尾空白后补齐结尾换行；命令为空时返回错误，避免写出空脚本。
func WriteCommandRunScript(commandPath, command string) error {
	path := strings.TrimSpace(commandPath)
	if path == "" {
		return errEmptyCommandPath
	}
	content := RunScriptContent(command)
	if content == "" {
		return errEmptyCommand
	}
	return writeTextAtomic(path, content, 0o755)
}

// ReadCommandRunScript 读取命令节点 run.sh 的正文。
//
// 路径为空或文件尚未生成时返回空串与 nil，调用方据此按「还没有命令」处理
// （例如授权预览里的旧值 diff、更新前的变更判断）。
func ReadCommandRunScript(commandPath string) (string, error) {
	path := strings.TrimSpace(commandPath)
	if path == "" {
		return "", nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(content), nil
}

// RunScriptContent 返回命令写入 run.sh 的标准正文：去掉首尾空白并补齐结尾换行
// （命令为空时返回空串）。写入与授权预览共用同一口径，避免 diff 与实际文件不一致。
func RunScriptContent(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	if !strings.HasSuffix(command, "\n") {
		command += "\n"
	}
	return command
}
