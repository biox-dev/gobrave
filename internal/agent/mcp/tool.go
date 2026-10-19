package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
)

// clientName 是注册到 MCP 服务端的客户端名（便于服务端日志区分来源）。
const clientName = "gobrave"

// MCPServer 是一个 MCP 工具对应的服务端连接。
type MCPServer struct {
	// Name 是配置中的服务名，工具名会被拼成 <server>_<tool>。
	Name string
	// Config 是该服务的连接配置。
	Config Server
	// Tool 是发现的工具定义。
	Tool mcp.Tool
}

// Tool 实现 tool.Tool：每次执行时新建连接 → initialize → CallTool → 关闭。
//
// 之所以不常驻连接，是为了避免 stdio 子进程长期存活、以及 SSE 连接掉线后的
// 重连状态管理；MCP 服务本身通常启动很快（与 opencode 行为一致）。
type Tool struct {
	server *MCPServer
}

// Discover 连接各 MCP 服务并列出其工具。
//
// 单个服务连接 / 列工具失败只记录日志并跳过，不会让整个发现流程失败。
func Discover(ctx context.Context, servers map[string]Server) []tool.Tool {
	out := make([]tool.Tool, 0, len(servers))
	for name, cfg := range servers {
		found, err := listTools(ctx, name, cfg)
		if err != nil {
			logger.GetLogger(ctx).Warnf("mcp: 无法加载服务 %s 的工具: %v", name, err)
			continue
		}
		for i := range found {
			out = append(out, &Tool{server: &found[i]})
		}
	}
	return out
}

// Manager 持有 MCP 工具发现结果（懒加载 + 进程内缓存）。
//
// 配置在构造时固定；如需热更新配置，请重新创建 Manager。
type Manager struct {
	servers map[string]Server

	mu    sync.Mutex
	tools []tool.Tool
	done  bool
}

// NewManager 创建 MCP 工具管理器。
func NewManager(servers map[string]Server) *Manager {
	return &Manager{servers: servers}
}

// Tools 返回 MCP 工具（首次调用时发现并缓存）。
func (m *Manager) Tools(ctx context.Context) []tool.Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.done {
		m.tools = Discover(ctx, m.servers)
		m.done = true
	}
	return m.tools
}

// Definition 实现 tool.Tool。
func (t *Tool) Definition() tool.Definition {
	required := t.server.Tool.InputSchema.Required
	if required == nil {
		required = []string{}
	}
	name := fmt.Sprintf("%s_%s", t.server.Name, t.server.Tool.Name)
	return tool.Definition{
		Name:        name,
		Description: t.server.Tool.Description,
		InputSchema: map[string]any{
			"type":       "object",
			"properties": t.server.Tool.InputSchema.Properties,
			"required":   required,
		},
	}
}

// Execute 实现 tool.Tool：连接服务 → initialize → 调用工具 → 返回文本结果。
func (t *Tool) Execute(ctx context.Context, args json.RawMessage) (any, error) {
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	var arguments map[string]any
	if err := json.Unmarshal(args, &arguments); err != nil {
		return nil, fmt.Errorf("mcp: invalid arguments: %w", err)
	}

	// MCP 工具的行为由外部服务决定（可能读写文件 / 访问网络），
	// 执行前统一请求 execute 授权（工具名与 Definition 保持一致）。
	name := fmt.Sprintf("%s_%s", t.server.Name, t.server.Tool.Name)
	if err := tool.RequirePermission(ctx, tool.PermissionRequest{
		ToolName:    name,
		Action:      "execute",
		Description: fmt.Sprintf("execute MCP tool %s", name),
		Params:      arguments,
	}); err != nil {
		return nil, err
	}

	c, err := newClient(t.server.Config)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	if err := initialize(ctx, c); err != nil {
		return nil, err
	}

	req := mcp.CallToolRequest{}
	req.Params.Name = t.server.Tool.Name
	req.Params.Arguments = arguments

	result, err := c.CallTool(ctx, req)
	if err != nil {
		return nil, err
	}
	return resultText(result), nil
}

// listTools 初始化连接并列出服务端工具。
func listTools(ctx context.Context, name string, cfg Server) ([]MCPServer, error) {
	c, err := newClient(cfg)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	if err := initialize(ctx, c); err != nil {
		return nil, err
	}
	resp, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, err
	}

	out := make([]MCPServer, 0, len(resp.Tools))
	for _, t := range resp.Tools {
		out = append(out, MCPServer{Name: name, Config: cfg, Tool: t})
	}
	return out, nil
}

// newClient 按传输类型创建 MCP 客户端。
func newClient(cfg Server) (client.MCPClient, error) {
	switch cfg.Type {
	case TypeStdio:
		return client.NewStdioMCPClient(cfg.Command, cfg.Env, cfg.Args...)
	case TypeSSE:
		return client.NewSSEMCPClient(cfg.URL, client.WithHeaders(cfg.Headers))
	default:
		return nil, fmt.Errorf("mcp: invalid server type %q", cfg.Type)
	}
}

// initialize 完成 MCP 握手。
func initialize(ctx context.Context, c client.MCPClient) error {
	req := mcp.InitializeRequest{}
	req.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcp.Implementation{Name: clientName}
	_, err := c.Initialize(ctx, req)
	return err
}

// resultText 把 MCP 返回的内容拼成纯文本（优先 TextContent）。
func resultText(result *mcp.CallToolResult) string {
	if result == nil {
		return ""
	}
	out := ""
	for _, c := range result.Content {
		if text, ok := c.(mcp.TextContent); ok {
			out += text.Text
			continue
		}
		out += fmt.Sprintf("%v", c)
	}
	return out
}
