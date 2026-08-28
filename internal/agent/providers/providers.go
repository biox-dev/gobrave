// Package providers 提供 agent.Provider 的内置实现。
//
// 目前除 mock 外均为占位实现：真实调用后续按 Provider 逐个接入
// （例如 copilot 可复用现有 copilot-sdk 的 bridge 逻辑）。
package providers

import "github.com/biox-dev/gobrave/internal/agent"

// All 返回框架内置的全部 Provider，供容器启动时统一注册。
// 新增 Provider 时在此追加。
func All() []agent.Provider {
	return []agent.Provider{
		NewMock(),
		NewClaudeCode(),
		NewCodex(),
		NewCopilot(),
		NewCustom(),
	}
}
