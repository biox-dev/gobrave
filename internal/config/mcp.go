package config

import (
	"fmt"
	"strings"
	"time"
)

// DefaultMCPTimeoutSeconds 是启动时发现 MCP 工具的超时时间（秒）。
const DefaultMCPTimeoutSeconds = 60

// MCPConfig 描述外部 MCP（Model Context Protocol）服务配置。
//
// 启用后，服务启动时会连接各 MCP 服务并把其工具注册到 Agent 工具表，
// 工具名为 <服务名>_<工具名>。单个服务连接失败只记录日志并跳过。
type MCPConfig struct {
	// Enabled 是总开关；false 时不会连接任何 MCP 服务。
	Enabled bool `yaml:"enabled" json:"enabled"`
	// TimeoutSeconds 是启动时发现工具的超时时间（秒），<=0 时使用默认值 60。
	TimeoutSeconds int `yaml:"timeout_seconds" json:"timeout_seconds"`
	// Servers 是 MCP 服务表，key 为服务名。
	Servers map[string]MCPServerConfig `yaml:"servers" json:"servers"`
}

// MCPServerConfig 是一个 MCP 服务的连接配置。
type MCPServerConfig struct {
	// Enabled 控制单个服务是否启用；nil 视为启用。
	Enabled *bool `yaml:"enabled" json:"enabled,omitempty"`
	// Type 是传输类型：stdio（默认）/ sse。留空时按 command / url 推断。
	Type string `yaml:"type" json:"type"`
	// Command / Args / Env 仅 stdio 使用。
	Command string            `yaml:"command" json:"command,omitempty"`
	Args    []string          `yaml:"args" json:"args,omitempty"`
	Env     map[string]string `yaml:"env" json:"env,omitempty"`
	// URL / Headers 仅 sse 使用。
	URL     string            `yaml:"url" json:"url,omitempty"`
	Headers map[string]string `yaml:"headers" json:"headers,omitempty"`
}

// DefaultMCPConfig 返回 mcp 段在代码中的默认配置（默认不配置任何服务）。
func DefaultMCPConfig() *MCPConfig {
	return &MCPConfig{
		Enabled:        true,
		TimeoutSeconds: DefaultMCPTimeoutSeconds,
		Servers:        map[string]MCPServerConfig{},
	}
}

// ResolveMCPConfig 返回 nil 安全的 mcp 配置。
func ResolveMCPConfig(cfg *Config) *MCPConfig {
	if cfg == nil || cfg.MCP == nil {
		return DefaultMCPConfig()
	}
	return cfg.MCP
}

// Timeout 返回工具发现超时时间。
func (c *MCPConfig) Timeout() time.Duration {
	seconds := DefaultMCPTimeoutSeconds
	if c != nil && c.TimeoutSeconds > 0 {
		seconds = c.TimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

// normalizeMCPConfig 归一化 mcp 配置：补全默认值、推断传输类型并校验必填项。
func normalizeMCPConfig(cfg *MCPConfig) error {
	if cfg == nil {
		return fmt.Errorf("mcp config is required")
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = DefaultMCPTimeoutSeconds
	}
	if cfg.Servers == nil {
		cfg.Servers = map[string]MCPServerConfig{}
	}

	normalized := make(map[string]MCPServerConfig, len(cfg.Servers))
	for name, server := range cfg.Servers {
		name = strings.TrimSpace(name)
		if name == "" {
			return fmt.Errorf("mcp server name cannot be empty")
		}

		server.Type = strings.ToLower(strings.TrimSpace(server.Type))
		if server.Type == "" {
			if strings.TrimSpace(server.URL) != "" && strings.TrimSpace(server.Command) == "" {
				server.Type = "sse"
			} else {
				server.Type = "stdio"
			}
		}

		// 未启用的服务允许只写名字，不做连接参数校验。
		if server.Enabled != nil && !*server.Enabled {
			normalized[name] = server
			continue
		}

		switch server.Type {
		case "stdio":
			server.Command = strings.TrimSpace(server.Command)
			if server.Command == "" {
				return fmt.Errorf("mcp server %q: command is required for stdio transport", name)
			}
		case "sse":
			server.URL = strings.TrimSpace(server.URL)
			if server.URL == "" {
				return fmt.Errorf("mcp server %q: url is required for sse transport", name)
			}
		default:
			return fmt.Errorf("mcp server %q: unsupported type %q, expected stdio or sse", name, server.Type)
		}
		normalized[name] = server
	}
	cfg.Servers = normalized
	return nil
}
