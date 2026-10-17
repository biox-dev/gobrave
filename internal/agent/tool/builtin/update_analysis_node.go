package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// UpdateAnalysisNodeToolName 是「更新分析节点基本信息」工具名。
const UpdateAnalysisNodeToolName = "update_analysis_node"

// UpdateAnalysisNodeInput 是 update_analysis_node 工具的入参。
//
// 只开放 node_name 一个元数据字段：节点参数由 build_analysis_node_params / run_analysis_node
// 负责，patterns 由 io_schema 决定，运行状态由编排器维护，均不在本工具职责内。
type UpdateAnalysisNodeInput struct {
	AnalysisNodeID string `json:"analysis_node_id"` // 分析节点主键 ID（必填，字符串以保精度）
	NodeName       string `json:"node_name"`        // 新的节点名称（必填，非空）
}

// UpdateAnalysisNodeOutput 是 update_analysis_node 工具返回给模型的结果。
type UpdateAnalysisNodeOutput struct {
	AnalysisNodeID int64    `json:"analysis_node_id"`
	NodeName       string   `json:"node_name,omitempty"`
	ScriptID       int64    `json:"script_id,omitempty"`
	WorkspaceDir   string   `json:"workspace_dir,omitempty"`
	ParamsPath     string   `json:"params_path,omitempty"`
	Changed        []string `json:"changed,omitempty"`
	Error          string   `json:"error,omitempty"`
}

// UpdateAnalysisNode 返回一个「更新分析节点 node_name」工具。
//
// 行为：
//   - analysis_node_id / node_name 均必填，缺省直接报错；
//   - 项目归属取自 RuntimeContext.ProjectID，节点必须属于当前激活项目，否则报错；
//   - 只修改节点名称，不触碰参数、patterns 与运行状态；
//   - 回传更新后的节点名称与工作目录、params.json 路径。
//
// 该工具会修改业务数据（写库），因此声明 write 授权：上层在执行前会先走权限门禁。
func UpdateAnalysisNode() tool.Tool {
	return tool.NewFunc(
		UpdateAnalysisNodeToolName,
		"更新已有分析节点的名称（node_name）。analysis_node_id 与 node_name 均必填。只改节点名称，不修改节点参数（参数请用 build_analysis_node_params 或 run_analysis_node）与运行状态。返回更新后的 node_name、workspace_dir 与 params_path。",
		tool.Schema("更新分析节点名称的入参", map[string]any{
			"analysis_node_id": tool.StringProperty("要更新的分析节点 ID（必填，整数，字符串以保精度）。"),
			"node_name":        tool.StringProperty("新的节点名称（必填，不能为空）。"),
		}, "analysis_node_id", "node_name"),
		func(ctx context.Context, in UpdateAnalysisNodeInput) (UpdateAnalysisNodeOutput, error) {
			projectID, err := requireProjectID(ctx, UpdateAnalysisNodeToolName)
			if err != nil {
				return UpdateAnalysisNodeOutput{}, err
			}

			nodeID, err := parseAnalysisNodeID(in.AnalysisNodeID)
			if err != nil {
				return UpdateAnalysisNodeOutput{}, fmt.Errorf("%s: %w", UpdateAnalysisNodeToolName, err)
			}

			nodeName := strings.TrimSpace(in.NodeName)
			if nodeName == "" {
				return UpdateAnalysisNodeOutput{}, fmt.Errorf("%s: node_name is required", UpdateAnalysisNodeToolName)
			}

			updater := tool.AnalysisNodeUpdaterFrom(ctx)
			if updater == nil {
				return UpdateAnalysisNodeOutput{}, fmt.Errorf("%s: analysis node updater is not available in this runtime", UpdateAnalysisNodeToolName)
			}

			name := nodeName
			result, err := updater.UpdateAnalysisNodeInfo(ctx, tool.AnalysisNodeUpdateInput{
				ProjectID:      projectID,
				AnalysisNodeID: nodeID,
				NodeName:       &name,
			})
			if err != nil {
				return UpdateAnalysisNodeOutput{
					AnalysisNodeID: nodeID,
					Error:          err.Error(),
				}, nil
			}

			return UpdateAnalysisNodeOutput{
				AnalysisNodeID: result.AnalysisNodeID,
				NodeName:       result.NodeName,
				ScriptID:       result.ScriptID,
				WorkspaceDir:   result.WorkspaceDir,
				ParamsPath:     result.ParamsPath,
				Changed:        result.Changed,
			}, nil
		},
	).WithPermission("write", "", "更新分析节点名称（写库）")
}
