package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// GetAnalysisNodeToolName 是「按 ID 查看分析节点」工具名。
const GetAnalysisNodeToolName = "get_analysis_node"

// GetAnalysisNodeInput 是 get_analysis_node 工具的入参。
//
// AnalysisNodeID 为必填：模型必须显式给出要查看的分析节点 ID，不再从运行时上下文回退。
// 项目归属取自 RuntimeContext.ProjectID，节点必须属于当前激活项目。
type GetAnalysisNodeInput struct {
	AnalysisNodeID string `json:"analysis_node_id"` // 分析节点主键 ID（必填，字符串以保精度）
}

// GetAnalysisNodeOutput 是 get_analysis_node 工具返回给模型的结果。
//
// WorkspaceDir 是节点根目录（绝对路径），ParamsPath 由它派生；RequestParam 是节点上
// 已保存的请求参数（键为脚本 io_schema inputs 的 name）。
//
// 节点类型只有三种，由 CreationSource 标识，NodeType 是其归一化结果（两者取值一致）：
//   - agent：Agent 命令节点（无脚本，run.sh 由 Agent 生成），用 update_agent_analysis 修改；
//   - script：脚本节点（前端独立节点 / create_script_analysis 创建），用 update_script_analysis 修改；
//   - scheduler：DAG 调度器节点（绑定脚本），用 update_script_analysis 修改。
//
// 三种类型都可以用 run_analysis_node 运行。
type GetAnalysisNodeOutput struct {
	AnalysisNodeID      int64          `json:"analysis_node_id,string"`
	NodeName            string         `json:"node_name"`
	CreationSource      string         `json:"creation_source,omitempty"`       // 落库值：agent / script / scheduler
	NodeType            string         `json:"node_type,omitempty"`             // 归一化节点类型：agent / script / scheduler
	NodeTypeDescription string         `json:"node_type_description,omitempty"` // 节点类型中文说明与可用工具
	ScriptID            int64          `json:"script_id,string,omitempty"`
	WorkspaceDir        string         `json:"workspace_dir"`
	ParamsPath          string         `json:"params_path"`
	RequestParam        map[string]any `json:"request_param,omitempty"`
	Error               string         `json:"error,omitempty"`
}

// GetAnalysisNode 返回一个「按 ID 查看分析节点」工具。
//
// 行为：
//   - 解析必填入参 analysis_node_id；
//   - 项目归属取自 RuntimeContext.ProjectID，节点必须属于当前激活项目，否则报错；
//   - 回传节点名称、节点类型（creation_source / node_type：agent / script / scheduler）、
//     工作目录（绝对路径）、params.json 路径与已保存的 request_param。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func GetAnalysisNode() tool.Tool {
	return tool.NewFunc(
		GetAnalysisNodeToolName,
		"按 analysis_node_id 查看分析节点。返回 node_name（节点名称）、node_type / creation_source（节点类型：agent 为 Agent 命令节点、script 为脚本节点、scheduler 为 DAG 调度器节点）与 node_type_description（类型说明及可用工具）、workspace_dir（节点工作目录绝对路径，output / params.json / run.sh / command.log 均在其下）、params_path（节点 params.json 路径）与 request_param（节点上已保存的请求参数，键为脚本 io_schema 中的 name）。修改节点前先看 node_type：agent 类型用 update_agent_analysis，script / scheduler 类型用 update_script_analysis；三种类型都可以用 run_analysis_node 运行，脚本型节点重算参数用 build_analysis_node_params。",
		tool.Schema("按 ID 查看分析节点的入参", map[string]any{
			"analysis_node_id": tool.StringProperty("分析节点主键 ID（必填，整数，字符串以保精度）。"),
		}, "analysis_node_id"),
		func(ctx context.Context, in GetAnalysisNodeInput) (GetAnalysisNodeOutput, error) {
			projectID, err := requireProjectID(ctx, GetAnalysisNodeToolName)
			if err != nil {
				return GetAnalysisNodeOutput{}, err
			}

			nodeID, err := parseAnalysisNodeID(in.AnalysisNodeID)
			if err != nil {
				return GetAnalysisNodeOutput{}, fmt.Errorf("%s: %w", GetAnalysisNodeToolName, err)
			}

			browser := tool.ProjectResourceBrowserFrom(ctx)
			if browser == nil {
				return GetAnalysisNodeOutput{}, fmt.Errorf("%s: project resource browser is not available in this runtime", GetAnalysisNodeToolName)
			}

			node, err := browser.GetAnalysisNodeByID(ctx, projectID, nodeID)
			if err != nil {
				return GetAnalysisNodeOutput{
					AnalysisNodeID: nodeID,
					Error:          err.Error(),
				}, nil
			}

			return GetAnalysisNodeOutput{
				AnalysisNodeID:      node.AnalysisNodeID,
				NodeName:            node.NodeName,
				CreationSource:      node.CreationSource,
				NodeType:            node.NodeType,
				NodeTypeDescription: node.NodeTypeDescription,
				ScriptID:            node.ScriptID,
				WorkspaceDir:        node.WorkspaceDir,
				ParamsPath:          node.ParamsPath,
				RequestParam:        node.RequestParam,
			}, nil
		},
	).WithPermission("read", "", "按 ID 查看分析节点名称、工作目录与参数")
}
