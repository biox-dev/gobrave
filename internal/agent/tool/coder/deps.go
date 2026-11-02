package coder

import (
	"github.com/biox-dev/gobrave/internal/agent/lsp"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/config"
)

// Deps 是 coder 工具集的进程级静态依赖，由 container 在构建工具注册表时一次性装配。
//
// 与 builtin.Deps 一样，这些依赖通过构造函数注入，而不是每次执行时从 context 读取：
// 工具依赖对编译器可见、可单测，provider 侧也无需维护逐能力的注入清单。
type Deps struct {
	// History 是会话内文件版本历史服务（edit / write / patch 记录版本）。
	History agentinterfaces.HistoryService
	// LSP 是 LSP 客户端提供者（diagnostics / view / edit / write / patch 读取诊断）。
	//
	// 以「提供者」而不是客户端 map 注入：Clients() 在每次执行时取当前快照，
	// 避免工具在构造期就把客户端集合固化下来。
	LSP LSPClientProvider
	// Config 是进程配置快照，供需要出站网络的工具（如 fetch）读取代理设置。
	// 为 nil 或未配置代理时直连（不读取环境变量）。
	Config *config.Config
}

// LSPClientProvider 抽象「提供当前 LSP 客户端集合」的能力。
//
// *lspmanager.Manager 直接满足该接口，避免 coder 包依赖 manager 的具体类型。
type LSPClientProvider interface {
	Clients() map[string]*lsp.Client
}

// lspClients 返回当前 LSP 客户端快照；未配置时返回 nil。
func (d Deps) lspClients() map[string]*lsp.Client {
	if d.LSP == nil {
		return nil
	}
	return d.LSP.Clients()
}
