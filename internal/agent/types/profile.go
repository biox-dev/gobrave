package agent

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/skill"
	"github.com/biox-dev/gobrave/internal/logger"
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

// builtinPromptDir 是内置提示词目录的绝对路径（相对工作目录解析）。
//
// 可用 BuiltinPromptDir 覆盖：为空时按当前工作目录解析 builtin/prompts，
// 测试或改成 embed 内嵌方案时只需改这一个入口，profile.go 无需再动。
var builtinPromptDir string

// SetBuiltinPromptDir 覆盖内置提示词目录（空串表示恢复默认：相对当前工作目录）。
// 仅用于测试或自定义部署目录；非并发安全，应在服务启动前调用。
func SetBuiltinPromptDir(dir string) {
	builtinPromptDir = strings.TrimSpace(dir)
}

// ReadBuiltinPrompt 按提示词名读取内置提示词文件（如 "default" → builtin/prompts/default.md）。
// 文件不存在时返回空提示词（不报错），读取失败时返回错误。
func ReadBuiltinPrompt(name string) (string, error) {
	return utils.ReadBuiltinPrompt(builtinPromptDir, name)
}

// mustReadBuiltinPrompt 读取内置提示词，失败时降级为空提示词。
//
// BuiltinProfiles 无 error 返回值（测试与调用点都依赖该签名），因此文件缺失时
// 只输出日志并返回空串，让 Agent 仍能启动；提示词内容错误应通过测试 / 日志发现。
func mustReadBuiltinPrompt(name string) string {
	prompt, err := ReadBuiltinPrompt(name)
	if err != nil {
		logger.Warnf(context.Background(), "[Agent] Failed to read builtin prompt %q: %v", name, err)
		return ""
	}
	return prompt
}

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

// BuiltinProfiles 返回框架内置 Profile 的种子数据。新增内置 Profile 时在此追加。
//
// 内置 Profile 仅作为数据库初始化工具：系统启动时由 EnsureBuiltinProfiles 按 ID
// 同步到数据库（不存在则新增、已存在则保留）。所有 Profile 的读取 / 写入 / 解析均
// 走数据库（ProfileRepository），内置 Profile 与用户自定义 Profile 统一存储。
func BuiltinProfiles() []*Profile {
	now := time.Now()
	return []*Profile{
		{
			ID:          BuiltinDefaultProfileID,
			Name:        BuiltinDefaultProfileName,
			Description: "默认配置：不附加领域提示词；注入记忆。",
			IsDefault:   true,
			IsBuiltin:   true,
			// SystemPrompt 为 pongo2 模板：调用方通过 RuntimeContext.SystemPromptVars
			// 提供变量，AgentService 渲染后写入真正的系统提示词。
			// 模板正文来自内置提示词文件（builtin/prompts/default.md），见 mustReadBuiltinPrompt。
			SystemPrompt: mustReadBuiltinPrompt(BuiltinPromptDefault),
			Model:        "deepseek-v4-flash",
			Provider:     ProviderCustom,
			Context:      ContextConfig{InjectMemory: true},
			CreatedAt:    now,
			UpdatedAt:    now,
		},
		{
			ID:           BuiltinAnalysisCoderID,
			Name:         "分析代码编写",
			Description:  "用于撰写生物信息学分析代码：强调代码规范、可复现性与执行约束。",
			IsBuiltin:    true,
			SystemPrompt: mustReadBuiltinPrompt(BuiltinPromptAnalysisCoder),
			// 创建脚本 / io_schema 契约 / 失败迭代流程，以及分析节点内写改脚本的约定，
			// 均已抽取为内置技能（见 skill.Builtin*SkillName），这里显式启用，模型按需 get_skill 加载。
			Skills: []string{
				skill.BuiltinAnalysisScriptSkillName,
				skill.BuiltinAnalysisNodeScriptSkillName,
			},
			Context:   ContextConfig{InjectMemory: true},
			CreatedAt: now,
			Model:     "deepseek-v4-flash",
			Provider:  ProviderCustom,
			UpdatedAt: now,
		},
		{
			ID:           BuiltinArticleWriterID,
			Name:         "科研文章撰写",
			Description:  "用于撰写科研报告 / 文章：按学术规范写作。",
			IsBuiltin:    true,
			SystemPrompt: mustReadBuiltinPrompt(BuiltinPromptArticleWriter),
			Context:      ContextConfig{InjectMemory: true},
			CreatedAt:    now,
			Model:        "deepseek-v4-flash",
			Provider:     ProviderCustom,
			UpdatedAt:    now,
		}, {
			ID:           BuiltinSummaryProfileID,
			Name:         "摘要总结",
			Description:  "用于生成分析 / 节点的摘要：按学术规范写作。",
			IsBuiltin:    true,
			SystemPrompt: mustReadBuiltinPrompt(BuiltinPromptSummary),
			Context:      ContextConfig{InjectMemory: true},
			CreatedAt:    now,
			Model:        "deepseek-v4-flash",
			Provider:     ProviderCustom,
			UpdatedAt:    now,
		},
	}
}

// IsBuiltinProfileID 判断给定 ID 是否为内置 Profile。
// 内置 Profile 使用固定的负数 ID，用户自定义 Profile 使用雪花算法生成的正整数 ID。
func IsBuiltinProfileID(id int64) bool {
	return id < 0
}

// EnsureBuiltinProfiles 把内置 Profile 同步到仓库（数据库初始化工具）。
//
// 系统启动时调用：按 ID 判断，记录不存在则新增，已存在则更新为代码中的最新定义
// （内置 Profile 以代码为准，管理端对内置 Profile 的修改会在重启后被覆盖）。
// repo 为空时无操作。
func EnsureBuiltinProfiles(ctx context.Context, repo ProfileRepository) error {
	if repo == nil {
		return nil
	}
	for _, p := range BuiltinProfiles() {
		existing, err := repo.Get(ctx, p.ID)
		switch {
		case err == nil:
			// 已存在则更新：保留原 CreatedAt，其余字段以代码定义为准。
			if existing != nil {
				p.CreatedAt = existing.CreatedAt
			}
			if err := repo.Update(ctx, p); err != nil {
				return err
			}
		case errors.Is(err, ErrProfileNotFound):
			if err := repo.Create(ctx, p); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}

// ProfileManager 负责 Profile 的解析、列表与增删改查编排。
//
// 内置 Profile 与用户自定义 Profile 均持久化在数据库（ProfileRepository）中，本管理器
// 只做统一视图的编排：所有解析 / 读取均按 ID 进行（Name 仅用于展示）。内置 Profile 与
// 用户自定义 Profile 统一存储。
type ProfileManager struct {
	repo ProfileRepository
}

// NewProfileManager 创建 Profile 管理器。
func NewProfileManager(repo ProfileRepository) *ProfileManager {
	return &ProfileManager{repo: repo}
}

// Resolve 按 ID 解析 Profile。
//
//   - id 为 0 时回退到内置默认 Profile；
//   - id 不存在时返回 ErrProfileNotFound。
func (m *ProfileManager) Resolve(ctx context.Context, id int64) (*Profile, error) {
	if id == 0 {
		id = BuiltinDefaultProfileID
	}
	p, err := m.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrProfileNotFound
	}
	return p, nil
}

// List 返回全部 Profile（内置 + 自定义，按名称升序）。
func (m *ProfileManager) List(ctx context.Context) ([]*Profile, error) {
	out, err := m.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get 按 ID 返回 Profile（全局可见）。
func (m *ProfileManager) Get(ctx context.Context, id int64) (*Profile, error) {
	p, err := m.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrProfileNotFound
	}
	return p, nil
}

// Save 创建或更新 Profile；IsDefault 为 true 时清除其他 Profile 的默认标记。
//
// 内置 Profile（IsBuiltin=true、负数 ID）也允许更新：由管理端修改后持久化，
// 启动时 EnsureBuiltinProfiles 不再覆盖其改动。
func (m *ProfileManager) Save(ctx context.Context, p *Profile) error {
	if p == nil {
		return nil
	}
	// Name 仅用于展示，不做名称规整，只校验非空。
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return ErrProfileNameRequired
	}

	now := time.Now()
	if p.ID == 0 {
		p.ID = utils.GenerateID()
		p.IsBuiltin = false
		p.CreatedAt = now
		p.UpdatedAt = now
		if p.IsDefault {
			if err := m.repo.ClearDefault(ctx, p.ID); err != nil {
				return err
			}
		}
		return m.repo.Create(ctx, p)
	}

	p.UpdatedAt = now
	if p.IsDefault {
		if err := m.repo.ClearDefault(ctx, p.ID); err != nil {
			return err
		}
	}
	return m.repo.Update(ctx, p)
}

// Delete 删除自定义 Profile；内置 Profile 不可删除。
func (m *ProfileManager) Delete(ctx context.Context, id int64) error {
	// 内置 Profile（负数 ID）不可删除。
	if IsBuiltinProfileID(id) {
		return ErrProfileNotFound
	}
	return m.repo.Delete(ctx, id)
}
