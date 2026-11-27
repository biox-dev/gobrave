package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// UpdateSessionTitleToolName 是「更新当前会话标题」工具名。
const UpdateSessionTitleToolName = "update_session_title"

// sessionTitleMaxRunes 限制标题长度，与 agent_sessions.title 的 varchar(255) 对齐。
const sessionTitleMaxRunes = 255

// UpdateSessionTitleInput 是 update_session_title 工具的入参。
//
// 目标会话不在入参里：它由执行事实提供（tool.SessionIDFrom），保证模型只能修改
// 当前正在运行的会话，不能越权改动别的会话。
type UpdateSessionTitleInput struct {
	Title string `json:"title"`
}

// UpdateSessionTitleOutput 是 update_session_title 工具返回给模型的结果。
type UpdateSessionTitleOutput struct {
	SessionID int64  `json:"session_id,string"`
	Title     string `json:"title,omitempty"`
	Message   string `json:"message,omitempty"`
	Error     string `json:"error,omitempty"`
}

// UpdateSessionTitle 返回「更新当前会话标题」工具。
//
// 行为：
//   - 目标会话取自执行事实（当前任务的 SessionID），模型不提供 session_id；
//   - 标题会先清洗（压缩空白、去掉包裹引号 / 标点）并截断到 255 个字符；
//   - 写入只更新 title / updated_at 两列，成功后推送 sessionTitleUpdated 事件刷新会话列表；
//   - 会话归属由实现侧按当前用户校验。
//
// 该工具会修改业务数据（写库），因此声明 write 授权。
func UpdateSessionTitle(updater runtimecontext.SessionTitleUpdater) tool.Tool {
	return tool.NewFunc(
		UpdateSessionTitleToolName,
		"Rename the CURRENT chat session (the conversation this task is running in). Pass only the new title; the target session comes from execution facts, so other sessions cannot be renamed. Use this when the user asks to rename / retitle the current conversation. The title is saved to the database and the open UI is refreshed automatically; a write permission is requested first.",
		tool.Schema("更新当前会话标题的入参", map[string]any{
			"title": tool.StringProperty("新的会话标题，简洁具体（建议不超过 20 个汉字，或其它语言约 40 个字符），不要引号、编号或句末标点。"),
		}, "title"),
		func(ctx context.Context, in UpdateSessionTitleInput) (UpdateSessionTitleOutput, error) {
			sessionID := tool.SessionIDFrom(ctx)
			if sessionID <= 0 {
				return UpdateSessionTitleOutput{}, fmt.Errorf("%s: session_id is required in this runtime", UpdateSessionTitleToolName)
			}

			title := normalizeSessionTitleInput(in.Title)
			if title == "" {
				return UpdateSessionTitleOutput{}, fmt.Errorf("%s: title is required", UpdateSessionTitleToolName)
			}
			if updater == nil {
				return UpdateSessionTitleOutput{}, fmt.Errorf("%s: session title updater is not available in this runtime", UpdateSessionTitleToolName)
			}

			result, err := updater.UpdateSessionTitle(ctx, sessionID, tool.UserIDFrom(ctx), title)
			if err != nil {
				return UpdateSessionTitleOutput{SessionID: sessionID, Error: err.Error()}, nil
			}
			return UpdateSessionTitleOutput{
				SessionID: result.SessionID,
				Title:     result.Title,
				Message:   "session title updated",
			}, nil
		},
	).WithPermission("write", "", "更新当前会话标题")
}

// normalizeSessionTitleInput 清洗模型给出的标题：
// 压缩所有空白（含换行）、反复去掉包裹符号与句末标点，并截断到列宽上限。
func normalizeSessionTitleInput(raw string) string {
	title := strings.Join(strings.Fields(raw), " ")
	for range 3 {
		trimmed := strings.Trim(title, "\"'`“”‘’「」【】*_# ")
		trimmed = strings.TrimRight(trimmed, "。.!！?？;；,，:：")
		if trimmed == title {
			break
		}
		title = trimmed
	}
	if runes := []rune(title); len(runes) > sessionTitleMaxRunes {
		title = string(runes[:sessionTitleMaxRunes])
	}
	return title
}
