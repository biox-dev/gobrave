package providers

import (
	agentclient "github.com/biox-dev/gobrave/internal/agent/client"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// NewCodex 创建 Codex Provider（占位，待接入 OpenAI Codex CLI 真实调用）。
func NewCodex() agentclient.Provider {
	return notImplementedProvider{name: agenttypes.ProviderCodex}
}
