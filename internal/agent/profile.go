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
)

// 内置 Profile 使用固定的负数 ID，避免与雪花算法生成的正整数主键冲突。
const (
	BuiltinDefaultProfileID int64 = -1 // 内置默认 Profile
	BuiltinAnalysisCoderID  int64 = -2 // 内置「分析代码编写」Profile
	BuiltinArticleWriterID  int64 = -3 // 内置「科研文章撰写」Profile
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
	ID           int64         `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	Name         string        `json:"name" gorm:"column:name;type:varchar(64);index:idx_agent_profiles_user_name,priority:2"`
	DisplayName  string        `json:"display_name" gorm:"column:display_name;type:varchar(128)"`
	Description  string        `json:"description" gorm:"column:description;type:text"`
	UserID       string        `json:"user_id" gorm:"column:user_id;type:varchar(64);index:idx_agent_profiles_user_name,priority:1"`
	IsDefault    bool          `json:"is_default" gorm:"column:is_default"`
	IsBuiltin    bool          `json:"is_builtin" gorm:"column:is_builtin"`
	SystemPrompt string        `json:"system_prompt" gorm:"column:system_prompt;type:text"`
	Skills       []string      `json:"skills" gorm:"column:skills;serializer:json"`
	Context      ContextConfig `json:"context" gorm:"column:context;serializer:json"`

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

// BuiltinProfiles 返回框架内置的 Profile。新增内置 Profile 时在此追加。
//
// 内置 Profile 由代码定义（不落库、不可删除），与用户自定义 Profile（落库）合并后
// 共同构成可选的 Profile 列表。
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
			UpdatedAt:    now,
		},
	}
}

// DefaultBuiltinProfile 返回内置默认 Profile（用于兜底：未配置 ProfileManager 或解析失败时）。
func DefaultBuiltinProfile() *Profile {
	for _, p := range BuiltinProfiles() {
		if p.Name == DefaultProfileName {
			return p
		}
	}
	return &Profile{Name: DefaultProfileName, Context: ContextConfig{InjectMemory: true}}
}

// normalizeProfileName 规整 Profile 名称：小写、去首尾空白、空格转下划线。
func normalizeProfileName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, " ", "_")
	return name
}

// ProfileManager 负责 Profile 的解析、列表与增删改查编排。
//
// 它把「内置 Profile（代码定义）」与「用户自定义 Profile（持久化）」合并成一个统一的
// 视图：按名称解析时用户自定义优先，其次内置；未指定名称时回退到默认 Profile。
type ProfileManager struct {
	repo     ProfileRepository
	builtins map[string]*Profile
}

// NewProfileManager 创建 Profile 管理器；repo 为空时使用内存实现。
func NewProfileManager(repo ProfileRepository) *ProfileManager {
	if repo == nil {
		repo = NewMemoryProfileRepository()
	}
	m := &ProfileManager{repo: repo, builtins: make(map[string]*Profile)}
	for _, p := range BuiltinProfiles() {
		m.builtins[p.Name] = p
	}
	return m
}

// Resolve 解析 Profile：
//
//   - name 为空：取默认（用户自定义默认优先，其次内置默认）；
//   - name 非空：用户自定义优先，其次内置；
//   - 均未命中：返回 ErrProfileNotFound。
func (m *ProfileManager) Resolve(ctx context.Context, userID, name string) (*Profile, error) {
	name = normalizeProfileName(name)
	uid := strings.TrimSpace(userID)

	if name == "" {
		if uid != "" {
			if p, err := m.repo.GetDefault(ctx, uid); err == nil && p != nil {
				return p, nil
			}
		}
		return DefaultBuiltinProfile(), nil
	}

	if uid != "" {
		if p, err := m.repo.GetByName(ctx, uid, name); err == nil && p != nil {
			return p, nil
		}
	}
	if p, ok := m.builtins[name]; ok {
		return p, nil
	}
	return nil, ErrProfileNotFound
}

// List 返回内置 Profile 与指定用户自定义 Profile 的合并列表（按名称升序）。
func (m *ProfileManager) List(ctx context.Context, userID string) ([]*Profile, error) {
	builtins := BuiltinProfiles()
	sort.Slice(builtins, func(i, j int) bool { return builtins[i].Name < builtins[j].Name })
	out := append([]*Profile(nil), builtins...)

	if uid := strings.TrimSpace(userID); uid != "" {
		userProfiles, err := m.repo.ListByUser(ctx, uid)
		if err != nil {
			return nil, err
		}
		sort.Slice(userProfiles, func(i, j int) bool { return userProfiles[i].Name < userProfiles[j].Name })
		out = append(out, userProfiles...)
	}
	return out, nil
}

// Get 按 ID 返回某用户的自定义 Profile（内置 Profile 不按 ID 取，列表已含其完整信息）。
func (m *ProfileManager) Get(ctx context.Context, userID string, id int64) (*Profile, error) {
	p, err := m.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if p.IsBuiltin || strings.TrimSpace(p.UserID) != strings.TrimSpace(userID) {
		return nil, ErrProfileNotFound
	}
	return p, nil
}

// Save 创建或更新用户自定义 Profile；IsDefault 为 true 时清除该用户的其他默认标记。
func (m *ProfileManager) Save(ctx context.Context, p *Profile) error {
	if p == nil {
		return nil
	}
	name := normalizeProfileName(p.Name)
	if name == "" {
		return ErrProfileNameRequired
	}
	p.Name = name
	p.IsBuiltin = false

	now := time.Now()
	if p.ID == 0 {
		p.ID = utils.GenerateID()
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
	if p.IsDefault {
		if err := m.repo.ClearDefault(ctx, p.UserID, p.ID); err != nil {
			return err
		}
	}
	return m.repo.Update(ctx, p)
}

// Delete 删除某用户的自定义 Profile；内置 Profile 不可删除。
func (m *ProfileManager) Delete(ctx context.Context, userID string, id int64) error {
	p, err := m.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	if p.IsBuiltin || strings.TrimSpace(p.UserID) != strings.TrimSpace(userID) {
		return ErrProfileNotFound
	}
	return m.repo.Delete(ctx, id)
}
