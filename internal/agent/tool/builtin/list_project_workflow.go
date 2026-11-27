package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// ListProjectWorkflowsToolName 是「列出当前项目全部工作流」工具名。
const ListProjectWorkflowsToolName = "list_project_workflow"

// ListProjectWorkflowsInput 是 list_project_workflow 工具的入参。
//
// ProjectID 不由模型提供：它取自执行事实（tool.ProjectIDFrom，当前用户激活项目），
// 避免模型去查别的工作流。
type ListProjectWorkflowsInput struct{}

// ProjectWorkflowItem 是单个工作流的摘要（工具回传给模型的字段）。
//
// 直接复用 runtimecontext.WorkflowBrief：路径字段由 types.Workflow 的逐文件方法统一推导，
// 工具层不再重复声明一份同构字段。
type ProjectWorkflowItem = runtimecontext.WorkflowBrief

// ListProjectWorkflowsOutput 是 list_project_workflow 工具返回给模型的结果。
type ListProjectWorkflowsOutput struct {
	ProjectID int64                 `json:"project_id,string"`
	Total     int                   `json:"total"`
	Workflows []ProjectWorkflowItem `json:"workflows"`
	Message   string                `json:"message,omitempty"`
	Error     string                `json:"error,omitempty"`
}

// ListProjectWorkflows 返回一个「列出当前项目全部工作流」工具。
//
// 行为：
//   - 从执行事实取当前项目 ID（缺失时直接报错）；
//   - 通过构造注入的 WorkflowBrowser 列出该项目下的全部工作流（按 id 倒序）；
//   - 每个工作流回传名称 / 描述 / 分类、目录内文件路径（dag_definition.json、
//     io_schema.json、README.md）与 DAG 节点 / 连线数。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func ListProjectWorkflows(browser runtimecontext.WorkflowBrowser) tool.Tool {
	return tool.NewFunc(
		ListProjectWorkflowsToolName,
		"列出当前激活项目下的全部分析工作流（按创建 id 倒序）。返回每个工作流的 workflow_id（字符串形式，避免大整数精度丢失，可直接传给 get_workflow / update_workflow）、workflow_key（relation_id，工作流目录名）、name（名称）、description（描述）、category（分类）、workflow_dir（工作流目录绝对路径）、dag_definition_path / io_schema_path / readme_file（目录内文件路径，可直接读写）、node_count / edge_count（DAG 节点数与连线数，0 表示还没有 DAG，可用 update_workflow 补）。需要查看 DAG 与 io_schema 正文时，再用 get_workflow 按 workflow_id 查询。工作流只是一次次分析的载体：要真正跑一遍，先用 create_analysis_workflow 按 workflow_id 建一条分析运行（默认创建后提交），已有分析运行时用 run_analysis_workflow 提交。所属项目取自当前会话上下文，无需传入。",
		tool.Schema("列出当前项目全部工作流的入参（无需参数）", map[string]any{}),
		func(ctx context.Context, _ ListProjectWorkflowsInput) (ListProjectWorkflowsOutput, error) {
			projectID, err := requireProjectID(ctx, ListProjectWorkflowsToolName)
			if err != nil {
				return ListProjectWorkflowsOutput{}, err
			}
			if browser == nil {
				return ListProjectWorkflowsOutput{}, fmt.Errorf("%s: workflow browser is not available in this runtime", ListProjectWorkflowsToolName)
			}

			workflows, listErr := browser.ListWorkflowsByProject(ctx, projectID)
			if listErr != nil {
				return ListProjectWorkflowsOutput{
					ProjectID: projectID,
					Workflows: []ProjectWorkflowItem{},
					Error:     listErr.Error(),
				}, nil
			}

			out := ListProjectWorkflowsOutput{
				ProjectID: projectID,
				Total:     len(workflows),
				Workflows: make([]ProjectWorkflowItem, 0, len(workflows)),
			}
			for _, workflow := range workflows {
				out.Workflows = append(out.Workflows, workflow)
			}
			if out.Total == 0 {
				out.Message = "当前项目下还没有工作流。需要时可用 create_workflow 新建。"
			}
			return out, nil
		},
	).WithPermission("read", "", "列出当前项目下的全部工作流")
}
