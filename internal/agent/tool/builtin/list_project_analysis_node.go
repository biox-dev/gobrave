package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// ListProjectAnalysisNodesToolName 是「列出当前项目分析节点」工具名。
const ListProjectAnalysisNodesToolName = "list_project_analysis_node"

// ListProjectAnalysisNodesInput 是 list_project_analysis_node 工具的入参。
//
// ProjectID 不由模型提供：它取自执行事实（tool.ProjectIDFrom，当前用户激活项目），
// 避免模型去查别的项目的节点。其余字段都是可选过滤条件。
type ListProjectAnalysisNodesInput struct {
	Keyword    string `json:"keyword,omitempty"`     // 模糊匹配节点名称 / 节点 ID / 脚本名称
	AnalysisID string `json:"analysis_id,omitempty"` // 只列该分析运行（nextflow.id）下的节点
	WorkflowID string `json:"workflow_id,omitempty"` // 只列该工作流（pipeline_components_relation.id）下各次运行的节点
	ScriptID   string `json:"script_id,omitempty"`   // 只列该脚本（pipeline_components.id）的节点，跨分析运行
	Status     string `json:"status,omitempty"`      // 按节点状态过滤，如 success / failed / running
	Limit      int    `json:"limit,omitempty"`       // 返回条数上限，留空取默认值
}

// ProjectAnalysisNodeItem 是单个分析节点的摘要（工具回传给模型的字段）。
//
// 直接复用 runtimecontext.AnalysisNodeSummary：节点自身信息（名称 / 类型 / 工作目录 /
// 参数）与所属分析运行的执行事实由浏览器统一填充，工具层不再重复声明同构字段。
type ProjectAnalysisNodeItem = runtimecontext.AnalysisNodeSummary

// ListProjectAnalysisNodesOutput 是 list_project_analysis_node 工具返回给模型的结果。
type ListProjectAnalysisNodesOutput struct {
	ProjectID int64                     `json:"project_id,string"`
	Total     int                       `json:"total"`
	Nodes     []ProjectAnalysisNodeItem `json:"nodes"`
	Truncated bool                      `json:"truncated,omitempty"`
	Message   string                    `json:"message,omitempty"`
	Error     string                    `json:"error,omitempty"`
}

// listProjectAnalysisNodesDescription 返回 list_project_analysis_node 的工具描述（Markdown 格式）。
func listProjectAnalysisNodesDescription() string {
	return `## 用途

列出当前激活项目下的分析节点（analysis_nodes），默认按创建 id 倒序、最多返回 50 条。每个节点都带「属于哪次分析运行」与运行状态，便于一次性掌握项目里所有节点。

## 适用场景

- 用户问「这个项目有哪些节点 / 某个脚本在哪些分析里跑过 / 哪些节点失败了」时，先用本工具按条件拉清单；
- 想要找到节点再深入查看时：用本工具拿到 analysis_node_id，再用 get_analysis_node 看 params.json / 工作目录 / 输出目录，用 list_analysis_node_files 看系统自动登记的产物文件，用 run_analysis_node 重跑。

## 返回字段

- **analysis_node_id**：节点主键（字符串形式，可直接传给 get_analysis_node / run_analysis_node / update_analysis_node_*）；
- **node_name / node_type / creation_source**：节点名称与类型（agent 命令节点 / script 脚本节点 / scheduler 调度器节点）；
- **script_id / script_name**：节点绑定的脚本（agent 命令节点 script_id 为 0）；
- **node_id / sample_id**：DAG 内节点标识与样本标识；
- **analysis_run_id / analysis_key / analysis_name / job_status**：所属分析运行的主键 / UUID / 名称与运行状态；
- **status / exit_code / started_at / finished_at**：节点自身的运行状态、退出码与起止时间；
- **workspace_dir / output_dir / params_path**：节点工作目录、输出目录与 params 文件绝对路径。

## 可选过滤

- **keyword**：模糊匹配节点名称 / 节点 ID / 脚本名称；
- **analysis_id**：只看某次分析运行下的节点（先由 list_project_analysis 拿到运行 ID）；
- **workflow_id**：只看某个工作流各次运行产生的节点；
- **script_id**：按脚本汇总节点（跨分析运行，适合「这个脚本都跑过哪些节点」）；
- **status**：按节点状态过滤（如只列 failed）；
- **limit**：返回条数上限（默认 50，最大 200）。

## 后续工具

查看单个节点详情 / 参数用 get_analysis_node，查看系统登记产物用 list_analysis_node_files；重跑用 run_analysis_node；修改用 update_analysis_node_agent（agent 节点）或 update_analysis_node_script（script / scheduler 节点）；查看所属运行用 get_analysis_workflow。`
}

// ListProjectAnalysisNodes 返回一个「列出当前项目分析节点」工具。
//
// 行为：
//   - 从执行事实取当前项目 ID（缺失时直接报错）；
//   - 通过构造注入的 AnalysisNodeBrowser 列出该项目的分析节点，支持关键字 / 分析运行 /
//     工作流 / 脚本 / 状态过滤与条数上限；
//   - 每个节点回传名称、类型、脚本、所属分析运行与运行状态。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func ListProjectAnalysisNodes(browser runtimecontext.AnalysisNodeBrowser) tool.Tool {
	return tool.NewFunc(
		ListProjectAnalysisNodesToolName,
		listProjectAnalysisNodesDescription(),
		tool.Schema("列出当前项目分析节点的入参（全部可选）", map[string]any{
			"keyword":     tool.StringProperty("模糊匹配节点名称 / 节点 ID / 脚本名称；留空不过滤。"),
			"analysis_id": tool.StringProperty("只列该分析运行（nextflow.id）下的节点，字符串以保精度；留空不过滤。"),
			"workflow_id": tool.StringProperty("只列该工作流（pipeline_components_relation.id）各次运行的节点，字符串以保精度；留空不过滤。"),
			"script_id":   tool.StringProperty("只列该脚本（pipeline_components.id）的节点（跨分析运行），字符串以保精度；留空不过滤。"),
			"status":      tool.StringProperty("按节点状态过滤，如 success / failed / running；留空不过滤。"),
			"limit":       tool.IntegerProperty("返回条数上限，留空默认 50，最大 200。"),
		}),
		func(ctx context.Context, in ListProjectAnalysisNodesInput) (ListProjectAnalysisNodesOutput, error) {
			projectID, err := requireProjectID(ctx, ListProjectAnalysisNodesToolName)
			if err != nil {
				return ListProjectAnalysisNodesOutput{}, err
			}
			if browser == nil {
				return ListProjectAnalysisNodesOutput{}, fmt.Errorf("%s: project resource browser is not available in this runtime", ListProjectAnalysisNodesToolName)
			}

			analysisID, err := optionalPositiveID(in.AnalysisID, ListProjectAnalysisNodesToolName, "analysis_id")
			if err != nil {
				return ListProjectAnalysisNodesOutput{}, err
			}
			workflowID, err := optionalPositiveID(in.WorkflowID, ListProjectAnalysisNodesToolName, "workflow_id")
			if err != nil {
				return ListProjectAnalysisNodesOutput{}, err
			}
			scriptID, err := optionalPositiveID(in.ScriptID, ListProjectAnalysisNodesToolName, "script_id")
			if err != nil {
				return ListProjectAnalysisNodesOutput{}, err
			}
			limit, err := normalizeListLimit(in.Limit, ListProjectAnalysisNodesToolName)
			if err != nil {
				return ListProjectAnalysisNodesOutput{}, err
			}

			nodes, listErr := browser.ListAnalysisNodesByProject(ctx, projectID, runtimecontext.AnalysisNodeListFilters{
				Keyword:    in.Keyword,
				AnalysisID: analysisID,
				WorkflowID: workflowID,
				ScriptID:   scriptID,
				Status:     in.Status,
				Limit:      limit,
			})
			if listErr != nil {
				return ListProjectAnalysisNodesOutput{
					ProjectID: projectID,
					Nodes:     []ProjectAnalysisNodeItem{},
					Error:     listErr.Error(),
				}, nil
			}

			out := ListProjectAnalysisNodesOutput{
				ProjectID: projectID,
				Total:     len(nodes),
				Nodes:     make([]ProjectAnalysisNodeItem, 0, len(nodes)),
				Truncated: len(nodes) >= limit,
			}
			out.Nodes = append(out.Nodes, nodes...)
			if out.Total == 0 {
				out.Message = "当前项目下没有符合条件的分析节点。可以用 create_analysis_node_agent 创建命令节点，或在工作流运行后用 create_analysis_node_script 添加脚本节点。"
			} else if out.Truncated {
				out.Message = fmt.Sprintf("已按上限返回 %d 条，可能还有更多节点；可用 analysis_id / workflow_id / script_id / status 等过滤条件缩小范围。", limit)
			}
			return out, nil
		},
	).WithPermission("read", "", "列出当前项目下的分析节点")
}
