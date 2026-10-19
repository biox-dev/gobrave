package provider

import (
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/llm/models"
)

// maxRetries 是遇到限流 / 5xx 时的最大重试次数。
const maxRetries = 8

// New 按厂商类型创建对应的协议客户端。
//
// 目前支持：
//   - OpenAI 及 OpenAI 兼容网关（deepseek / groq / openrouter / xai / local）；
//   - Anthropic（Claude）；
//   - Google Gemini。
//
// azure / vertexai / bedrock 需要额外的认证与部署参数，暂未迁移，调用时返回明确错误。
func New(opts Options) (Client, error) {
	switch opts.Provider {
	case models.ProviderOpenAI, models.ProviderDeepSeek, models.ProviderGroq,
		models.ProviderOpenRouter, models.ProviderXAI, models.ProviderLocal:
		return newOpenAIClient(opts), nil
	case models.ProviderAnthropic:
		return newAnthropicClient(opts), nil
	case models.ProviderGemini:
		return newGeminiClient(opts)
	default:
		return nil, fmt.Errorf("provider: vendor %q is not supported yet", opts.Provider)
	}
}

// defaultBaseURL 返回 OpenAI 兼容厂商的默认 API 基地址。
//
// 宿主显式配置的 BaseURL 始终优先（见 newOpenAIClient）。
func defaultBaseURL(p models.ModelProvider) string {
	switch p {
	case models.ProviderDeepSeek:
		return "https://api.deepseek.com/v1"
	case models.ProviderGroq:
		return "https://api.groq.com/openai/v1"
	case models.ProviderOpenRouter:
		return "https://openrouter.ai/api/v1"
	case models.ProviderXAI:
		return "https://api.x.ai/v1"
	default:
		return ""
	}
}

// resolveBaseURL 在宿主未显式配置时回退到厂商默认地址。
func resolveBaseURL(opts Options) string {
	if base := strings.TrimSpace(opts.BaseURL); base != "" {
		return base
	}
	return defaultBaseURL(opts.Provider)
}
