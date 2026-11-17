package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	. "github.com/biox-dev/gobrave/internal/agent/types"
)

// ProfilePromptWriter 实现 runtimecontext.ProfilePromptWriter：按 old_string → new_string
// 修改「当前 Profile」的 SystemPrompt，供 update_profile_prompt 工具使用。
//
// 与技能写入同构：Plan* 只读取并计算写入计划（不落盘），供工具在授权前展示 unified diff；
// Apply 在授权通过后写库。
//
// 内置 Profile 的系统提示词来源是代码内置的 builtin/prompts/<name>.md（如 default.md），
// 启动时由 EnsureBuiltinProfiles 同步进数据库。是否覆盖已存在的内置 Profile 由
// config.agent.overwrite_builtin_profile 决定，因此这里会在修改内置 Profile 时，
// 用 Notice 提醒用户「重启后可能被内置提示词覆盖」。
type ProfilePromptWriter struct {
	repo *ProfileManager
	// overwriteBuiltinProfile 与 config.agent.overwrite_builtin_profile 保持一致。
	overwriteBuiltinProfile bool
}

// NewProfilePromptWriter 创建 Profile 提示词写入器。
//
// overwriteBuiltinProfile 为 true（配置默认值）时，对内置 Profile 的修改会在系统重启后被
// 内置提示词覆盖，写入计划会附带提醒。
func NewProfilePromptWriter(repo *ProfileManager, overwriteBuiltinProfile bool) *ProfilePromptWriter {
	return &ProfilePromptWriter{repo: repo, overwriteBuiltinProfile: overwriteBuiltinProfile}
}

// PlanEdit 实现 runtimecontext.ProfilePromptWriter：按 ID 计算唯一文本替换的写入计划（不落盘）。
//
// 语义与 coder 的 edit 工具一致：old_string 必须在 SystemPrompt 中唯一出现，
// 且替换结果必须真实改变内容。
func (w *ProfilePromptWriter) PlanEdit(ctx context.Context, profileID int64, oldString, newString string) (runtimecontext.ProfilePromptPlan, error) {
	if w == nil || w.repo == nil {
		return runtimecontext.ProfilePromptPlan{}, errors.New("profile store is not configured")
	}
	if oldString == "" {
		return runtimecontext.ProfilePromptPlan{}, errors.New("old_string is required")
	}

	profile, err := w.repo.Resolve(ctx, profileID)
	if err != nil {
		return runtimecontext.ProfilePromptPlan{}, fmt.Errorf("failed to resolve profile: %w", err)
	}

	before := profile.SystemPrompt
	index := strings.Index(before, oldString)
	if index == -1 {
		return runtimecontext.ProfilePromptPlan{}, errors.New("old_string not found in system prompt. Make sure it matches exactly, including whitespace and line breaks")
	}
	if last := strings.LastIndex(before, oldString); last != index {
		return runtimecontext.ProfilePromptPlan{}, errors.New("old_string appears multiple times in the system prompt. Please provide more context to ensure a unique match")
	}

	after := before[:index] + newString + before[index+len(oldString):]
	if after == before {
		return runtimecontext.ProfilePromptPlan{}, errors.New("new content is the same as old content. No changes made")
	}

	return runtimecontext.ProfilePromptPlan{
		ProfileID: profile.ID,
		Name:      profile.Name,
		// FilePath 仅用于前端 diff 标题展示：真实存储是 agent_profiles.system_prompt。
		FilePath: profilePromptPath(profile),
		Before:   before,
		After:    after,
		Notice:   w.overwriteNotice(profile),
	}, nil
}

// Apply 实现 runtimecontext.ProfilePromptWriter：把计划写回 Profile.SystemPrompt。
//
// 写库前重新读取一次，避免覆盖并发改动（预览后提示词若被改动则拒绝）。
func (w *ProfilePromptWriter) Apply(ctx context.Context, plan runtimecontext.ProfilePromptPlan) (runtimecontext.ProfilePromptInfo, error) {
	if w == nil || w.repo == nil {
		return runtimecontext.ProfilePromptInfo{}, errors.New("profile store is not configured")
	}

	profile, err := w.repo.Get(ctx, plan.ProfileID)
	if err != nil {
		return runtimecontext.ProfilePromptInfo{}, fmt.Errorf("failed to get profile: %w", err)
	}
	if profile.SystemPrompt != plan.Before {
		return runtimecontext.ProfilePromptInfo{}, errors.New("system prompt changed since the diff was prepared; please retry")
	}

	profile.SystemPrompt = plan.After
	// 走 ProfileManager.Save：它会刷新 UpdatedAt，并处理 IsDefault 等字段语义。
	if err := w.repo.Save(ctx, profile); err != nil {
		return runtimecontext.ProfilePromptInfo{}, fmt.Errorf("failed to save profile: %w", err)
	}

	return runtimecontext.ProfilePromptInfo{
		ProfileID: profile.ID,
		Name:      profile.Name,
		Notice:    plan.Notice,
	}, nil
}

// overwriteNotice 返回本次写入需要提醒用户的事项；无需提醒时返回空串。
//
// 内置 Profile 在启动时会由 EnsureBuiltinProfiles 重新同步；
// overwrite_builtin_profile=true（默认）时该同步会覆盖本次修改，因此提前提醒用户。
func (w *ProfilePromptWriter) overwriteNotice(profile *Profile) string {
	if w == nil || !w.overwriteBuiltinProfile || !IsBuiltinProfileID(profile.ID) {
		return ""
	}
	return fmt.Sprintf("profile %q is a builtin profile: this change will be overwritten by the builtin definition when the system restarts. To keep it, set agent.overwrite_builtin_profile: false in config.yml (default is true), or edit the prompt file directly.", profile.Name)
}

// profilePromptPath 返回 Profile 提示词在 diff 预览中展示的路径。
//
// 提示词真实存储在 agent_profiles.system_prompt 列，这里给出一个稳定的可视化路径，
// 供前端在授权前展示 unified diff 的标题。
func profilePromptPath(profile *Profile) string {
	return fmt.Sprintf("agent_profiles/%d/system_prompt.md", profile.ID)
}
