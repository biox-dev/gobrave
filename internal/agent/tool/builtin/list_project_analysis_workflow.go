package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// ListProjectAnalysesWorkflowToolName 是「列出当前项目全部分析运行」工具名。
//
// 注意与 list_project_analysis_node 区分：本工具列的是 nextflow 表的行（一次工作流运行），
// 不列单个节点。
const ListProjectAnalysesWorkflowToolName = "list_project_analysis_workflow"

// ListProjectAnalysesWorkflowInput 是 list_project_analysis_workflow 工具的入参。
//
// ProjectID 不由模型提供：它取自执行事实（tool.ProjectIDFrom，当前用户激活项目），
// 避免模型去查别的项目的分析运行。其余字段都是可选过滤条件。
type ListProjectAnalysesWorkflowInput struct {
	Keyword    string `json:"keyword,omitempty"`     // 模糊匹配分析名称 / analysis_id
	WorkflowID string `json:"workflow_id,omitempty"` // 工作流主键（字符串以保精度），只列该工作流的运行
	JobStatus  string `json:"job_status,omitempty"`  // 运行状态，如 running / finished / failed
	CacheType  int    `json:"cache_type,omitempty"`  // 缓存策略 1~4，0 表示不过滤
	IsReport   *bool  `json:"is_report,omitempty"`   // 是否只列报告类分析，nil 表示不过滤
	Limit      int    `json:"limit,omitempty"`       // 返回条数上限，留空取默认值
}

// ProjectAnalysisWorkflowItem 是单次分析运行的摘要（工具回传给模型的字段）。
//
// 直接复用 runtimecontext.AnalysisBrief：节点状态分布与派生路径口径与
// get_analysis_workflow 保持一致，工具层不再重复声明一份同构字段。
type ProjectAnalysisWorkflowItem = runtimecontext.AnalysisBrief

// ListProjectAnalysesWorkflowOutput 是 list_project_analysis_workflow 工具返回给模型的结果。
type ListProjectAnalysesWorkflowOutput struct {
	ProjectID int64                         `json:"project_id,string"`
	Total     int                           `json:"total"`
	Analyses  []ProjectAnalysisWorkflowItem `json:"analyses"`
	Truncated bool                          `json:"truncated,omitempty"`
	Message   string                        `json:"message,omitempty"`
	Error     string                        `json:"error,omitempty"`
}

// listProjectAnalysesWorkflowDescription 返回 list_project_analysis_workflow 的工具描述（Markdown 格式）。
func listProjectAnalysesWorkflowDescription() string {
	return `## 用途

列出当前激活项目下的「分析运行」（nextflow 表的一行 = 某个工作流的一次提交），默认按创建 id 倒序、最多返回 50 条。

## 适用场景

- 用户问「这个项目跑过哪些分析 / 最近的运行是什么状态」时，先用本工具拿到 analysis_id 与 job_status；
- 需要找到某次运行再深入查看时：用本工具定位，再用 get_analysis_workflow 看详情、list_project_analysis_node 看这次运行里的节点。

## 命名说明

本工具列出的是「分析运行」（工作流运行，nextflow 行），不是单个节点；节点请用 list_project_analysis_node。

## 返回字段

- **analysis_id**：运行主键（字符串形式，可直接传给 get_analysis_workflow / update_analysis_workflow）；**analysis_key** 是 analysis_id UUID。
- **analysis_name / workflow_id / workflow_name**：运行名称与其工作流。
- **job_status**：运行状态（created / running / stopping / stopped / finished / failed）；**scheduler_mode** 是推进它的调度器（dag / dynamic / dataflow）。
- **cache_type**：缓存策略（1 全量重跑，2 复用已有节点，3 脚本未变则复用，4 脚本与参数都未变才复用）。
- **workspace_dir / params_path**：分析根目录与本次运行的 params.json 绝对路径。
- **node_count / node_status_counts**：节点总数与各状态分布。

## 可选过滤

- **keyword**：模糊匹配分析名称 / analysis_id；
- **workflow_id**：只列某个工作流的运行；
- **job_status**：按状态过滤（如只列 failed）；
- **cache_type**：按缓存策略过滤；
- **is_report**：只列（或排除）报告类分析；
- **limit**：返回条数上限（默认 50，最大 200）。

## 后续工具

看单次运行的参数与详情用 get_analysis_workflow；看节点用 list_project_analysis_node / get_analysis_node；新建一次运行用 create_analysis_workflow，重新提交用 run_analysis_workflow。`
}

// ListProjectAnalysesWorkflow 返回一个「列出当前项目全部分析运行」工具。
//
// 行为：
//   - 从执行事实取当前项目 ID（缺失时直接报错）；
//   - 通过构造注入的 AnalysisBrowser 列出该项目的分析运行，支持关键字 / 工作流 /
//     状态 / 缓存策略 / 报告过滤与条数上限；
//   - 每个运行回传名称、所属工作流、状态、调度器、工作目录与节点状态分布。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func ListProjectAnalysesWorkflow(browser runtimecontext.AnalysisBrowser) tool.Tool {
	return tool.NewFunc(
		ListProjectAnalysesWorkflowToolName,
		listProjectAnalysesWorkflowDescription(),
		tool.Schema("列出当前项目分析运行的入参（全部可选）", map[string]any{
			"keyword":     tool.StringProperty("模糊匹配分析名称 / analysis_id；留空不过滤。"),
			"workflow_id": tool.StringProperty("只列该工作流（pipeline_components_relation.id）下的运行，字符串以保精度；留空不过滤。"),
			"job_status":  tool.StringProperty("按运行状态过滤：created / running / stopping / stopped / finished / failed；留空不过滤。"),
			"cache_type":  tool.IntegerProperty("按缓存策略过滤：1 全量重跑，2 复用已有节点，3 脚本未变则复用，4 脚本与参数都未变才复用；0 表示不过滤。"),
			"is_report":   tool.BooleanProperty("只列（true）或排除（false）报告类分析；留空不过滤。"),
			"limit":       tool.IntegerProperty("返回条数上限，留空默认 50，最大 200。"),
		}),
		func(ctx context.Context, in ListProjectAnalysesWorkflowInput) (ListProjectAnalysesWorkflowOutput, error) {
			projectID, err := requireProjectID(ctx, ListProjectAnalysesWorkflowToolName)
			if err != nil {
				return ListProjectAnalysesWorkflowOutput{}, err
			}
			if browser == nil {
				return ListProjectAnalysesWorkflowOutput{}, fmt.Errorf("%s: analysis browser is not available in this runtime", ListProjectAnalysesWorkflowToolName)
			}

			workflowID, err := optionalPositiveID(in.WorkflowID, ListProjectAnalysesWorkflowToolName, "workflow_id")
			if err != nil {
				return ListProjectAnalysesWorkflowOutput{}, err
			}
			limit, err := normalizeListLimit(in.Limit, ListProjectAnalysesWorkflowToolName)
			if err != nil {
				return ListProjectAnalysesWorkflowOutput{}, err
			}
			if in.CacheType < 0 {
				return ListProjectAnalysesWorkflowOutput{}, fmt.Errorf("%s: cache_type must not be negative", ListProjectAnalysesWorkflowToolName)
			}

			analyses, listErr := browser.ListAnalysesByProject(ctx, projectID, runtimecontext.AnalysisListFilters{
				Keyword:    in.Keyword,
				WorkflowID: workflowID,
				JobStatus:  in.JobStatus,
				CacheType:  in.CacheType,
				IsReport:   in.IsReport,
				Limit:      limit,
			})
			if listErr != nil {
				return ListProjectAnalysesWorkflowOutput{
					ProjectID: projectID,
					Analyses:  []ProjectAnalysisWorkflowItem{},
					Error:     listErr.Error(),
				}, nil
			}

			out := ListProjectAnalysesWorkflowOutput{
				ProjectID: projectID,
				Total:     len(analyses),
				Analyses:  make([]ProjectAnalysisWorkflowItem, 0, len(analyses)),
				Truncated: len(analyses) >= limit,
			}
			out.Analyses = append(out.Analyses, analyses...)
			if out.Total == 0 {
				out.Message = "当前项目下没有符合条件的分析运行。需要新建一次运行可用 create_analysis_workflow。"
			} else if out.Truncated {
				out.Message = fmt.Sprintf("已按上限返回 %d 条，可能还有更早的运行；可用 workflow_id / job_status 等过滤条件缩小范围。", limit)
			}
			return out, nil
		},
	).WithPermission("read", "", "列出当前项目下的分析运行")
}
