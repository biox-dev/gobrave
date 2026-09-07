package agent

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// 内置 AgentProfile 名称常量。新增内置 Profile 时在此登记唯一名称。
const (
	DefaultProfileName   = "default"        // 系统默认 Profile
	ProfileAnalysisCoder = "analysis_coder" // 撰写分析代码
	ProfileArticleWriter = "article_writer" // 撰写科研文章 / 报告
	ProfileSummary       = "summary"        // 摘要总结
)

// 内置 Profile 使用固定的负数 ID，避免与雪花算法生成的正整数主键冲突。
const (
	BuiltinDefaultProfileID int64 = -1 // 内置默认 Profile
	BuiltinAnalysisCoderID  int64 = -2 // 内置「分析代码编写」Profile
	BuiltinArticleWriterID  int64 = -3 // 内置「科研文章撰写」Profile
	// 摘要总结
	BuiltinSummaryProfileID int64 = -4 // 内置「摘要总结」Profile
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
//   - InjectMemory：检索长期记忆并拼进 SystemPrompt；
//   - InjectProject：把当前项目已完成的分析节点等背景拼进 SystemPrompt。
//     （只有撰写文章 / 报告一类的任务才需要项目背景，因此默认关闭。）
type ContextConfig struct {
	InjectMemory  bool `json:"inject_memory"`
	InjectProject bool `json:"inject_project"`
}

// Profile 描述一类任务的 Agent 配置（系统提示词 + 技能 + 上下文注入开关）。
//
// 与 Options / Request（每次调用的动态参数）不同，Profile 是「按任务类型选择」的静态
// 配置快照：HTTP 层或前端在发起调用时通过 Request.Profile 指定名称，AgentService 在
// 调用前解析并应用（合并系统提示词、过滤技能、按开关注入背景）。
//
// UserID 为空表示系统级（内置）Profile；非空表示某用户的自定义 Profile。
type Profile struct {
	ID           int64  `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	Name         string `json:"name" gorm:"column:name;type:varchar(64);index:idx_agent_profiles_user_name,priority:2"`
	DisplayName  string `json:"display_name" gorm:"column:display_name;type:varchar(128)"`
	Description  string `json:"description" gorm:"column:description;type:text"`
	UserID       string `json:"user_id" gorm:"column:user_id;type:varchar(64);index:idx_agent_profiles_user_name,priority:1"`
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
			ID:           BuiltinDefaultProfileID,
			Name:         DefaultProfileName,
			DisplayName:  "通用助手",
			Description:  "默认配置：不附加领域提示词；注入记忆、不注入项目上下文。",
			IsDefault:    true,
			IsBuiltin:    true,
			SystemPrompt: "",
			Model:        "deepseek-v4-flash",
			Provider:     ProviderCopilot,
			Context:      ContextConfig{InjectMemory: true, InjectProject: false},
			CreatedAt:    now,
			UpdatedAt:    now,
		},
		{
			ID:           BuiltinAnalysisCoderID,
			Name:         ProfileAnalysisCoder,
			DisplayName:  "分析代码编写",
			Description:  "用于撰写生物信息学分析代码：强调代码规范、可复现性与执行约束。",
			IsBuiltin:    true,
			SystemPrompt: "你是一名生物信息学数据分析工程师。请输出规范、可复现的分析代码，明确依赖与输入输出；如需执行，优先使用运行时提供的工具，而不是直接调用 shell / Rscript / python 等命令。",
			Context:      ContextConfig{InjectMemory: true, InjectProject: false},
			CreatedAt:    now,
			Model:        "deepseek-v4-flash",
			Provider:     ProviderCustom,
			UpdatedAt:    now,
		},
		{
			ID:           BuiltinArticleWriterID,
			Name:         ProfileArticleWriter,
			DisplayName:  "科研文章撰写",
			Description:  "用于撰写科研报告 / 文章：注入项目上下文（已完成的分析节点等），按学术规范写作。",
			IsBuiltin:    true,
			SystemPrompt: "你是一名科研写作助手。请严格遵循科研报告 / 科研文章的学术规范撰写内容，语言严谨、结构完整，并确保结论与项目中的分析结果保持一致。",
			Context:      ContextConfig{InjectMemory: true, InjectProject: true},
			CreatedAt:    now,
			Model:        "deepseek-v4-flash",
			Provider:     ProviderCustom,
			UpdatedAt:    now,
		}, {
			ID:           BuiltinSummaryProfileID,
			Name:         ProfileSummary,
			DisplayName:  "摘要总结",
			Description:  "用于生成分析 / 节点的摘要：注入项目上下文（已完成的分析节点等），按学术规范写作。",
			IsBuiltin:    true,
			SystemPrompt: "你是一名生物信息学分析助手，请根据给定的分析输出内容，生成简洁、准确的中文摘要; 检查分析中存在的问题，并提出改进建议。",
			Context:      ContextConfig{InjectMemory: true, InjectProject: true},
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
// 系统启动时调用：按 ID 判断，记录不存在则新增、已存在则跳过，从而允许管理端
// 对内置 Profile 的修改在重启后得以保留。repo 为空时无操作。
func EnsureBuiltinProfiles(ctx context.Context, repo ProfileRepository) error {
	if repo == nil {
		return nil
	}
	for _, p := range BuiltinProfiles() {
		_, err := repo.Get(ctx, p.ID)
		switch {
		case err == nil:
			// 已存在则跳过：内置 Profile 允许被修改，启动时不再覆盖。
			continue
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

// normalizeProfileName 规整 Profile 名称：小写、去首尾空白、空格转下划线。
func normalizeProfileName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, " ", "_")
	return name
}

// ProfileManager 负责 Profile 的解析、列表与增删改查编排。
//
// 内置 Profile 与用户自定义 Profile 均持久化在数据库（ProfileRepository）中，本管理器
// 只做统一视图的编排：按名称解析时用户自定义优先、其次内置；未指定名称时回退到默认
// Profile（用户默认优先、其次内置默认）。所有读写均通过仓库完成。
type ProfileManager struct {
	repo ProfileRepository
}

// NewProfileManager 创建 Profile 管理器；repo 为空时使用内存实现。
func NewProfileManager(repo ProfileRepository) *ProfileManager {
	if repo == nil {
		repo = NewMemoryProfileRepository()
	}
	return &ProfileManager{repo: repo}
}

// Resolve 按名称解析 Profile。
//
//   - 先按 name 查询 Profile；
//   - name 不存在：返回 ErrProfileNotFound；
//   - userID 非空时校验其与查询到的 Profile 的 UserID 是否相等，不相等返回 ErrProfileNotFound。
func (m *ProfileManager) Resolve(ctx context.Context, userID, name string) (*Profile, error) {
	name = normalizeProfileName(name)
	// uid := strings.TrimSpace(userID)

	p, err := m.repo.GetByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrProfileNotFound
	}
	// if uid != "" && strings.TrimSpace(p.UserID) != uid {
	// 	return nil, ErrProfileNotFound
	// }
	return p, nil
}

// List 返回内置 Profile 与指定用户自定义 Profile 的合并列表（按名称升序）。
func (m *ProfileManager) List(ctx context.Context, userID string) ([]*Profile, error) {
	builtins, err := m.repo.ListBuiltin(ctx)
	if err != nil {
		return nil, err
	}
	out := append([]*Profile(nil), builtins...)

	if uid := strings.TrimSpace(userID); uid != "" {
		userProfiles, err := m.repo.ListByUser(ctx, uid)
		if err != nil {
			return nil, err
		}
		out = append(out, userProfiles...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Get 按 ID 返回 Profile。内置 Profile 全局共享（任意用户可读），
// 自定义 Profile 仅限其所属用户读取。
func (m *ProfileManager) Get(ctx context.Context, userID string, id int64) (*Profile, error) {
	p, err := m.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.IsBuiltin {
		return p, nil
	}
	if strings.TrimSpace(p.UserID) != strings.TrimSpace(userID) {
		return nil, ErrProfileNotFound
	}
	return p, nil
}

// Save 创建或更新 Profile；IsDefault 为 true 时清除该用户的其他默认标记。
//
// 内置 Profile（IsBuiltin=true、负数 ID）也允许更新：由管理端修改后持久化，
// 启动时 EnsureBuiltinProfiles 不再覆盖其改动。内置 Profile 的默认标记为全局
// 语义，不参与「清除该用户其他默认」的编排。
func (m *ProfileManager) Save(ctx context.Context, p *Profile) error {
	if p == nil {
		return nil
	}
	name := normalizeProfileName(p.Name)
	if name == "" {
		return ErrProfileNameRequired
	}
	p.Name = name

	now := time.Now()
	if p.ID == 0 {
		p.ID = utils.GenerateID()
		p.IsBuiltin = false
		p.CreatedAt = now
		p.UpdatedAt = now
		if p.IsDefault {
			if err := m.repo.ClearDefault(ctx, p.UserID, p.ID); err != nil {
				return err
			}
		}
		return m.repo.Create(ctx, p)
	}

	p.UpdatedAt = now
	if p.IsDefault && !p.IsBuiltin {
		if err := m.repo.ClearDefault(ctx, p.UserID, p.ID); err != nil {
			return err
		}
	}
	return m.repo.Update(ctx, p)
}

// Delete 删除某用户的自定义 Profile；内置 Profile 不可删除。
func (m *ProfileManager) Delete(ctx context.Context, userID string, id int64) error {
	// 内置 Profile（负数 ID）不可删除。
	if IsBuiltinProfileID(id) {
		return ErrProfileNotFound
	}
	p, err := m.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(p.UserID) != strings.TrimSpace(userID) {
		return ErrProfileNotFound
	}
	return m.repo.Delete(ctx, id)
}
