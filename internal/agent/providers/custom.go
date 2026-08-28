package providers

import "github.com/biox-dev/gobrave/internal/agent"

// NewCustom 创建自研 Agent Provider（占位，后续接入团队自研 Agent 实现）。
func NewCustom() agent.Provider {
	return notImplementedProvider{name: agent.ProviderCustom}
}
