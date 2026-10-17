package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// CreateAnalysisNodeToolName 是「为已有脚本创建分析节点」工具名。
const CreateAnalysisNodeToolName = "create_analysis_node"

// CreateAnalysisNodeInput 是 create_analysis_node 工具的入参。
//
// ProjectID 不由模型提供：它取自 RuntimeContext.ProjectID（当前用户激活项目）。
type CreateAnalysisNodeInput struct {
	ScriptID     string         `json:"script_id"`     // 脚本主键 ID（必填，字符串以保精度）
	NodeName     string         `json:"node_name"`     // 节点名称，留空时用脚本名称
	RequestParam map[string]any `json:"request_param"` // 初始请求参数（键为 io_schema inputs 的 name），可留空
}

// CreateAnalysisNodeOutput 是 create_analysis_node 工具返回给模型的结果。
//
// 除节点自身的路径（WorkspaceDir / ParamsPath）之外，也回传脚本在磁盘上的关键路径：
//   - ScriptFilePath：脚本主文件（如 main.R），由 utils.GetScriptFile 推导；
//   - IOSchemaPath：脚本目录下的 io_schema.json（节点 patterns 的来源）。
type CreateAnalysisNodeOutput struct {
	AnalysisNodeID int64          `json:"analysis_node_id"`
	NodeName       string         `json:"node_name,omitempty"`
	ScriptID       int64          `json:"script_id,omitempty"`
	ScriptName     string         `json:"script_name,omitempty"`
	ScriptType     string         `json:"script_type,omitempty"`
	ScriptFilePath string         `json:"script_file_path,omitempty"`
	IOSchemaPath   string         `json:"io_schema_path,omitempty"`
	WorkspaceDir   string         `json:"workspace_dir,omitempty"`
	ParamsPath     string         `json:"params_path,omitempty"`
	RequestParam   map[string]any `json:"request_param,omitempty"`
	Error          string         `json:"error,omitempty"`
}

// CreateAnalysisNode 返回一个「为已有脚本创建独立分析节点」工具。
//
// 行为：按脚本最新 io_schema.json 初始化节点的 inputs_patterns / output_patterns；
// request_param 非空时按脚本表单解析出 resolved_inputs 并写入节点，同时生成
// params.json / run.sh 等运行期文件，返回节点 ID、关键路径与脚本主文件 / io_schema.json
// 路径，随后可直接用 run_analysis_node 运行该节点、或改脚本后重跑。
//
// 该工具会创建业务数据（写库 + 写盘），因此声明 write 授权。
func CreateAnalysisNode() tool.Tool {
	return tool.NewFunc(
		CreateAnalysisNodeToolName,
		"为已有脚本创建一个新的独立分析节点（可反复创建，用于同一脚本的多次独立分析）。script_id 必填；node_name 留空时使用脚本名称；可选传入 request_param（键为脚本 io_schema 的 name）作为节点的初始参数，传入时会按 io_schema 解析并生成 params.json。返回节点 ID、工作目录、params.json 路径，以及脚本主文件路径（script_file_path）与 io_schema.json 路径（io_schema_path），可直接用于编写 / 修改脚本与调用 run_analysis_node。所属项目取自当前会话上下文，无需传入。",
		tool.Schema("为已有脚本创建分析节点的入参", map[string]any{
			"script_id": tool.StringProperty("脚本主键 ID（必填，整数，字符串以保精度）。"),
			"node_name": tool.StringProperty("节点名称（可选），留空时使用脚本名称。"),
			"request_param": map[string]any{
				"type":        "object",
				"description": "节点的初始请求参数对象，键为脚本 io_schema.json inputs 中各条目的 name。可留空，留空时仅按 io_schema 初始化节点输入输出契约。",
			},
		}, "script_id"),
		func(ctx context.Context, in CreateAnalysisNodeInput) (CreateAnalysisNodeOutput, error) {
			rc := tool.RuntimeContextFrom(ctx)
			if rc == nil || rc.ProjectID <= 0 {
				return CreateAnalysisNodeOutput{}, fmt.Errorf("create_analysis_node: runtime context with project_id is required (no active project in this conversation)")
			}

			scriptID, err := parseScriptID(in.ScriptID)
			if err != nil {
				return CreateAnalysisNodeOutput{}, fmt.Errorf("create_analysis_node: %w", err)
			}

			creator := tool.ScriptCreatorFrom(ctx)
			if creator == nil {
				return CreateAnalysisNodeOutput{}, fmt.Errorf("create_analysis_node: script creator is not available in this runtime")
			}

			result, err := creator.CreateAnalysisNode(ctx, tool.AnalysisNodeCreateInput{
				ProjectID:    rc.ProjectID,
				ScriptID:     scriptID,
				NodeName:     in.NodeName,
				RequestParam: in.RequestParam,
			})
			if err != nil {
				return CreateAnalysisNodeOutput{Error: err.Error()}, nil
			}

			return CreateAnalysisNodeOutput{
				AnalysisNodeID: result.AnalysisNodeID,
				NodeName:       result.NodeName,
				ScriptID:       result.ScriptID,
				ScriptName:     result.ScriptName,
				ScriptType:     result.ScriptType,
				ScriptFilePath: result.ScriptFilePath,
				IOSchemaPath:   result.IOSchemaPath,
				WorkspaceDir:   result.WorkspaceDir,
				ParamsPath:     result.ParamsPath,
				RequestParam:   result.RequestParam,
			}, nil
		},
	).WithPermission("write", "", "为已有脚本创建分析节点（写库并初始化节点目录）")
}
