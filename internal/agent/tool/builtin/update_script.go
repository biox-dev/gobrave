package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// UpdateScriptToolName 是「更新脚本基本信息」工具名。
const UpdateScriptToolName = "update_script"

// UpdateScriptInput 是 update_script 工具的入参。
//
// 只开放 name / description 两个元数据字段：脚本内容由模型直接写文件，io_schema 与
// 容器模板有各自的维护入口，不在本工具职责内。
//
// ScriptID 为必填：模型必须显式给出要更新的脚本 ID，不再从运行时上下文回退。
type UpdateScriptInput struct {
	ScriptID    string `json:"script_id"`   // 脚本主键 ID（必填，字符串以保精度）
	Name        string `json:"name"`        // 新脚本名称，留空表示不修改
	Description string `json:"description"` // 新脚本描述，留空表示不修改
}

// UpdateScriptOutput 是 update_script 工具返回给模型的结果。
//
// 除更新后的元数据外，回传脚本主文件与 io_schema.json 路径，便于模型继续读写脚本内容。
type UpdateScriptOutput struct {
	ScriptID       int64  `json:"script_id,string"`
	ScriptName     string `json:"script_name,omitempty"`
	Description    string `json:"description,omitempty"`
	ScriptType     string `json:"script_type,omitempty"`
	ScriptFilePath string `json:"script_file_path,omitempty"`
	IOSchemaPath   string `json:"io_schema_path,omitempty"`
	// Changed 列出本次实际被修改的字段（name / description）。
	Changed []string `json:"changed,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// UpdateScript 返回一个「更新脚本 name / description」工具。
//
// 行为：
//   - script_id 为必填，缺省直接报错；
//   - 只覆盖本次给出的字段，未给出的字段保持数据库原值；
//   - 项目归属取自执行事实（tool.ProjectIDFrom），脚本必须属于当前激活项目，否则报错；
//   - 回传更新后的名称 / 描述与脚本主文件、io_schema.json 路径。
//
// 该工具会修改业务数据（写库），因此声明 write 授权：上层在执行前会先走权限门禁。
func UpdateScript() tool.Tool {
	return tool.NewFunc(
		UpdateScriptToolName,
		"更新已有分析脚本的基本信息（name / description）。script_id 必填。只改元数据，不修改脚本内容（脚本内容请直接写脚本主文件）、io_schema 与容器模板；未给出的字段保持原值。返回更新后的名称、描述与脚本主文件路径。",
		tool.Schema("更新脚本基本信息的入参", map[string]any{
			"script_id":   tool.StringProperty("脚本主键 ID（必填，整数，字符串以保精度）。"),
			"name":        tool.StringProperty("新的脚本名称（同一项目下应唯一，由业务侧校验）。留空表示不修改名称。"),
			"description": tool.StringProperty("新的脚本描述。留空表示不修改描述（如需清空描述请明确说明，本工具不会把空串当作清空）。"),
		}, "script_id"),
		func(ctx context.Context, in UpdateScriptInput) (UpdateScriptOutput, error) {
			projectID := tool.ProjectIDFrom(ctx)
			if projectID <= 0 {
				return UpdateScriptOutput{}, fmt.Errorf("update_script: project_id is required (no active project in this conversation)")
			}

			name := strings.TrimSpace(in.Name)
			description := strings.TrimSpace(in.Description)
			if name == "" && description == "" {
				return UpdateScriptOutput{}, fmt.Errorf("update_script: at least one of name / description must be provided")
			}

			updater := tool.ScriptUpdaterFrom(ctx)
			if updater == nil {
				return UpdateScriptOutput{}, fmt.Errorf("update_script: script updater is not available in this runtime")
			}

			scriptID, err := parseScriptID(in.ScriptID)
			if err != nil {
				return UpdateScriptOutput{}, fmt.Errorf("update_script: %w", err)
			}

			updateInput := tool.ScriptUpdateInput{
				ProjectID: projectID,
				ScriptID:  scriptID,
			}
			if name != "" {
				updateInput.Name = &name
			}
			if description != "" {
				updateInput.Description = &description
			}

			result, err := updater.UpdateScriptInfo(ctx, updateInput)
			if err != nil {
				return UpdateScriptOutput{Error: err.Error()}, nil
			}

			return UpdateScriptOutput{
				ScriptID:       result.ScriptID,
				ScriptName:     result.ScriptName,
				Description:    result.Description,
				ScriptType:     result.ScriptType,
				ScriptFilePath: result.ScriptFilePath,
				IOSchemaPath:   result.IOSchemaPath,
				Changed:        result.Changed,
			}, nil
		},
	).WithPermission("write", "", "更新脚本名称 / 描述（写库）")
}

// parseScriptID 解析必填的脚本 ID：为空或非法时返回错误。
func parseScriptID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idStr == "" {
		return 0, fmt.Errorf("script_id is required")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid script_id %q: %w", idStr, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("script_id must be a positive integer")
	}
	return id, nil
}
