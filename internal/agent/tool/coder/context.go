package coder

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/lsp"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// 本文件集中了 coder 工具在 gobrave 运行时下取「工作目录 / 会话 / 服务能力」的方式。
//
// opencode 的工具体直接持有 history.Service / lspClients / permission.Service；
// gobrave 统一改为从工具执行 ctx 读取（见 tool.WithFileHistory / WithLSPClients），
// 使工具本身无状态、可在 builtin 注册表里单例复用。

// workingDir 返回本次调用的工作目录：优先 RuntimeContext.WorkingDir，其次进程 cwd。
func workingDir(ctx context.Context) string {
	if rc := tool.RuntimeContextFrom(ctx); rc != nil && rc.WorkingDir != "" {
		return rc.WorkingDir
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

// fileHistory 返回注入的文件历史服务；未注入时返回 nil。
func fileHistory(ctx context.Context) agentinterfaces.HistoryService {
	return tool.FileHistoryFrom(ctx)
}

// lspClients 返回注入的 LSP 客户端集合。
func lspClients(ctx context.Context) map[string]*lsp.Client {
	return tool.LSPClientsFrom(ctx)
}

// permissionPath 返回文件操作的授权范围（对齐 opencode）：
// 文件位于工作目录内时收敛到工作目录，否则取文件所在目录。
func permissionPath(ctx context.Context, filePath string) string {
	root := workingDir(ctx)
	if strings.HasPrefix(filePath, root) {
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
