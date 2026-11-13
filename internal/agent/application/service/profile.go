package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/skill"
	"github.com/biox-dev/gobrave/internal/agent/tool/builtin"
	"github.com/biox-dev/gobrave/internal/agent/tool/coder"
	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/utils"
)

// builtinToolNames 返回内置工具（框架 builtin + coder）的全部名称（升序）。
//
// 内置 Profile 用它显式声明工具白名单（而不是留空表示“全部”），使「该 Profile 能用
// 哪些工具」在代码里可直接审计；名单与 builtin.All / coder.All 同源，新增内置工具会
// 自动进入名单。外部 MCP 工具不在此列：MCP 工具由配置决定，需要时请在自定义 Profile
// 的 Tools 中显式添加。
func builtinToolNames() []string {
	names := append(builtin.Names(), coder.Names()...)
	sort.Strings(names)
	return names
}

// builtinPromptDir 是内置提示词目录的绝对路径（相对工作目录解析）。
var builtinPromptDir string

// SetBuiltinPromptDir 覆盖内置提示词目录（空串表示恢复默认：相对当前工作目录）。
func SetBuiltinPromptDir(dir string) {
	builtinPromptDir = strings.TrimSpace(dir)
}

// ReadBuiltinPrompt 按提示词名读取内置提示词文件。
func ReadBuiltinPrompt(name string) (string, error) {
	return utils.ReadBuiltinPrompt(builtinPromptDir, name)
}

// mustReadBuiltinPrompt 读取内置提示词，失败时降级为空提示词。
func mustReadBuiltinPrompt(name string) string {
	prompt, err := ReadBuiltinPrompt(name)
	if err != nil {
		logger.Warnf(context.Background(), "[Agent] Failed to read builtin prompt %q: %v", name, err)
		return ""
	}
	return prompt
}

// BuiltinProfiles 返回框架内置 Profile 的种子数据。新增内置 Profile 时在此追加。
func BuiltinProfiles() []*Profile {
	now := time.Now()
	return []*Profile{
		{
			ID:           BuiltinDefaultProfileID,
			Name:         BuiltinDefaultProfileName,
			Description:  "默认配置：注入全部内置技能（分析编码、科研报告撰写等）与记忆，由模型按当前任务自动选用对应技能。",
			IsDefault:    true,
			IsBuiltin:    true,
			SystemPrompt: mustReadBuiltinPrompt(BuiltinPromptDefault),
			// 分析编码 / 科研写作等具体规范全部由内置技能提供（正文按需通过 get_skill 加载），
			// 因此默认 Profile 注入全部内置技能的元信息。
			Skills: skill.BuiltinNames(),
			// 显式列出全部内置工具（builtin + coder），便于在代码里审计本 Profile 的能力面。
			Tools:     builtinToolNames(),
			Model:     "deepseek-v4-flash",
			Provider:  ProviderCustom,
			Context:   ContextConfig{InjectMemory: true},
			CreatedAt: now,
			UpdatedAt: now,
		},
		{
			ID:           BuiltinSummaryProfileID,
			Name:         "摘要总结",
			Description:  "用于生成项目 / 分析 / 分析节点的摘要与总结：按学术规范概括结果、指出问题并提出改进建议。",
			IsBuiltin:    true,
			SystemPrompt: mustReadBuiltinPrompt(BuiltinPromptSummary),
			// 与默认 Profile 一致：显式声明全部内置工具，后续可按摘要场景收窄。
			Tools:     builtinToolNames(),
			Context:   ContextConfig{InjectMemory: true},
			CreatedAt: now,
			Model:     "deepseek-v4-flash",
			Provider:  ProviderCustom,
			UpdatedAt: now,
		},
	}
}

// EnsureBuiltinProfiles 把内置 Profile 同步到仓库（数据库初始化工具）。
func EnsureBuiltinProfiles(ctx context.Context, repo ProfileRepository) error {
	if repo == nil {
		return nil
	}
	for _, p := range BuiltinProfiles() {
		existing, err := repo.Get(ctx, p.ID)
		switch {
		case err == nil:
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

	// 清理已删除的历史内置 Profile（旧库里可能仍有 -2 / -3 两条记录）。
	for _, id := range RemovedBuiltinProfileIDs {
		if err := repo.Delete(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// ProfileManager 负责 Profile 的解析、列表与增删改查编排。
type ProfileManager struct {
	repo ProfileRepository
}

// NewProfileManager 创建 Profile 管理器。
func NewProfileManager(repo ProfileRepository) *ProfileManager {
	return &ProfileManager{repo: repo}
}

// Resolve 按 ID 解析 Profile（id 为 0 时回退到内置默认 Profile）。
//
// 已删除的历史内置 Profile（-2 / -3）统一回退到默认 Profile，避免存量用户的
// agent_config.profile_id 指向已删除记录时任务直接失败。
func (m *ProfileManager) Resolve(ctx context.Context, id int64) (*Profile, error) {
	if id == 0 || IsRemovedBuiltinProfileID(id) {
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
func (m *ProfileManager) Save(ctx context.Context, p *Profile) error {
	if p == nil {
		return nil
	}
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
	if IsBuiltinProfileID(id) {
		return ErrProfileNotFound
	}
	return m.repo.Delete(ctx, id)
}
