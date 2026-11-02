package coder

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// 本文件集中了 coder 工具在 gobrave 运行时下取「工作目录 / 会话」的方式。
//
// 进程级静态依赖（文件历史 / LSP）已改为构造期注入（见 coder.Deps），
// 这里只保留随请求变化的执行事实。

// workingDir 返回本次调用的工作目录：优先执行事实（TaskFacts.WorkingDir），其次进程 cwd。
func workingDir(ctx context.Context) string {
	if wd := tool.TaskFactsFrom(ctx).WorkingDir; wd != "" {
		return wd
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}

// sessionID 返回本次调用所属会话 ID（0 表示未知）。
func sessionID(ctx context.Context) int64 {
	return tool.SessionIDFrom(ctx)
}

// permissionPath 返回文件操作的授权范围（对齐 opencode）：
// 文件位于工作目录内时收敛到工作目录，否则取文件所在目录。
//
// 注意：不能用 strings.HasPrefix 判断包含关系，因为 "/data/36" 也是
// "/data/360/x" 的前缀，会把工作目录外的文件误判为工作目录内，
// 从而把授权范围放大到整个工作目录。这里统一用 filepath.Rel 判断。
func permissionPath(ctx context.Context, filePath string) string {
	root := workingDir(ctx)
	if rel, err := filepath.Rel(root, filePath); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return root
	}
	return filepath.Dir(filePath)
}

// toolError 把可读错误信息包装为工具执行错误（由上层转成失败结果回传模型）。
func toolError(msg string) error {
	return &coderError{msg: msg}
}

type coderError struct{ msg string }

func (e *coderError) Error() string { return e.msg }
