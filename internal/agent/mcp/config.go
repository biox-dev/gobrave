// Package mcp 把外部 MCP（Model Context Protocol）服务提供的工具接入 gobrave 的
// 工具框架（从 opencode internal/llm/agent/mcp-tools.go 迁移）。
//
// 与 opencode 的差异：
//   - 工具不再实现 opencode 的 tools.BaseTool，而是直接实现 gobrave 的 tool.Tool，
//     因此能被 tool.Registry / ToolRunner 统一调度（权限门禁、事件流都复用）；
//   - 配置不再读取 opencode 的全局 config，而是由调用方显式传入 Server 列表；
//   - 不再把工具缓存在包级变量里，改为 Manager 实例持有缓存，便于测试与多实例。
package mcp

// Type 是 MCP 服务的传输类型。
type Type string

const (
	// TypeStdio 通过子进程 stdin/stdout 通信。
	TypeStdio Type = "stdio"
	// TypeSSE 通过 HTTP SSE 通信。
	TypeSSE Type = "sse"
)

// Server 是一个 MCP 服务的连接配置。
type Server struct {
	// Type 是传输类型：stdio / sse。
	Type Type
	// Command / Args / Env 仅 stdio 使用。
	Command string
	Args    []string
	Env     []string
	// URL / Headers 仅 sse 使用。
	URL     string
	Headers map[string]string
}
