package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// ListProjectScriptsToolName 是「列出当前项目全部脚本」工具名。
const ListProjectScriptsToolName = "list_project_scripts"

// ListProjectScriptsInput 是 list_project_scripts 工具的入参。
//
// ProjectID 不由模型提供：它取自执行事实（tool.ProjectIDFrom，当前用户激活项目），
// 避免模型去查别的项目的脚本。
type ListProjectScriptsInput struct{}

// ProjectScriptItem 是单个脚本的摘要（工具回传给模型的字段）。
//
// ScriptFilePath / IOSchemaPath 由 utils.GetScriptFile / utils.WorkflowIOSchemaPath 推导，
// 模型可直接按这两个路径读写脚本主文件与 io_schema.json。
type ProjectScriptItem struct {
	ScriptID       int64  `json:"script_id,string"`
	ScriptName     string `json:"script_name"`
	Description    string `json:"description,omitempty"`
	ScriptType     string `json:"script_type,omitempty"`
	ScriptFilePath string `json:"script_file_path"`
	IOSchemaPath   string `json:"io_schema_path"`
}

// ListProjectScriptsOutput 是 list_project_scripts 工具返回给模型的结果。
type ListProjectScriptsOutput struct {
	ProjectID int64               `json:"project_id,string"`
	Total     int                 `json:"total"`
	Scripts   []ProjectScriptItem `json:"scripts"`
	Message   string              `json:"message,omitempty"`
	Error     string              `json:"error,omitempty"`
}

// ListProjectScripts 返回一个「列出当前项目全部脚本」工具。
//
// 行为：
//   - 从 RuntimeContext 取当前项目 ID（缺失时直接报错）；
//   - 通过注入的 ProjectResourceBrowser 列出该项目下的全部脚本；
//   - 每个脚本回传名称、描述、主文件路径与 io_schema.json 路径。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func ListProjectScripts() tool.Tool {
	return tool.NewFunc(
		ListProjectScriptsToolName,
		"列出当前激活项目下的全部分析脚本。返回每个脚本的 script_id（字符串形式，避免大整数精度丢失，可直接传给 get_script）、name（脚本名称）、description（描述）、script_file_path（脚本主文件路径，可直接读写）与 io_schema_path（脚本输入输出契约 io_schema.json 的路径）。需要查看某个脚本的细节时，再用 get_script 按 script_id 查询。所属项目取自当前会话上下文，无需传入。",
		tool.Schema("列出当前项目全部脚本的入参（无需参数）", map[string]any{}),
		func(ctx context.Context, _ ListProjectScriptsInput) (ListProjectScriptsOutput, error) {
			projectID, err := requireProjectID(ctx, ListProjectScriptsToolName)
			if err != nil {
				return ListProjectScriptsOutput{}, err
			}

			browser := tool.ProjectResourceBrowserFrom(ctx)
			if browser == nil {
				return ListProjectScriptsOutput{}, fmt.Errorf("%s: project resource browser is not available in this runtime", ListProjectScriptsToolName)
			}

			scripts, err := browser.ListScriptsByProject(ctx, projectID)
			if err != nil {
				return ListProjectScriptsOutput{
					ProjectID: projectID,
					Scripts:   []ProjectScriptItem{},
					Error:     err.Error(),
				}, nil
			}

			out := ListProjectScriptsOutput{
				ProjectID: projectID,
				Total:     len(scripts),
				Scripts:   make([]ProjectScriptItem, 0, len(scripts)),
			}
			for _, s := range scripts {
				out.Scripts = append(out.Scripts, ProjectScriptItem{
					ScriptID:       s.ScriptID,
					ScriptName:     s.ScriptName,
					Description:    s.Description,
					ScriptType:     s.ScriptType,
					ScriptFilePath: s.ScriptFilePath,
					IOSchemaPath:   s.IOSchemaPath,
				})
			}
			if out.Total == 0 {
				out.Message = "当前项目下还没有脚本。"
			}
			return out, nil
		},
	).WithPermission("read", "", "列出当前项目下的全部脚本")
}

// requireProjectID 从执行事实取当前激活项目 ID；缺失时返回带工具名的错误。
func requireProjectID(ctx context.Context, toolName string) (int64, error) {
	projectID := tool.ProjectIDFrom(ctx)
	if projectID <= 0 {
		return 0, fmt.Errorf("%s: project_id is required (no active project in this conversation)", toolName)
	}
	return projectID, nil
}
