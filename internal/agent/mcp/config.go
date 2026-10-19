// Package mcp 把外部 MCP（Model Context Protocol）服务提供的工具接入 gobrave 的
// 工具框架（从 opencode internal/llm/agent/mcp-tools.go 迁移）。
//
// 与 opencode 的差异：
//   - 工具不再实现 opencode 的 tools.BaseTool，而是直接实现 gobrave 的 tool.Tool，
//     因此能被 tool.Registry / ToolRunner 统一调度（权限门禁、事件流都复用）；
//   - 配置不再读取 opencode 的全局 config，而是由调用方显式传入 Server 列表；
//   - 不再把工具缓存在包级变量里，改为 Manager 实例持有缓存，便于测试与多实例。
package mcp

import (
	"sort"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
)

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

// ServersFromConfig 把 mcp 配置段转换为 Server 表。
//
// 总开关关闭、未配置服务或单个服务 enabled=false 时跳过该服务。
// Normalize 会在写入配置前补齐 type 并校验必填项，这里仍做一次兜底推断，
// 以兼容直接构造配置结构体的调用方（如测试）。
func ServersFromConfig(cfg *config.MCPConfig) map[string]Server {
	out := make(map[string]Server)
	if cfg == nil || !cfg.Enabled {
		return out
	}
	for name, sc := range cfg.Servers {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if sc.Enabled != nil && !*sc.Enabled {
			continue
		}
		typ := Type(strings.ToLower(strings.TrimSpace(sc.Type)))
		if typ == "" {
			if strings.TrimSpace(sc.URL) != "" && strings.TrimSpace(sc.Command) == "" {
				typ = TypeSSE
			} else {
				typ = TypeStdio
			}
		}
		out[name] = Server{
			Type:    typ,
			Command: strings.TrimSpace(sc.Command),
			Args:    sc.Args,
			Env:     EnvSlice(sc.Env),
			URL:     strings.TrimSpace(sc.URL),
			Headers: sc.Headers,
		}
	}
	return out
}

// EnvSlice 把 env map 转换为 KEY=VALUE 切片（按 key 排序，保证结果稳定）。
func EnvSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}
