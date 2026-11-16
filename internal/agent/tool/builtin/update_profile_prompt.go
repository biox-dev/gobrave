package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/tool/coder/diff"
)

// UpdateProfilePromptToolName 是「修改当前 Agent Profile 系统提示词」工具名。
const UpdateProfilePromptToolName = "update_profile_prompt"

// UpdateProfilePromptInput 是 update_profile_prompt 工具的入参。
//
// 目标 Profile 不在入参里：它由执行事实提供（tool.ProfileIDFrom），保证模型只能修改
// 当前正在运行的 Profile，不能越权改动别的 Profile。
type UpdateProfilePromptInput struct {
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

// UpdateProfilePromptOutput 是 update_profile_prompt 工具返回给模型的结果。
type UpdateProfilePromptOutput struct {
	ProfileID int64  `json:"profile_id,string,omitempty"`
	Name      string `json:"name,omitempty"`
	// Notice 是写入成功后需要转告用户的提醒（如内置 Profile 会被下次启动覆盖）。
	Notice string `json:"notice,omitempty"`
	Error  string `json:"error,omitempty"`
}

// UpdateProfilePrompt 返回「修改当前 Agent Profile 系统提示词」工具。
//
// 语义与 coder 的 edit 工具 / skill.EditSkill 一致：按 old_string → new_string 做一次
// 唯一文本替换。目标 Profile 取自执行事实（当前任务的 Profile ID），模型无需也不能指定。
// 写入会持久化到数据库并影响后续轮次的系统提示词，因此与 edit_skill 一致先请求写授权
// 并附带 unified diff。
func UpdateProfilePrompt(writer runtimecontext.ProfilePromptWriter) tool.Tool {
	return tool.NewFunc(
		UpdateProfilePromptToolName,
		"Edit the system prompt of the CURRENT agent profile (the profile this task is running with). Pass an old_string that appears exactly once in the current system prompt (include enough surrounding context) and the new_string to replace it with. Use this when the user asks you to change / tune your own instructions or behavior rules. The change is saved to the profile and affects subsequent turns; a diff is shown for approval before writing.",
		tool.Schema("修改当前 Profile 系统提示词的入参", map[string]any{
			"old_string": tool.StringProperty("要替换的原文，必须在当前系统提示词中唯一出现（含空白与换行需完全一致）。"),
			"new_string": tool.StringProperty("替换后的文本。"),
		}, "old_string", "new_string"),
		func(ctx context.Context, in UpdateProfilePromptInput) (UpdateProfilePromptOutput, error) {
			if writer == nil {
				return UpdateProfilePromptOutput{}, fmt.Errorf("%s: profile store is not available in this runtime", UpdateProfilePromptToolName)
			}

			// 目标 Profile 来自执行事实：0 表示当前使用内置默认 Profile。
			profileID := tool.ProfileIDFrom(ctx)

			// 先算计划：读取当前 SystemPrompt 并得到替换后的内容（不落库），供授权前展示 diff。
			plan, err := writer.PlanEdit(ctx, profileID, in.OldString, in.NewString)
			if err != nil {
				return UpdateProfilePromptOutput{ProfileID: profileID, Error: err.Error()}, nil
			}

			// 修改系统提示词会改变后续轮次的行为，与 edit_skill 一致：先请求写授权（附 diff）。
			if err := tool.RequirePermission(ctx, tool.PermissionRequest{
				ToolName:    UpdateProfilePromptToolName,
				Action:      "write",
				Description: fmt.Sprintf("Edit system prompt of profile %s", plan.Name),
				Params:      profilePromptDiffPreview(plan),
			}); err != nil {
				return UpdateProfilePromptOutput{}, err
			}

			info, err := writer.Apply(ctx, plan)
			if err != nil {
				return UpdateProfilePromptOutput{ProfileID: profileID, Error: err.Error()}, nil
			}
			return UpdateProfilePromptOutput{ProfileID: info.ProfileID, Name: info.Name, Notice: info.Notice}, nil
		},
	)
}

// profilePromptDiffPreview 把写入计划转为授权预览：file_path / diff 与 coder edit / write 的
// 授权参数同键（tool.PermissionPreview），上层会放进 Operation.Metadata 供前端渲染 diff。
func profilePromptDiffPreview(plan runtimecontext.ProfilePromptPlan) tool.PermissionPreview {
	text, _, _ := diff.GenerateDiff(plan.Before, plan.After, plan.FilePath)
	return tool.PermissionPreview{FilePath: plan.FilePath, Diff: text}
}
