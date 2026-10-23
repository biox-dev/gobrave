// Package providers 提供 agentclient.Provider 的内置实现。
//
// custom 为 gobrave 原生 Agent（见 internal/agent/native）；copilot 基于
// copilot-sdk；claude_code / codex 仍为占位实现，后续按需接入。
package providers

import (
	agentclient "github.com/biox-dev/gobrave/internal/agent/client"
)

// All 返回框架内置的全部 Provider，供容器启动时统一注册。
// 新增 Provider 时在此追加。
func All() []agentclient.Provider {
	return []agentclient.Provider{
		NewMock(),
		NewClaudeCode(),
		NewCodex(),
		NewCopilot(),
		NewCustom(),
	}
}
