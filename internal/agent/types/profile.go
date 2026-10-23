package types

import (
	"errors"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// 内置 Profile 使用固定的负数 ID，避免与雪花算法生成的正整数主键冲突。
// Profile 的查询 / 引用一律使用 ID；Name 仅用于展示。
const (
	BuiltinDefaultProfileID int64 = -1 // 内置默认 Profile
	BuiltinAnalysisCoderID  int64 = -2 // 内置「分析代码编写」Profile
	BuiltinArticleWriterID  int64 = -3 // 内置「科研文章撰写」Profile
	BuiltinSummaryProfileID int64 = -4 // 内置「摘要总结」Profile
)

// BuiltinDefaultProfileName 是内置默认 Profile 的展示名。用于无需查询数据库即可获得
// 展示名的场景（如 DefaultUserAgentConfig）。
const BuiltinDefaultProfileName = "通用助手"

// 内置 Profile 的 SystemPrompt 不硬编码在代码里，而是从内置提示词目录
// （utils.BuiltinPromptDir，即 builtin/prompts）读取同名 .md 文件：
// 文件名 = 提示词名（不含 .md 后缀），内容为 pongo2 模板。
const (
	BuiltinPromptDefault       = "default"        // builtin/prompts/default.md
	BuiltinPromptAnalysisCoder = "analysis_coder" // builtin/prompts/analysis_coder.md
	BuiltinPromptArticleWriter = "article_writer" // builtin/prompts/article_writer.md
	BuiltinPromptSummary       = "summary"        // builtin/prompts/summary.md
	// BuiltinPromptBaseCoder 是所有 Profile 共用的基础编码提示词（拷自 opencode 的
	// baseAnthropicCoderPrompt），作为系统提示词的前置部分统一注入，见 buildSystemPrompt。
	BuiltinPromptBaseCoder = "base_coder" // builtin/prompts/base_coder.md
)

// Profile 相关错误。
var (
	// ErrProfileNotFound 表示 Profile 不存在。
	ErrProfileNotFound = errors.New("agent: profile not found")
	// ErrProfileNameRequired 表示 Profile 名称缺失。
	ErrProfileNameRequired = errors.New("agent: profile name is required")
)

// ContextConfig 控制 AgentService 在调用前注入哪些背景。
//
//   - InjectMemory：检索长期记忆并拼进 SystemPrompt。
type ContextConfig struct {
	InjectMemory bool `json:"inject_memory"`
}

// Profile 描述一类任务的 Agent 配置（系统提示词 + 技能 + 上下文注入开关）。
//
// 与 Options / Request（每次调用的动态参数）不同，Profile 是「按任务类型选择」的静态
// 配置快照：HTTP 层或前端在发起调用时通过 Request.ProfileID 指定 Profile，AgentService
// 在调用前按 ID 解析并应用（合并系统提示词、过滤技能、按开关注入背景）。
//
// Name 仅用于展示；Profile 的查询 / 引用一律使用 ID。
// Profile 不再按用户隔离：所有 Profile 全局可见。
type Profile struct {
	ID int64 `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	// Name 仅作为展示名使用，不参与任何查询。
	Name         string `json:"name" gorm:"column:name;type:varchar(128)"`
	Description  string `json:"description" gorm:"column:description;type:text"`
	IsDefault    bool   `json:"is_default" gorm:"column:is_default"`
	IsBuiltin    bool   `json:"is_builtin" gorm:"column:is_builtin"`
	SystemPrompt string `json:"system_prompt" gorm:"column:system_prompt;type:text"`
	// Model 指定本 Profile 使用的模型名（对应 config.agent.providers 的 key）。
	// 为空时回退到请求级 req.Model（若仍未指定则由 Provider 兜底）。
	Model string `json:"model" gorm:"column:model;type:varchar(128)"`
	// Provider 指定本 Profile 使用的 Agent（Provider）名称（mock | claude_code | codex | copilot | custom）。
	// 为空时回退到请求级 req.Provider（若仍未指定则由 Client 的默认 Provider 兜底）。
	Provider string        `json:"provider" gorm:"column:provider;type:varchar(64)"`
	Skills   []string      `json:"skills" gorm:"column:skills;serializer:json"`
	Context  ContextConfig `json:"context" gorm:"column:context;serializer:json"`

	CreatedAt time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt time.Time `json:"updated_at" gorm:"column:updated_at"`
}

// TableName 返回 Profile 表的表名。
func (Profile) TableName() string { return "agent_profiles" }

// BeforeCreate 在写入数据库前用雪花 ID 初始化主键（仅 ID 为 0 时）。
func (p *Profile) BeforeCreate(_ *gorm.DB) error {
	if p.ID == 0 {
		p.ID = utils.GenerateID()
	}
	return nil
}

// IsBuiltinProfileID 判断给定 ID 是否为内置 Profile。
// 内置 Profile 使用固定的负数 ID，用户自定义 Profile 使用雪花算法生成的正整数 ID。
func IsBuiltinProfileID(id int64) bool {
	return id < 0
}
