package builtin

import (
	"context"
	"fmt"
	"strings"

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
// WorkspaceDir 是节点根目录（绝对路径），ParamsPath 由它派生。
//
// 节点类型只有三种，由 CreationSource 标识，NodeType 是其归一化结果（两者取值一致）：
//   - agent：Agent 命令节点（无脚本，run.sh 由 Agent 生成），用 update_agent_analysis 修改；
//   - script：脚本节点（前端独立节点 / create_script_analysis 创建），用 update_script_analysis 修改；
//   - scheduler：DAG 调度器节点（绑定脚本），用 update_script_analysis 修改。
//
// 回传字段按节点类型区分，避免模型拿错口径：
//   - agent：返回 command_path（run.sh 绝对路径），不返回 script_id / request_param；
//   - script / scheduler：返回 script_id 与 request_param（键为脚本 io_schema inputs 的 name），
//     不返回 command_path。
//
// 三种类型都可以用 run_analysis_node 运行。
type GetAnalysisNodeOutput struct {
	AnalysisNodeID      int64  `json:"analysis_node_id,string"`
	NodeName            string `json:"node_name"`
	CreationSource      string `json:"creation_source,omitempty"`       // 落库值：agent / script / scheduler
	NodeType            string `json:"node_type,omitempty"`             // 归一化节点类型：agent / script / scheduler
	NodeTypeDescription string `json:"node_type_description,omitempty"` // 节点类型中文说明与可用工具
	// ScriptID 仅 script / scheduler 节点返回；agent 命令节点无脚本，该字段不出现。
	ScriptID int64 `json:"script_id,string,omitempty"`
	// WorkspaceDir 是节点根目录（绝对路径），output / params.json / run.sh / command.log 均在其下。
	WorkspaceDir string `json:"workspace_dir"`
	// CommandPath 仅 agent 命令节点返回，是节点本次执行的 run.sh 绝对路径。
	CommandPath string `json:"command_path,omitempty"`
	ParamsPath  string `json:"params_path"`
	// RequestParam 仅 script / scheduler 节点返回（键为脚本 io_schema inputs 的 name）；agent 节点不返回。
	RequestParam map[string]any `json:"request_param,omitempty"`
	Error        string         `json:"error,omitempty"`
}

// analysisNodeTypeAgent 是 Agent 命令节点的归一化类型值（与 types.AnalysisNode.NodeType 取值一致）。
//
// 这里用字面量而不是引用 types 包，是为了避免 builtin → types → agent → builtin 的循环依赖。
const analysisNodeTypeAgent = "agent"

// GetAnalysisNode 返回一个「按 ID 查看分析节点」工具。
//
// 行为：
//   - 解析必填入参 analysis_node_id；
//   - 项目归属取自 RuntimeContext.ProjectID，节点必须属于当前激活项目，否则报错；
//   - 回传节点名称、节点类型（creation_source / node_type：agent / script / scheduler）、
//     工作目录（绝对路径）与 params.json 路径；
//   - 按类型区分回传字段：agent 节点回传 command_path（run.sh 绝对路径），
//     不回传 script_id / request_param；script / scheduler 节点回传 script_id 与
//     已保存的 request_param，不回传 command_path。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func GetAnalysisNode() tool.Tool {
	return tool.NewFunc(
		GetAnalysisNodeToolName,
		"按 analysis_node_id 查看分析节点。返回 node_name（节点名称）、node_type / creation_source（节点类型：agent 为 Agent 命令节点、script 为脚本节点、scheduler 为 DAG 调度器节点）与 node_type_description（类型说明及可用工具）、workspace_dir（节点工作目录绝对路径，output / params.json / run.sh / command.log 均在其下）与 params_path（节点 params.json 路径）。返回字段按节点类型区分：node_type=agent 时返回 command_path（节点 run.sh 绝对路径，可直接读取以复核命令），不返回 script_id / request_param；node_type=script / scheduler 时返回 script_id 与 request_param（节点上已保存的请求参数，键为脚本 io_schema 中的 name），不返回 command_path。修改节点前先看 node_type：agent 类型用 update_agent_analysis，script / scheduler 类型用 update_script_analysis；三种类型都可以用 run_analysis_node 运行，脚本型节点重算参数用 build_analysis_node_params。",
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

			out := GetAnalysisNodeOutput{
				AnalysisNodeID:      node.AnalysisNodeID,
				NodeName:            node.NodeName,
				CreationSource:      node.CreationSource,
				NodeType:            node.NodeType,
				NodeTypeDescription: node.NodeTypeDescription,
				WorkspaceDir:        node.WorkspaceDir,
				ParamsPath:          node.ParamsPath,
			}
			if isAgentAnalysisNode(node.NodeType) {
				// Agent 命令节点没有脚本与脚本参数，只回传 run.sh 路径。
				out.CommandPath = node.CommandPath
			} else {
				// script / scheduler 节点绑定脚本，回传脚本 ID 与已保存的脚本参数。
				out.ScriptID = node.ScriptID
				out.RequestParam = node.RequestParam
			}
			return out, nil
		},
	).WithPermission("read", "", "按 ID 查看分析节点名称、工作目录与参数")
}

// isAgentAnalysisNode 报告归一化节点类型是否为 agent（Agent 命令节点）。
func isAgentAnalysisNode(nodeType string) bool {
	return strings.EqualFold(strings.TrimSpace(nodeType), analysisNodeTypeAgent)
}
