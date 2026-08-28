package providers

import "github.com/biox-dev/gobrave/internal/agent"

// NewCodex 创建 Codex Provider（占位，待接入 OpenAI Codex CLI 真实调用）。
func NewCodex() agent.Provider {
	return notImplementedProvider{name: agent.ProviderCodex}
}
