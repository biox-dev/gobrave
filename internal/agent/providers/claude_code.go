package providers

import "github.com/biox-dev/gobrave/internal/agent"

// NewClaudeCode 创建 Claude Code Provider（占位，待接入 Anthropic Claude Code CLI 真实调用）。
func NewClaudeCode() agent.Provider {
	return notImplementedProvider{name: agent.ProviderClaudeCode}
}
