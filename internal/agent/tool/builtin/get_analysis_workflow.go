package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// GetAnalysisWorkflowToolName 是「按 ID 查看分析运行（工作流运行）」工具名。
const GetAnalysisWorkflowToolName = "get_analysis_workflow"

// GetAnalysisWorkflowInput 是 get_analysis_workflow 工具的入参。
//
// AnalysisID 是一次分析运行的主键（nextflow 表 id），不是分析节点 ID。
type GetAnalysisWorkflowInput struct {
	AnalysisID string `json:"analysis_id"` // 分析运行主键 ID（必填，支持 "@analysis:<id>" 形式，字符串以保精度）
}

// GetAnalysisWorkflowOutput 是 get_analysis_workflow 工具返回给模型的结果。
type GetAnalysisWorkflowOutput struct {
	runtimecontext.AnalysisDetail
	Error string `json:"error,omitempty"`
}

// getAnalysisWorkflowDescription 返回 get_analysis_workflow 的工具描述（Markdown 格式）。
func getAnalysisWorkflowDescription() string {
	return `## 用途

按 analysis_id 查看一次「分析运行」（nextflow 表的一行：某个工作流的一次提交）。用于回答用户对 @analysis:<id> 引用的提问：直接使用冒号后的 ID 作为 analysis_id。

注意区分两个层级，别混用：
- **analysis**（本工具）：一次工作流运行，含 job_status / 全部节点；
- **analysis_node**（get_analysis_node）：这次运行里的单个节点，含工作目录 / params.json / 产物。

## 返回字段

- **analysis_id / analysis_key**：主键与 analysis_id（UUID）。
- **analysis_name / workflow_id / workflow_name**：分析名称与其工作流。
- **job_status**：运行状态（created / updated / running / stopping / stopped / finished / failed）；**scheduler_mode** 是推进它的调度器（dag / dynamic / dataflow）。
- **cache_type**：缓存策略（1 全量重跑，2 复用已有节点，3 脚本未变则复用，4 脚本与参数都未变才复用）。
- **workspace_dir**：分析根目录绝对路径；**params_path** 是本次运行的 params.json（编译后的参数）。
- **request_param**：本次运行保存的请求参数（键为工作流 io_schema.inputs 的 name）。
- **node_count / node_status_counts**：节点总数与各状态分布。

## 后续工具

- 看单个节点的参数与产物用 get_analysis_node（节点 ID 来自 UI 或项目分析树）。
- 改名称 / 参数 / 缓存策略用 update_analysis_workflow；新建一次运行用 create_analysis_workflow，重新提交运行用 run_analysis_workflow。
- 看工作流定义用 get_workflow（workflow_id 已随本工具回传）。`
}

// GetAnalysisWorkflow 返回一个「按 ID 查看分析运行」工具。
//
// 该工具是只读的，因此声明 read 授权。
func GetAnalysisWorkflow(browser runtimecontext.AnalysisBrowser) tool.Tool {
	return tool.NewFunc(
		GetAnalysisWorkflowToolName,
		getAnalysisWorkflowDescription(),
		tool.Schema("按 ID 查看分析运行的入参", map[string]any{
			"analysis_id": tool.StringProperty("分析运行主键 ID（必填，整数，字符串以保精度）。来自用户输入里的 @analysis:<id> 引用时直接使用冒号后的 ID。"),
		}, "analysis_id"),
		func(ctx context.Context, in GetAnalysisWorkflowInput) (GetAnalysisWorkflowOutput, error) {
			projectID, err := requireProjectID(ctx, GetAnalysisWorkflowToolName)
			if err != nil {
				return GetAnalysisWorkflowOutput{}, err
			}
			analysisID, err := parseAnalysisID(in.AnalysisID)
			if err != nil {
				return GetAnalysisWorkflowOutput{}, wrapToolNameError(GetAnalysisWorkflowToolName, err)
			}
			if browser == nil {
				return GetAnalysisWorkflowOutput{}, fmt.Errorf("%s: analysis browser is not available in this runtime", GetAnalysisWorkflowToolName)
			}

			detail, loadErr := browser.GetAnalysisByID(ctx, projectID, analysisID)
			out := GetAnalysisWorkflowOutput{AnalysisDetail: detail}
			if loadErr != nil {
				out.Error = loadErr.Error()
			}
			return out, nil
		},
	).WithPermission("read", "", "按 ID 查看一次分析运行的名称、状态、参数与节点分布")
}

// parseAnalysisID 解析必填的分析运行 ID：为空或非法时返回错误。
//
// 容错处理界面插入的 "@analysis:<id>" 引用前缀。注意不要与
// "@analysis_node:<id>" 混用：后者由 parseAnalysisNodeID 处理。
func parseAnalysisID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idx := strings.Index(idStr, "@analysis:"); idx >= 0 {
		idStr = strings.TrimSpace(idStr[idx+len("@analysis:"):])
	}
	if idStr == "" {
		return 0, fmt.Errorf("analysis_id is required")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid analysis_id %q: %w", idStr, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("analysis_id must be a positive integer")
	}
	return id, nil
}
