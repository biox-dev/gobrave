package config

import (
	"strings"
)

// 模型厂商（Provider）预置配置。
//
// 目录中只保存「看图就能确定的」信息：厂商类型、默认 base_url、常见模型名与展示名。
// 其余可调参数（上下文窗口 / 最大输出 / 是否支持推理 / 价格 / 推理强度）通过 Overrides
// 补充，见 ModelVendorOverride。
//
// 设计要点：
//   - config.agent.providers 中的条目与预置条目按模型名合并：用户显式填写的字段优先，
//     未填写的字段由预置补齐，因此「选厂商 + 填 API Key」即可完成模型配置；
//   - providers 表的 key 是模型名（与 Profile.model 对应），统一小写；
//   - base_url 留空表示使用厂商 SDK 的默认地址（这里只记录可读的默认值用于展示）。
const (
	// ModelVendorOpenAI OpenAI 官方（含所有 OpenAI 兼容网关）。
	ModelVendorOpenAI = "openai"
	// ModelVendorAnthropic Anthropic 官方（Claude）。
	ModelVendorAnthropic = "anthropic"
	// ModelVendorDeepSeek 深度求索（OpenAI 兼容协议）。
	ModelVendorDeepSeek = "deepseek"
	// ModelVendorGemini Google Gemini。
	ModelVendorGemini = "gemini"
	// ModelVendorOpenRouter OpenRouter 聚合网关（OpenAI 兼容协议）。
	ModelVendorOpenRouter = "openrouter"
	// ModelVendorXAI xAI Grok（OpenAI 兼容协议）。
	ModelVendorXAI = "xai"
	// ModelVendorGroq Groq 推理云（OpenAI 兼容协议）。
	ModelVendorGroq = "groq"
	// ModelVendorAzure Azure OpenAI。
	ModelVendorAzure = "azure"
	// ModelVendorVertexAI Google Vertex AI。
	ModelVendorVertexAI = "vertexai"
	// ModelVendorBedrock AWS Bedrock。
	ModelVendorBedrock = "bedrock"
)

// ModelVendorPreset 描述一个模型厂商的预置信息。
type ModelVendorPreset struct {
	// Type 是厂商类型，写入 ModelProviderConfig.Provider（决定 SDK 走哪条客户端）。
	Type string `json:"type"`
	// Name 是展示名（UI 下拉框使用）。
	Name string `json:"name"`
	// BaseURL 是厂商默认 API 基地址；留空表示由 SDK 决定（如 azure / bedrock）。
	BaseURL string `json:"base_url"`
	// APIModelPrefix 是该厂商常见的模型名前缀，用于自动补全 APIModel
	// （例如 deepseek 的 deepseek-v4-flash 直接就是真实模型名，前缀即模型名本身）。
	APIModelPrefix string `json:"api_model_prefix,omitempty"`
	// Models 是常见模型名（可为空，用户也可以自行填写）。
	Models []string `json:"models"`
}

// ModelVendorOverride 补充单个厂商中某个模型的能力元信息。
//
// 预置模型表只覆盖「常用且稳定」的取值；未命中的模型会保留 PATCH 请求中的值
// （或 ModelProviderConfig 的默认值），因此新增模型无需改代码即可使用。
type ModelVendorOverride map[string]ModelProviderConfig

// modelVendorCatalog 是内置厂商目录（按 UI 展示顺序）。
var modelVendorCatalog = []ModelVendorPreset{
	{
		Type:           ModelVendorOpenAI,
		Name:           "OpenAI",
		BaseURL:        "https://api.openai.com/v1",
		APIModelPrefix: "gpt-",
		Models:         []string{"gpt-4.1", "gpt-4.1-mini", "gpt-4o", "gpt-4o-mini", "o3", "o4-mini"},
	},
	{
		Type:           ModelVendorAnthropic,
		Name:           "Anthropic (Claude)",
		BaseURL:        "https://api.anthropic.com",
		APIModelPrefix: "claude-",
		Models: []string{
			"claude-sonnet-4-20250514",
			"claude-opus-4-20250514",
			"claude-3-7-sonnet-20250219",
			"claude-3-5-haiku-20241022",
		},
	},
	{
		Type:           ModelVendorDeepSeek,
		Name:           "DeepSeek",
		BaseURL:        "https://api.deepseek.com/v1",
		APIModelPrefix: "deepseek-",
		Models:         []string{"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-chat", "deepseek-reasoner"},
	},
	{
		Type:           ModelVendorGemini,
		Name:           "Google Gemini",
		BaseURL:        "https://generativelanguage.googleapis.com",
		APIModelPrefix: "gemini-",
		Models:         []string{"gemini-2.5-pro", "gemini-2.5-flash"},
	},
	{
		Type:           ModelVendorOpenRouter,
		Name:           "OpenRouter",
		BaseURL:        "https://openrouter.ai/api/v1",
		APIModelPrefix: "",
		Models:         []string{},
	},
	{
		Type:           ModelVendorXAI,
		Name:           "xAI (Grok)",
		BaseURL:        "https://api.x.ai/v1",
		APIModelPrefix: "grok-",
		Models:         []string{"grok-4", "grok-3", "grok-3-mini"},
	},
	{
		Type:           ModelVendorGroq,
		Name:           "Groq",
		BaseURL:        "https://api.groq.com/openai/v1",
		APIModelPrefix: "",
		Models:         []string{"llama-3.3-70b-versatile", "qwen/qwen3-32b"},
	},
	{
		Type:           ModelVendorAzure,
		Name:           "Azure OpenAI",
		BaseURL:        "",
		APIModelPrefix: "",
		Models:         []string{},
	},
	{
		Type:           ModelVendorVertexAI,
		Name:           "Google Vertex AI",
		BaseURL:        "",
		APIModelPrefix: "",
		Models:         []string{},
	},
	{
		Type:           ModelVendorBedrock,
		Name:           "AWS Bedrock",
		BaseURL:        "",
		APIModelPrefix: "",
		Models:         []string{},
	},
}

// modelVendorOverrides 是预置模型的能力元信息（按厂商类型分组、模型名小写）。
//
// 只补充那些「厂商稳定且能被文档确认」的取值；未列出的模型保留用户填写的值。
var modelVendorOverrides = map[string]ModelVendorOverride{
	ModelVendorOpenAI: {
		"gpt-4.1":      {ContextWindow: 1_047_576, DefaultMaxTokens: 32_768, SupportsAttachments: true, CostPer1MIn: 2, CostPer1MOut: 8, CostPer1MInCached: 0.5},
		"gpt-4.1-mini": {ContextWindow: 1_047_576, DefaultMaxTokens: 32_768, SupportsAttachments: true, CostPer1MIn: 0.4, CostPer1MOut: 1.6, CostPer1MInCached: 0.1},
		"gpt-4o":       {ContextWindow: 128_000, DefaultMaxTokens: 16_384, SupportsAttachments: true, CostPer1MIn: 2.5, CostPer1MOut: 10, CostPer1MInCached: 1.25},
		"gpt-4o-mini":  {ContextWindow: 128_000, DefaultMaxTokens: 16_384, SupportsAttachments: true, CostPer1MIn: 0.15, CostPer1MOut: 0.6, CostPer1MInCached: 0.075},
		"o3":           {ContextWindow: 200_000, DefaultMaxTokens: 100_000, CanReason: true, SupportsAttachments: true, ReasoningEffort: "medium", CostPer1MIn: 2, CostPer1MOut: 8, CostPer1MInCached: 0.5},
		"o4-mini":      {ContextWindow: 200_000, DefaultMaxTokens: 100_000, CanReason: true, SupportsAttachments: true, ReasoningEffort: "medium", CostPer1MIn: 1.1, CostPer1MOut: 4.4, CostPer1MInCached: 0.275},
	},
	ModelVendorAnthropic: {
		"claude-sonnet-4-20250514":   {ContextWindow: 200_000, DefaultMaxTokens: 64_000, CanReason: true, SupportsAttachments: true, CostPer1MIn: 3, CostPer1MOut: 15, CostPer1MInCached: 0.3, CostPer1MOutCached: 3.75},
		"claude-opus-4-20250514":     {ContextWindow: 200_000, DefaultMaxTokens: 32_000, CanReason: true, SupportsAttachments: true, CostPer1MIn: 15, CostPer1MOut: 75, CostPer1MInCached: 1.5, CostPer1MOutCached: 18.75},
		"claude-3-7-sonnet-20250219": {ContextWindow: 200_000, DefaultMaxTokens: 64_000, CanReason: true, SupportsAttachments: true, CostPer1MIn: 3, CostPer1MOut: 15, CostPer1MInCached: 0.3, CostPer1MOutCached: 3.75},
		"claude-3-5-haiku-20241022":  {ContextWindow: 200_000, DefaultMaxTokens: 8_192, SupportsAttachments: true, CostPer1MIn: 0.8, CostPer1MOut: 4, CostPer1MInCached: 0.08, CostPer1MOutCached: 1},
	},
	ModelVendorDeepSeek: {
		"deepseek-v4-flash": {ContextWindow: 128_000, DefaultMaxTokens: 8_192, CanReason: true},
		"deepseek-v4-pro":   {ContextWindow: 128_000, DefaultMaxTokens: 8_192, CanReason: true},
		"deepseek-chat":     {ContextWindow: 64_000, DefaultMaxTokens: 8_192},
		"deepseek-reasoner": {ContextWindow: 64_000, DefaultMaxTokens: 8_192, CanReason: true},
	},
	ModelVendorGemini: {
		"gemini-2.5-pro":   {ContextWindow: 1_048_576, DefaultMaxTokens: 65_536, CanReason: true, SupportsAttachments: true, CostPer1MIn: 1.25, CostPer1MOut: 10},
		"gemini-2.5-flash": {ContextWindow: 1_048_576, DefaultMaxTokens: 65_536, CanReason: true, SupportsAttachments: true, CostPer1MIn: 0.3, CostPer1MOut: 2.5},
	},
	ModelVendorXAI: {
		"grok-4":      {ContextWindow: 256_000, DefaultMaxTokens: 16_384, CanReason: true, SupportsAttachments: true, CostPer1MIn: 3, CostPer1MOut: 15},
		"grok-3":      {ContextWindow: 131_072, DefaultMaxTokens: 16_384, SupportsAttachments: true, CostPer1MIn: 3, CostPer1MOut: 15},
		"grok-3-mini": {ContextWindow: 131_072, DefaultMaxTokens: 16_384, CanReason: true, CostPer1MIn: 0.3, CostPer1MOut: 0.5},
	},
	ModelVendorGroq: {
		"llama-3.3-70b-versatile": {ContextWindow: 128_000, DefaultMaxTokens: 32_768},
	},
}

// ModelVendors 返回内置厂商目录（按 UI 展示顺序）。
func ModelVendors() []ModelVendorPreset {
	out := make([]ModelVendorPreset, 0, len(modelVendorCatalog))
	for _, v := range modelVendorCatalog {
		// 拷贝 Models，避免调用方修改到全局预置。
		models := make([]string, len(v.Models))
		copy(models, v.Models)
		v.Models = models
		out = append(out, v)
	}
	return out
}

// FindModelVendor 按厂商类型查找预置信息（大小写与首尾空白不敏感）。
func FindModelVendor(vendorType string) (ModelVendorPreset, bool) {
	key := strings.ToLower(strings.TrimSpace(vendorType))
	for _, v := range modelVendorCatalog {
		if v.Type == key {
			return v, true
		}
	}
	return ModelVendorPreset{}, false
}

// DefaultModelProviderConfig 返回某个厂商/模型的内置默认配置（不含凭据）。
//
// 优先使用厂商预置的默认 base_url 与模型能力元信息；未命中时返回一个只带
// Provider / ModelID / APIModel 的最小可用配置。
func DefaultModelProviderConfig(vendorType, modelName string) ModelProviderConfig {
	vendor := strings.ToLower(strings.TrimSpace(vendorType))
	model := strings.ToLower(strings.TrimSpace(modelName))

	cfg := ModelProviderConfig{
		ModelID:  model,
		Provider: vendor,
		APIModel: model,
	}
	if preset, ok := FindModelVendor(vendor); ok {
		cfg.BaseURL = strings.TrimSpace(preset.BaseURL)
	}
	if override, ok := modelVendorOverrides[vendor][model]; ok {
		if override.ContextWindow > 0 {
			cfg.ContextWindow = override.ContextWindow
		}
		if override.DefaultMaxTokens > 0 {
			cfg.DefaultMaxTokens = override.DefaultMaxTokens
		}
		cfg.CanReason = override.CanReason
		cfg.SupportsAttachments = override.SupportsAttachments
		cfg.CostPer1MIn = override.CostPer1MIn
		cfg.CostPer1MOut = override.CostPer1MOut
		cfg.CostPer1MInCached = override.CostPer1MInCached
		cfg.CostPer1MOutCached = override.CostPer1MOutCached
		cfg.ReasoningEffort = override.ReasoningEffort
	}
	return cfg
}

// defaultBuiltinModelProviders 返回代码内置的模型提供商配置表（按模型名索引）。
//
// 它是 config.yml 未配置任何 provider 时的兜底表：使用内置默认 Profile 的模型名
// （deepseek-v4-flash），API Key 留空，由用户在可视化配置页填写。
func defaultBuiltinModelProviders() map[string]ModelProviderConfig {
	const builtinModel = "deepseek-v4-flash"
	return map[string]ModelProviderConfig{
		builtinModel: DefaultModelProviderConfig(ModelVendorDeepSeek, builtinModel),
	}
}

// MergeModelProviderConfig 把「用户显式填写的值」合并到「默认值」之上。
//
// 规则：字符串 / 数值 / 布尔字段为「零值」时保留默认值，否则以用户值为准。
// 这样 UI 只需提交 provider + api_model（或模型名）+ api_key，其余元信息自动补齐。
func MergeModelProviderConfig(base, override ModelProviderConfig) ModelProviderConfig {
	if v := strings.TrimSpace(override.ModelID); v != "" {
		base.ModelID = v
	}
	if v := strings.TrimSpace(override.BaseURL); v != "" {
		base.BaseURL = v
	}
	if v := strings.TrimSpace(override.APIKey); v != "" {
		base.APIKey = v
	}
	if v := strings.TrimSpace(override.Provider); v != "" {
		base.Provider = v
	}
	if v := strings.TrimSpace(override.APIModel); v != "" {
		base.APIModel = v
	}
	if v := strings.TrimSpace(override.Name); v != "" {
		base.Name = v
	}
	if override.ContextWindow != 0 {
		base.ContextWindow = override.ContextWindow
	}
	if override.DefaultMaxTokens != 0 {
		base.DefaultMaxTokens = override.DefaultMaxTokens
	}
	// 布尔字段：只在用户显式置 true 时覆盖默认值（false 视为未填写）。
	if override.CanReason {
		base.CanReason = true
	}
	if override.SupportsAttachments {
		base.SupportsAttachments = true
	}
	if override.CostPer1MIn != 0 {
		base.CostPer1MIn = override.CostPer1MIn
	}
	if override.CostPer1MOut != 0 {
		base.CostPer1MOut = override.CostPer1MOut
	}
	if override.CostPer1MInCached != 0 {
		base.CostPer1MInCached = override.CostPer1MInCached
	}
	if override.CostPer1MOutCached != 0 {
		base.CostPer1MOutCached = override.CostPer1MOutCached
	}
	if v := strings.TrimSpace(override.ReasoningEffort); v != "" {
		base.ReasoningEffort = v
	}
	if len(override.Extra) > 0 {
		merged := make(map[string]string, len(base.Extra)+len(override.Extra))
		for k, v := range base.Extra {
			merged[k] = v
		}
		for k, v := range override.Extra {
			merged[k] = v
		}
		base.Extra = merged
	}
	return base
}

// mergeModelProviderTable 以 defaults 为底、entries 为显式覆盖，合并成最终配置表。
func mergeModelProviderTable(defaults, entries map[string]ModelProviderConfig) map[string]ModelProviderConfig {
	out := make(map[string]ModelProviderConfig, len(defaults)+len(entries))
	for name, pc := range defaults {
		out[name] = pc
	}
	for name, pc := range entries {
		out[name] = MergeModelProviderConfig(out[name], pc)
	}
	return out
}
