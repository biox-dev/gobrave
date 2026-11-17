package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/utils"
)

// CreateWorkflowToolName 是「创建工作流」工具名。
const CreateWorkflowToolName = "create_workflow"

// JSONText 是「既接受 JSON 字符串、也接受 JSON 对象 / 数组字面量」的入参类型。
//
// 模型经常把 dag_definition / io_schema 直接写成对象，也可能写成转义后的字符串；
// 两种写法都归一化成 JSON 文本，交给上层按 utils.WriteWorkflowDagDefinition /
// WriteWorkflowIOSchema 的落盘口径校验与格式化。
type JSONText string

// UnmarshalJSON 实现「字符串或内联 JSON 都接受」的解码。
func (t *JSONText) UnmarshalJSON(data []byte) error {
	if t == nil {
		return fmt.Errorf("JSONText: nil receiver")
	}
	trimmed := string(data)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*t = JSONText(s)
		return nil
	}
	*t = JSONText(trimmed)
	return nil
}

// value 返回 JSON 文本；nil 表示未提供。
func (t *JSONText) value() string {
	if t == nil {
		return ""
	}
	return string(*t)
}

// CreateWorkflowInput 是 create_workflow 工具的入参。
//
// ProjectID 不由模型提供：它取自执行事实（tool.ProjectIDFrom，当前用户激活项目）。
type CreateWorkflowInput struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	Category      string   `json:"category"`
	DagDefinition JSONText `json:"dag_definition"`
	IOSchema      JSONText `json:"io_schema"`
	Readme        string   `json:"readme"`
}

// CreateWorkflowOutput 是 create_workflow 工具返回给模型的结果。
//
// 内嵌 runtimecontext.WorkflowCreateResult（工作流摘要 + 目录内文件路径 + warnings），
// 只额外补 error。
type CreateWorkflowOutput struct {
	runtimecontext.WorkflowCreateResult
	Error string `json:"error,omitempty"`
}

// CreateWorkflow 返回一个「创建工作流」工具。
//
// 行为：在当前激活项目下新建一行工作流（pipeline_components_relation）并生成 relation_id，
// 同时把 dag_definition / io_schema / README 写入工作流目录，重新生成 workflow.json。
// 只落盘、不提交 git：「提交版本」仍由用户在 UI 侧触发。
//
// 该工具会写入业务数据，因此声明 write 授权：上层在执行前会先走权限门禁。
func CreateWorkflow(creator runtimecontext.WorkflowCreator) tool.Tool {
	return tool.NewFunc(
		CreateWorkflowToolName,
		`在当前激活项目下创建一个新的分析工作流。

创建工作流只产出「空壳 + 目录」：随后用 update_workflow 或直接编辑 dag_definition_path 指向的文件补 DAG 与 io_schema，再用 run_analysis 提交运行。

入参说明：
- name：工作流名称（必填）。
- description / category：描述与分类（category 留空时落 default）。
- dag_definition：DAG 定义。既可直接写 JSON 对象（推荐），也可写 JSON 字符串。结构为 {"nodes": [...], "edges": [...]}：
  - nodes[] 至少包含 id（节点唯一标识）、name（节点名）、script_id（引用的脚本 script_key，即脚本目录名 UUID）、inputs / outputs（句柄契约）；
  - edges[] 为 {"source": 源节点 id, "sourceHandle": 源输出句柄, "target": 目标节点 id, "targetHandle": 目标输入句柄}。
- io_schema：工作流级输入契约，结构为 {"inputs": [...], "outputs": [...]}，inputs 的 name 就是 run_analysis 时 request_param 的键。
- readme：工作流说明文档正文（写入工作流目录的 README.md），可留空。

返回：workflow_id（主键，后续工具都用它）、workflow_key（relation_id，目录名）、workflow_dir、dag_definition_path、io_schema_path、readme_file 与 warning（workflow.json 重新生成失败时的提示）。所属项目取自当前会话上下文，无需传入。`,
		tool.Schema("创建工作流的入参", map[string]any{
			"name":        tool.StringProperty("工作流名称（必填）。"),
			"description": tool.StringProperty("工作流描述，可选。"),
			"category":    tool.StringProperty("工作流分类，可选；留空时落 default。"),
			"dag_definition": map[string]any{
				"type":        "object",
				"description": "DAG 定义对象（也接受 JSON 字符串）：{\"nodes\": [...], \"edges\": [...]}，nodes[] 含 id / name / script_id / inputs / outputs，edges[] 含 source / sourceHandle / target / targetHandle。可留空，之后再用 update_workflow 补。",
			},
			"io_schema": map[string]any{
				"type":        "object",
				"description": "工作流级输入输出契约（也接受 JSON 字符串）：{\"inputs\": [...], \"outputs\": [...]}，inputs 的 name 即运行分析时 request_param 的键。可留空。",
			},
			"readme": tool.StringProperty("工作流说明文档 README.md 的正文，可选。"),
		}, "name"),
		func(ctx context.Context, in CreateWorkflowInput) (CreateWorkflowOutput, error) {
			projectID, err := requireProjectID(ctx, CreateWorkflowToolName)
			if err != nil {
				return CreateWorkflowOutput{}, err
			}
			if creator == nil {
				return CreateWorkflowOutput{}, fmt.Errorf("%s: workflow creator is not available in this runtime", CreateWorkflowToolName)
			}

			result, createErr := creator.CreateWorkflow(ctx, runtimecontext.WorkflowCreateInput{
				ProjectID:     projectID,
				Name:          in.Name,
				Description:   in.Description,
				Category:      in.Category,
				DagDefinition: in.DagDefinition.value(),
				IOSchema:      in.IOSchema.value(),
				Readme:        in.Readme,
			})

			out := CreateWorkflowOutput{WorkflowCreateResult: result}
			if createErr != nil {
				out.Error = createErr.Error()
			}
			return out, nil
		},
	).WithPermissionPreview("write", "", "创建工作流（写库并初始化工作流目录）", previewCreateWorkflow)
}

// previewCreateWorkflow 生成批准前预览：新建 dag_definition.json 的内容。
//
// 未给出 dag_definition 时没有文件改动可预览（只建空目录），回退到工具入参。
func previewCreateWorkflow(_ context.Context, in CreateWorkflowInput) tool.PermissionPreview {
	content := in.DagDefinition.value()
	if content == "" {
		return tool.PermissionPreview{}
	}
	return textDiffPreview(utils.DagDefinitionFileName, "", formattedJSONContent(content))
}
