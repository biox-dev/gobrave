package providers

import (
	agentclient "github.com/biox-dev/gobrave/internal/agent/client"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// NewClaudeCode 创建 Claude Code Provider（占位，待接入 Anthropic Claude Code CLI 真实调用）。
func NewClaudeCode() agentclient.Provider {
	return notImplementedProvider{name: agenttypes.ProviderClaudeCode}
}
