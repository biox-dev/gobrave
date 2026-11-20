package builtin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// GetWorkflowToolName 是「按 ID 查看工作流」工具名。
const GetWorkflowToolName = "get_workflow"

// GetWorkflowInput 是 get_workflow 工具的入参。
//
// WorkflowID 为必填：模型必须显式给出要查看的工作流 ID，不再从运行时上下文回退。
// 可以直接传用户消息里的 @workflow:<id> 引用文本（前缀会被容错剥离）。
type GetWorkflowInput struct {
	WorkflowID string   `json:"workflow_id"` // 工作流主键 ID（必填，支持 "@workflow:<id>" 形式，字符串以保精度）
	Files      []string `json:"files"`       // dag_definition / io_schema / readme；留空时全部返回
}

// GetWorkflowOutput 是 get_workflow 工具返回给模型的结果。
//
// 内嵌 runtimecontext.WorkflowDetail：工作流摘要 + dag_definition / io_schema 原文 +
// 工作流级 inputs + 引用到的脚本列表 + README 正文；Files 按文件类型回传正文。
type GetWorkflowOutput struct {
	runtimecontext.WorkflowDetail
	Files map[string]runtimecontext.ResourceFileContent `json:"files,omitempty"`
	Error string                                        `json:"error,omitempty"`
}

// getWorkflowDescription 返回 get_workflow 的工具描述（Markdown 格式）。
func getWorkflowDescription() string {
	return `## 用途

按 workflow_id 查看工作流（后续提交分析运行的载体）。用于回答用户对 @workflow:<id> 引用的提问：直接使用冒号后的 ID 作为 workflow_id。

## 返回字段

- **workflow_id / workflow_key**：主键与 relation_id（工作流目录名）。
- **name / description / category**：工作流元数据。
- **workflow_dir**：工作流目录绝对路径（dag_definition.json / io_schema.json / README.md 都在其下）。
- **dag_definition_path / io_schema_path / readme_file**：目录内三个文件的实际路径，可直接读写。
- **dag_definition**：DAG 定义原文，结构为 {"nodes": [...], "edges": [...]}；nodes[] 含 id / name / script_id / inputs / outputs，edges[] 含 source / sourceHandle / target / targetHandle。
- **io_schema**：工作流级输入输出契约原文；**inputs** 是其中 inputs 数组（运行分析时 request_param 的键）。
- **node_count / edge_count**：DAG 节点数与连线数。
- **scripts**：DAG 引用到的脚本（script_id 主键、script_key 目录名、script_name）。
- **readme_content**：README.md 正文（为空表示没有 README）。

## 后续工具

- 改名称 / 描述 / DAG / io_schema 用 update_workflow。
- 用该工作流跑一次分析：先看 scripts 确认节点绑定的脚本，再用 create_analysis_workflow 建一次分析运行（默认创建后提交；已有分析运行时用 run_analysis_workflow）。
- 查看单个脚本的输入输出契约用 get_script。`
}

// GetWorkflow 返回一个「按 ID 查看工作流」工具。
//
// 行为：
//   - 解析必填入参 workflow_id（容错剥离 @workflow: 前缀）；
//   - 项目归属取自执行事实（tool.ProjectIDFrom），工作流必须属于当前激活项目；
//   - 回传工作流元数据、目录内文件路径、DAG 定义与 io_schema 原文、引用脚本与 README。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func GetWorkflow(browser runtimecontext.WorkflowBrowser) tool.Tool {
	return tool.NewFunc(
		GetWorkflowToolName,
		getWorkflowDescription(),
		tool.Schema("按 ID 查看工作流的入参", map[string]any{
			"workflow_id": tool.StringProperty("工作流主键 ID（必填，整数，字符串以保精度）。来自用户输入里的 @workflow:<id> 引用时直接使用冒号后的 ID。"),
			"files":       resourceFilesSchema("要直接读取正文的文件列表；留空时返回全部三个文件。", runtimecontext.ResourceFileDagDefinition, runtimecontext.ResourceFileIOSchema, runtimecontext.ResourceFileReadme),
		}, "workflow_id"),
		func(ctx context.Context, in GetWorkflowInput) (GetWorkflowOutput, error) {
			projectID, err := requireProjectID(ctx, GetWorkflowToolName)
			if err != nil {
				return GetWorkflowOutput{}, err
			}
			workflowID, err := parseWorkflowID(in.WorkflowID)
			if err != nil {
				return GetWorkflowOutput{}, wrapToolNameError(GetWorkflowToolName, err)
			}
			if browser == nil {
				return GetWorkflowOutput{}, fmt.Errorf("%s: workflow browser is not available in this runtime", GetWorkflowToolName)
			}

			detail, loadErr := browser.GetWorkflowByID(ctx, projectID, workflowID)
			out := GetWorkflowOutput{WorkflowDetail: detail}
			if loadErr != nil {
				out.Error = loadErr.Error()
				return out, nil
			}

			files, err := collectResourceFiles(in.Files, GetWorkflowToolName,
				[]runtimecontext.ResourceFile{runtimecontext.ResourceFileDagDefinition, runtimecontext.ResourceFileIOSchema, runtimecontext.ResourceFileReadme},
				runtimecontext.ResourceFileDagDefinition, runtimecontext.ResourceFileIOSchema, runtimecontext.ResourceFileReadme)
			if err != nil {
				out.Error = err.Error()
				return out, nil
			}
			contents, err := readResourceFiles(files, func(file runtimecontext.ResourceFile) (runtimecontext.ResourceFileContent, error) {
				return browser.ReadWorkflowFile(ctx, projectID, workflowID, file)
			})
			if err != nil {
				out.Error = err.Error()
				return out, nil
			}
			out.Files = contents
			return out, nil
		},
	).WithPermission("read", "", "按 ID 查看工作流元数据、DAG 定义与 io_schema")
}

// parseWorkflowID 解析必填的工作流 ID：为空或非法时返回错误。
//
// 容错处理界面插入的 "@workflow:<id>" 引用前缀：
// 模型可原样把 chip 文本传进来，这里统一剥离后再解析。
func parseWorkflowID(raw string) (int64, error) {
	idStr := strings.TrimSpace(raw)
	if idx := strings.Index(idStr, "@workflow:"); idx >= 0 {
		idStr = strings.TrimSpace(idStr[idx+len("@workflow:"):])
	}
	if idStr == "" {
		return 0, fmt.Errorf("workflow_id is required")
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid workflow_id %q: %w", idStr, err)
	}
	if id <= 0 {
		return 0, fmt.Errorf("workflow_id must be a positive integer")
	}
	return id, nil
}
