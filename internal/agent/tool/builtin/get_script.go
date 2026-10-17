package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// GetScriptToolName 是「按 ID 查看脚本」工具名。
const GetScriptToolName = "get_script"

// GetScriptInput 是 get_script 工具的入参。
//
// ScriptID 为必填：模型必须显式给出要查看的脚本 ID，不再从运行时上下文回退。
// 项目归属取自 RuntimeContext.ProjectID，脚本必须属于当前激活项目。
type GetScriptInput struct {
	ScriptID string `json:"script_id"` // 脚本主键 ID（必填，字符串以保精度）
}

// GetScriptOutput 是 get_script 工具返回给模型的结果。
//
// ScriptFilePath / IOSchemaPath 由 utils.GetScriptFile / utils.WorkflowIOSchemaPath 推导，
// 模型可直接按这两个路径读写脚本主文件与 io_schema.json。
type GetScriptOutput struct {
	ScriptID       int64  `json:"script_id"`
	ScriptName     string `json:"script_name"`
	Description    string `json:"description,omitempty"`
	ScriptType     string `json:"script_type,omitempty"`
	ScriptFilePath string `json:"script_file_path"`
	IOSchemaPath   string `json:"io_schema_path"`
	Error          string `json:"error,omitempty"`
}

// GetScript 返回一个「按 ID 查看脚本」工具。
//
// 行为：
//   - 解析必填入参 script_id；
//   - 项目归属取自 RuntimeContext.ProjectID，脚本必须属于当前激活项目，否则报错；
//   - 回传脚本名称、描述、主文件路径与 io_schema.json 路径。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func GetScript() tool.Tool {
	return tool.NewFunc(
		GetScriptToolName,
		"按 script_id 查看单个分析脚本的基本信息。返回 name（脚本名称）、description（描述）、script_file_path（脚本主文件路径，可直接读写以修改脚本内容）与 io_schema_path（脚本输入输出契约 io_schema.json 的路径）。不确定 script_id 时先用 list_project_scripts 列出当前项目脚本。",
		tool.Schema("按 ID 查看脚本的入参", map[string]any{
			"script_id": tool.StringProperty("脚本主键 ID（必填，整数，字符串以保精度）。可来自 list_project_scripts 的结果。"),
		}, "script_id"),
		func(ctx context.Context, in GetScriptInput) (GetScriptOutput, error) {
			projectID, err := requireProjectID(ctx, GetScriptToolName)
			if err != nil {
				return GetScriptOutput{}, err
			}

			scriptID, err := parseScriptID(in.ScriptID)
			if err != nil {
				return GetScriptOutput{}, fmt.Errorf("%s: %w", GetScriptToolName, err)
			}

			browser := tool.ProjectResourceBrowserFrom(ctx)
			if browser == nil {
				return GetScriptOutput{}, fmt.Errorf("%s: project resource browser is not available in this runtime", GetScriptToolName)
			}

			script, err := browser.GetScriptByID(ctx, projectID, scriptID)
			if err != nil {
				return GetScriptOutput{
					ScriptID: scriptID,
					Error:    err.Error(),
				}, nil
			}

			return GetScriptOutput{
				ScriptID:       script.ScriptID,
				ScriptName:     script.ScriptName,
				Description:    script.Description,
				ScriptType:     script.ScriptType,
				ScriptFilePath: script.ScriptFilePath,
				IOSchemaPath:   script.IOSchemaPath,
			}, nil
		},
	).WithPermission("read", "", "按 ID 查看脚本基本信息与文件路径")
}
