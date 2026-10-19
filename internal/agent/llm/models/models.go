package models

// Package models 定义 LLM 调用所需的模型元信息。
//
// 迁移自 opencode internal/llm/models，但去掉了内置模型目录：作为 gobrave 的
// 原生 Agent 时，模型能力（上下文窗口 / 价格 / 是否支持推理等）全部来自
// config.agent.providers，由 Provider 在每次调用时注入。

// ModelID 是模型标识（对应 config.agent.providers 的 key）。
type ModelID string

// ModelProvider 是模型厂商类型，决定走哪条 LLM 协议客户端。
type ModelProvider string

// 支持的模型厂商类型，取值与 config.ModelVendor* 保持一致。
const (
	ProviderOpenAI     ModelProvider = "openai"
	ProviderAnthropic  ModelProvider = "anthropic"
	ProviderDeepSeek   ModelProvider = "deepseek"
	ProviderGemini     ModelProvider = "gemini"
	ProviderOpenRouter ModelProvider = "openrouter"
	ProviderXAI        ModelProvider = "xai"
	ProviderGroq       ModelProvider = "groq"
	ProviderAzure      ModelProvider = "azure"
	ProviderVertexAI   ModelProvider = "vertexai"
	ProviderBedrock    ModelProvider = "bedrock"
	ProviderLocal      ModelProvider = "local"
)

// Model 描述一次调用所用的模型及其能力元信息。
type Model struct {
	ID                  ModelID       `json:"id"`
	Name                string        `json:"name"`
	Provider            ModelProvider `json:"provider"`
	APIModel            string        `json:"api_model"`
	CostPer1MIn         float64       `json:"cost_per_1m_in"`
	CostPer1MOut        float64       `json:"cost_per_1m_out"`
	CostPer1MInCached   float64       `json:"cost_per_1m_in_cached"`
	CostPer1MOutCached  float64       `json:"cost_per_1m_out_cached"`
	ContextWindow       int64         `json:"context_window"`
	DefaultMaxTokens    int64         `json:"default_max_tokens"`
	CanReason           bool          `json:"can_reason"`
	SupportsAttachments bool          `json:"supports_attachments"`
}
