package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/types"
)

// ListFileToolName 是「列出当前项目下文件」工具名。
const ListFileToolName = "list_file"

// ListFileInput 是 list_file 工具的入参。
//
// ProjectID 不由模型提供：它取自执行事实（tool.ProjectIDFrom，当前用户激活项目），
// 避免模型去查别的项目的文件。Roles 可按 go_file.role 过滤（DEFAULT / TABLE /
// DATABASE），为空表示全部；未知取值会被丢弃。
type ListFileInput struct {
	Roles []string `json:"roles"` // 角色过滤（go_file.role），可多选；为空返回全部
}

// ProjectFileItem 是单个文件的摘要（工具回传给模型的字段）。
//
// 直接复用 runtimecontext.ProjectFileBrief，工具层不再重复声明一份同构字段。
type ProjectFileItem = runtimecontext.ProjectFileBrief

// ListFileOutput 是 list_file 工具返回给模型的结果。
type ListFileOutput struct {
	ProjectID int64             `json:"project_id,string"`
	Total     int               `json:"total"`
	Files     []ProjectFileItem `json:"files"`
	Message   string            `json:"message,omitempty"`
	Error     string            `json:"error,omitempty"`
}

// ListFile 返回一个「列出当前项目下文件」工具。
//
// 行为：
//   - 从执行事实取当前项目 ID（缺失时直接报错）；
//   - 通过构造注入的 DatasetBrowser 列出该项目下文件（go_project_dataset ->
//     go_dataset_file -> go_file），可按 role 过滤；
//   - 每个文件回传 file_id / path / format / role / 所属数据集等信息。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func ListFile(browser runtimecontext.DatasetBrowser) tool.Tool {
	return tool.NewFunc(
		ListFileToolName,
		"列出当前激活项目下的文件（按文件 id 倒序）。可选 roles 按角色过滤（go_file.role，取值 DEFAULT / TABLE / DATABASE，可传多个）。返回每个文件的 file_id（字符串以保精度，可直接传给 get_file / update_file）、path（绝对路径）、format、role、dataset_id / dataset_name（所属数据集）与 file_name / size 等。需要读取文件内容时用 get_file；需要修改元数据时用 update_file。所属项目取自当前会话上下文，无需传入。",
		tool.Schema("列出当前项目文件的入参", map[string]any{
			"roles": map[string]any{
				"type":        "array",
				"description": "按角色过滤（go_file.role），可选值 [\"DEFAULT\",\"TABLE\",\"DATABASE\"]；为空返回全部文件。",
				"items":       map[string]any{"type": "string"},
			},
		}),
		func(ctx context.Context, in ListFileInput) (ListFileOutput, error) {
			projectID, err := requireProjectID(ctx, ListFileToolName)
			if err != nil {
				return ListFileOutput{}, err
			}
			if browser == nil {
				return ListFileOutput{}, fmt.Errorf("%s: file browser is not available in this runtime", ListFileToolName)
			}

			files, listErr := browser.ListFilesByProject(ctx, projectID, types.NormalizeRoles(in.Roles))
			if listErr != nil {
				return ListFileOutput{
					ProjectID: projectID,
					Files:     []ProjectFileItem{},
					Error:     listErr.Error(),
				}, nil
			}

			out := ListFileOutput{
				ProjectID: projectID,
				Total:     len(files),
				Files:     files,
			}
			if out.Total == 0 {
				out.Message = listFilesEmptyMessage(in.Roles)
			}
			return out, nil
		},
	).WithPermission("read", "", "列出当前项目下的文件")
}

// listFilesEmptyMessage 根据是否按角色过滤给出不同空提示。
func listFilesEmptyMessage(roles []string) string {
	if len(roles) == 0 {
		return "当前项目下还没有文件。"
	}
	return fmt.Sprintf("当前项目下没有角色为 %s 的文件。", strings.Join(roles, " / "))
}
