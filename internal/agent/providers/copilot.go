package providers

import "github.com/biox-dev/gobrave/internal/agent"

// NewCopilot 创建 Copilot Provider（占位，后续可复用现有 copilot-sdk 的 bridge 逻辑）。
func NewCopilot() agent.Provider {
	return notImplementedProvider{name: agent.ProviderCopilot}
}
