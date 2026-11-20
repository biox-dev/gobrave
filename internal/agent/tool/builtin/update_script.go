package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// UpdateScriptToolName 是「更新脚本基本信息」工具名。
const UpdateScriptToolName = "update_script"

// UpdateScriptInput 是 update_script 工具的入参。
//
// 两类修改可在同一次调用中完成：
//   - 元数据：name / description（空串表示不修改）；
//   - 文件：file（script_file / io_schema / readme）+ content（整体覆盖）
//     或 old_string → new_string（唯一文本替换）。
//
// ScriptID 为必填：模型必须显式给出要更新的脚本 ID，不再从运行时上下文回退。
type UpdateScriptInput struct {
	ScriptID    string  `json:"script_id"`   // 脚本主键 ID（必填，字符串以保精度）
	Name        string  `json:"name"`        // 新脚本名称，留空表示不修改
	Description string  `json:"description"` // 新脚本描述，留空表示不修改
	File        string  `json:"file"`        // script_file / io_schema / readme，留空表示不改文件
	Content     *string `json:"content"`     // 整体覆盖正文；nil 表示不覆盖
	OldString   string  `json:"old_string"`  // 文本替换的原文（必须唯一出现）
	NewString   string  `json:"new_string"`  // 文本替换后的内容
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
func UpdateScript(updater runtimecontext.ScriptUpdater, browser runtimecontext.ScriptBrowser) tool.Tool {
	return tool.NewFunc(
		UpdateScriptToolName,
		"更新已有分析脚本。script_id 必填。可同时修改元数据（name / description）与脚本文件：file 取 script_file / io_schema / readme，配合 content（整体覆盖）或 old_string → new_string（唯一文本替换）直接编辑文件正文，无需再用 write / edit / bash。未给出的字段保持原值；返回更新后的名称、描述与脚本文件路径。",
		tool.Schema("更新脚本的入参", map[string]any{
			"script_id":   tool.StringProperty("脚本主键 ID（必填，整数，字符串以保精度）。"),
			"name":        tool.StringProperty("新的脚本名称。留空表示不修改名称。"),
			"description": tool.StringProperty("新的脚本描述。留空表示不修改描述。"),
			"file":        tool.StringProperty("要编辑的脚本文件：script_file / io_schema / readme。留空表示不改文件。"),
			"content":     tool.StringProperty("整体覆盖该文件正文；与 old_string/new_string 二选一。"),
			"old_string":  tool.StringProperty("文本替换模式：要替换的原文，必须在文件中唯一出现。"),
			"new_string":  tool.StringProperty("文本替换模式：替换后的文本。"),
		}, "script_id"),
		func(ctx context.Context, in UpdateScriptInput) (UpdateScriptOutput, error) {
			projectID := tool.ProjectIDFrom(ctx)
			if projectID <= 0 {
				return UpdateScriptOutput{}, fmt.Errorf("update_script: project_id is required (no active project in this conversation)")
			}

			name := strings.TrimSpace(in.Name)
			description := strings.TrimSpace(in.Description)

			file, err := parseResourceFile(in.File, UpdateScriptToolName,
				runtimecontext.ResourceFileScript, runtimecontext.ResourceFileIOSchema, runtimecontext.ResourceFileReadme)
			if err != nil {
				return UpdateScriptOutput{}, err
			}
			edit, err := resourceEditFrom(UpdateScriptToolName, file, in.Content, in.OldString, in.NewString)
			if err != nil {
				return UpdateScriptOutput{}, err
			}
			if name == "" && description == "" && edit.IsZero() {
				return UpdateScriptOutput{}, fmt.Errorf("update_script: at least one of name / description / file edit must be provided")
			}

			if updater == nil {
				return UpdateScriptOutput{}, fmt.Errorf("update_script: script updater is not available in this runtime")
			}

			scriptID, err := parseScriptID(in.ScriptID)
			if err != nil {
				return UpdateScriptOutput{}, fmt.Errorf("update_script: %w", err)
			}

			// 先算 diff：读取目标文件当前正文，按编辑模式得到 after，授权前展示给用户。
			preview := tool.PermissionPreview{}
			if !edit.IsZero() {
				if browser == nil {
					// 无浏览器时退化为「无旧值」的预览，不影响授权与写入本身。
					if edit.Content != nil {
						preview = previewNewFiles([]newFilePreview{{Path: string(file), Content: *edit.Content}})
					}
				} else {
					current, readErr := browser.ReadScriptFile(ctx, projectID, scriptID, file)
					if readErr != nil {
						return UpdateScriptOutput{ScriptID: scriptID, Error: readErr.Error()}, nil
					}
					after, applyErr := edit.Apply(current.Content)
					if applyErr != nil {
						return UpdateScriptOutput{ScriptID: scriptID, Error: applyErr.Error()}, nil
					}
					preview = previewFileEdit(current.Path, current.Content, after)
				}
			}
			if err := authorizeResourceWrite(ctx, UpdateScriptToolName, fmt.Sprintf("Update script %d", scriptID), preview); err != nil {
				return UpdateScriptOutput{}, err
			}

			updateInput := runtimecontext.ScriptUpdateInput{
				ProjectID: projectID,
				ScriptID:  scriptID,
				File:      edit.File,
				Content:   edit.Content,
				OldString: edit.OldString,
				NewString: edit.NewString,
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
	)
}

// parseScriptID 解析必填的脚本 ID：为空或非法时返回错误。
//
// 容错处理用户 / 模型可能带上的 "@script:" 引用前缀（如 "@script:2109711590457610240"）：
// 该前缀来自界面「Ask Agent」插入的引用 chip（脚本面板），
// 模型可原样把 chip 文本传进来，这里统一剥离后再解析。
func parseScriptID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idx := strings.Index(idStr, "@script:"); idx >= 0 {
		idStr = strings.TrimSpace(idStr[idx+len("@script:"):])
	}
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
