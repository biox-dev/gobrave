package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// UpdateScriptAnalysisToolName 是「更新脚本型分析节点」工具名。
const UpdateScriptAnalysisToolName = "update_script_analysis"

// UpdateScriptAnalysisInput 是 update_script_analysis 工具的入参。
//
// AnalysisNodeID 为必填：模型必须显式给出要更新的脚本型节点 ID。
// 其余字段均可选：留空表示不修改对应字段；至少需要给出一个可更新字段。
//
// RequestParam 的键必须与脚本 io_schema.json 中的 name 一一对应。字段缺省时保持原值；
// 显式传入空对象（{}）表示清空节点已保存的参数。
type UpdateScriptAnalysisInput struct {
	AnalysisNodeID string         `json:"analysis_node_id"` // 分析节点 ID（必填，字符串以保精度）
	NodeName       string         `json:"node_name"`        // 新节点名称，留空表示不修改
	RequestParam   map[string]any `json:"request_param"`    // 新请求参数，nil 表示不修改
}

// UpdateScriptAnalysisOutput 是 update_script_analysis 工具返回给模型的结果。
type UpdateScriptAnalysisOutput struct {
	AnalysisNodeID int64          `json:"analysis_node_id,string"`
	NodeName       string         `json:"node_name,omitempty"`
	ScriptID       int64          `json:"script_id,string,omitempty"`
	WorkspaceDir   string         `json:"workspace_dir,omitempty"`
	ParamsPath     string         `json:"params_path,omitempty"`
	CommandPath    string         `json:"command_path,omitempty"`
	RequestParam   map[string]any `json:"request_param,omitempty"`
	Warnings       []string       `json:"warnings,omitempty"`
	Changed        []string       `json:"changed,omitempty"`
	Error          string         `json:"error,omitempty"`
}

// UpdateScriptAnalysis 返回一个「更新脚本型分析节点」工具。
//
// 行为：
//   - analysis_node_id 必填；node_name / request_param 至少给出一个；
//   - 项目归属取自 RuntimeContext.ProjectID，节点必须属于当前激活项目；
//   - 只更新给出的字段，未给出的保持原值；
//   - request_param 非 nil 时按脚本最新 io_schema.json 重新解析并刷新节点参数，
//     重新生成 params.json；空对象表示清空参数。
//
// 该工具会写库并重写 params.json，因此声明 write 授权。
func UpdateScriptAnalysis() tool.Tool {
	return tool.NewFunc(
		UpdateScriptAnalysisToolName,
		"更新脚本型分析节点。analysis_node_id 必填；可选给出 node_name / request_param，至少给出一个；未给出的字段保持原值。request_param 的键为脚本 io_schema.json inputs 中各条目的 name，给出时会按 io_schema 重新解析并生成 params.json（显式传空对象表示清空参数）。若节点是命令型节点请改用 update_agent_analysis。",
		tool.Schema("更新脚本型分析节点的入参", map[string]any{
			"analysis_node_id": tool.StringProperty("要更新的分析节点 ID（必填，整数，字符串以保精度）。"),
			"node_name":        tool.StringProperty("新的节点名称，留空表示不修改。"),
			"request_param": map[string]any{
				"type":        "object",
				"description": "新的请求参数对象，键为脚本 io_schema.json inputs 中各条目的 name。缺省表示不修改；显式传空对象（{}）表示清空参数。",
			},
		}, "analysis_node_id"),
		func(ctx context.Context, in UpdateScriptAnalysisInput) (UpdateScriptAnalysisOutput, error) {
			projectID, err := requireProjectID(ctx, UpdateScriptAnalysisToolName)
			if err != nil {
				return UpdateScriptAnalysisOutput{}, err
			}

			nodeID, err := parseAnalysisNodeID(in.AnalysisNodeID)
			if err != nil {
				return UpdateScriptAnalysisOutput{}, wrapToolNameError(UpdateScriptAnalysisToolName, err)
			}

			name := strings.TrimSpace(in.NodeName)
			if name == "" && in.RequestParam == nil {
				return UpdateScriptAnalysisOutput{}, fmt.Errorf("%s: at least one of node_name / request_param must be provided", UpdateScriptAnalysisToolName)
			}

			updater := tool.AnalysisNodeUpdaterFrom(ctx)
			if updater == nil {
				return UpdateScriptAnalysisOutput{}, fmt.Errorf("%s: analysis node updater is not available in this runtime", UpdateScriptAnalysisToolName)
			}

			input := tool.ScriptAnalysisUpdateInput{
				ProjectID:      projectID,
				AnalysisNodeID: nodeID,
				RequestParam:   in.RequestParam,
			}
			if name != "" {
				input.NodeName = &name
			}

			result, err := updater.UpdateScriptAnalysis(ctx, input)
			if err != nil {
				return UpdateScriptAnalysisOutput{
					AnalysisNodeID: nodeID,
					Error:          err.Error(),
				}, nil
			}

			return UpdateScriptAnalysisOutput{
				AnalysisNodeID: result.AnalysisNodeID,
				NodeName:       result.NodeName,
				ScriptID:       result.ScriptID,
				WorkspaceDir:   result.WorkspaceDir,
				ParamsPath:     result.ParamsPath,
				CommandPath:    result.CommandPath,
				RequestParam:   result.RequestParam,
				Warnings:       result.Warnings,
				Changed:        result.Changed,
			}, nil
		},
	).WithPermission("write", "", "更新脚本型分析节点（写库并重建 params.json）")
}
