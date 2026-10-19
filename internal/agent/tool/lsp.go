package tool

import (
	"context"

	"github.com/biox-dev/gobrave/internal/agent/lsp"
)

// 本文件把 LSP 客户端集合作为进程级服务能力挂到工具执行上下文上。
//
// 集合来自 lsp/manager.Manager.Clients()（进程启动时拉起各语言服务），由上层注入一次；
// diagnostics / view / edit / write / patch 等工具通过 LSPClientsFrom 取值。

// lspClientsKey 是 LSP 客户端集合在 context 中的私有键类型。
type lspClientsKey struct{}

// WithLSPClients 注入 LSP 客户端集合；nil 时返回原 context。
func WithLSPClients(ctx context.Context, clients map[string]*lsp.Client) context.Context {
	if ctx == nil || len(clients) == 0 {
		return ctx
	}
	return context.WithValue(ctx, lspClientsKey{}, clients)
}

// LSPClientsFrom 取出注入的 LSP 客户端集合；未注入时返回空 map。
func LSPClientsFrom(ctx context.Context) map[string]*lsp.Client {
	if ctx == nil {
		return nil
	}
	clients, _ := ctx.Value(lspClientsKey{}).(map[string]*lsp.Client)
	return clients
}
