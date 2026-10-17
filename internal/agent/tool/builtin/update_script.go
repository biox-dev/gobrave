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

// scriptIDVarKey 是运行时上下文 SystemPromptVars 中承载脚本主键 ID 的键。
//
// 供模型在脚本上下文里省略 script_id 时回退使用（与 run_analysis_node 回退
// analysis_node_id 的口径一致）；分析节点上下文下工具可通过其脚本反查。
const scriptIDVarKey = "script_id"

// UpdateScriptInput 是 update_script 工具的入参。
//
// 只开放 name / description 两个元数据字段：脚本内容由模型直接写文件，io_schema 与
// 容器模板有各自的维护入口，不在本工具职责内。
//
// ScriptID 允许为空：此时回退到运行时上下文的 script_id 变量。
type UpdateScriptInput struct {
	ScriptID    string `json:"script_id"`   // 脚本主键 ID（字符串以保精度），可选
	Name        string `json:"name"`        // 新脚本名称，留空表示不修改
	Description string `json:"description"` // 新脚本描述，留空表示不修改
}

// UpdateScriptOutput 是 update_script 工具返回给模型的结果。
//
// 除更新后的元数据外，回传脚本主文件与 io_schema.json 路径，便于模型继续读写脚本内容。
type UpdateScriptOutput struct {
	ScriptID       int64  `json:"script_id"`
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
//   - 未传 script_id 时，回退到运行时上下文的 script_id（脚本上下文注入）；
//   - 只覆盖本次给出的字段，未给出的字段保持数据库原值；
//   - 项目归属取自 RuntimeContext.ProjectID，脚本必须属于当前激活项目，否则报错；
//   - 回传更新后的名称 / 描述与脚本主文件、io_schema.json 路径。
//
// 该工具会修改业务数据（写库），因此声明 write 授权：上层在执行前会先走权限门禁。
func UpdateScript() tool.Tool {
	return tool.NewFunc(
		UpdateScriptToolName,
		"更新已有分析脚本的基本信息（name / description）。只改元数据，不修改脚本内容（脚本内容请直接写脚本主文件）、io_schema 与容器模板；未给出的字段保持原值。脚本默认取当前会话上下文（script / analysisNode 上下文）中的脚本，也可显式传 script_id。返回更新后的名称、描述与脚本主文件路径。",
		tool.Schema("更新脚本基本信息的入参", map[string]any{
			"script_id":   tool.StringProperty("脚本主键 ID（整数，字符串以保精度）。留空时使用当前上下文的脚本。"),
			"name":        tool.StringProperty("新的脚本名称（同一项目下应唯一，由业务侧校验）。留空表示不修改名称。"),
			"description": tool.StringProperty("新的脚本描述。留空表示不修改描述（如需清空描述请明确说明，本工具不会把空串当作清空）。"),
		}),
		func(ctx context.Context, in UpdateScriptInput) (UpdateScriptOutput, error) {
			rc := tool.RuntimeContextFrom(ctx)
			if rc == nil || rc.ProjectID <= 0 {
				return UpdateScriptOutput{}, fmt.Errorf("update_script: runtime context with project_id is required (no active project in this conversation)")
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

			scriptID, err := resolveScriptRef(rc, in)
			if err != nil {
				return UpdateScriptOutput{}, err
			}

			updateInput := tool.ScriptUpdateInput{
				ProjectID: rc.ProjectID,
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

// resolveScriptRef 解析要更新的脚本引用：优先工具入参，其次运行时上下文的 script_id
// 变量（脚本上下文注入）。都解析不到时返回描述性错误，提示模型显式传入。
func resolveScriptRef(rc *tool.RuntimeContext, in UpdateScriptInput) (int64, error) {
	if idStr := strings.TrimSpace(in.ScriptID); idStr != "" {
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("update_script: invalid script_id %q: %w", idStr, err)
		}
		if id <= 0 {
			return 0, fmt.Errorf("update_script: script_id must be a positive integer")
		}
		return id, nil
	}
	if idStr := rc.SystemPromptVar(scriptIDVarKey); idStr != "" {
		if id, err := strconv.ParseInt(idStr, 10, 64); err == nil && id > 0 {
			return id, nil
		}
	}
	return 0, fmt.Errorf("update_script: script_id is required (pass it explicitly; the current context does not identify a script)")
}
