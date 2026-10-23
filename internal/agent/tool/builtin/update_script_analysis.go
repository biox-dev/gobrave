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
// AnalysisNodeID 为必填：模型必须显式给出要更新的脚本节点 ID。
// 其余字段均可选：留空表示不修改对应字段；至少需要给出一个可更新字段。
//
// RequestParam 的键必须与脚本 io_schema.json 中的 name 一一对应。字段缺省时保持原值；
// 显式传入空对象（{}）表示清空节点已保存的参数。
type UpdateScriptAnalysisInput struct {
	AnalysisNodeID string         `json:"analysis_node_id"` // 分析节点 ID（必填，字符串以保精度）
	NodeName       string         `json:"node_name"`        // 新节点名称，留空表示不修改
	RequestParam   map[string]any `json:"request_param"`    // 新请求参数，nil 表示不修改
	// Run 可选：更新后是否立即运行该节点。nil / true 表示运行（默认），false 表示只更新不运行。
	Run *bool `json:"run"`
	// TimeoutSeconds 是等待节点完成的超时（秒），可选；run=false 时忽略。
	TimeoutSeconds int `json:"timeout_seconds"`
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
	// Ran 表示本次是否提交了运行（run=false 时为 false，Status/Log 等运行期字段为空）。
	Ran    bool   `json:"ran"`
	Status string `json:"status,omitempty"` // 节点终态（success / failed / timeout ...）
	// Success 为 true 表示节点以成功终态结束。
	Success bool   `json:"success"`
	Log     string `json:"log,omitempty"`
	LogPath string `json:"log_path,omitempty"`
	Error   string `json:"error,omitempty"`
}

// UpdateScriptAnalysis 返回一个「更新脚本节点」工具。
//
// 适用节点类型：仅 creation_source=script 或 scheduler（get_analysis_node 返回
// node_type=script / scheduler）的节点，即绑定脚本、参数由脚本 io_schema 解析的节点；
// agent 类型（无脚本的命令节点）请改用 update_agent_analysis。
//
// 行为：
//   - analysis_node_id 必填；node_name / request_param 至少给出一个；
//   - 项目归属取自执行事实（tool.ProjectIDFrom），节点必须属于当前激活项目；
//   - 只更新给出的字段，未给出的保持原值；
//   - request_param 非 nil 时按脚本最新 io_schema.json 重新解析并刷新节点参数，
//     重新生成 params.json 并清空节点输出目录（output）；空对象表示清空参数；
//   - run 可选且默认为 true：更新成功后立即运行该节点并等待结束，返回状态与执行日志；
//     run=false 时只更新节点，不提交运行。
//
// 该工具会写库并重写 params.json，并在默认情况下触发一次节点运行，因此声明 write 授权。
func UpdateScriptAnalysis() tool.Tool {
	return tool.NewFunc(
		UpdateScriptAnalysisToolName,
		"更新脚本节点（creation_source=script 或 scheduler，绑定脚本，参数按脚本 io_schema 解析），默认更新后运行。仅适用于 get_analysis_node 返回 node_type=script / scheduler 的节点；agent 类型（无脚本的命令节点）请改用 update_agent_analysis。analysis_node_id 必填；可选给出 node_name / request_param，至少给出一个；未给出的字段保持原值。request_param 的键为脚本 io_schema.json inputs 中各条目的 name，给出时会按 io_schema 重新解析并生成 params.json 并清空节点输出目录（output）（显式传空对象表示清空参数）。run 可选，默认 true：为 true 时更新成功后立即运行该节点并等待结束，返回状态、执行日志与日志路径；为 false 时只更新节点不运行（之后可用 run_analysis_node 运行）。timeout_seconds 可选，默认 600，run=false 时忽略。",
		tool.Schema("更新 script / scheduler 节点的入参", map[string]any{
			"analysis_node_id": tool.StringProperty("要更新的分析节点 ID（必填，整数，字符串以保精度）。"),
			"node_name":        tool.StringProperty("新的节点名称，留空表示不修改。"),
			"request_param": map[string]any{
				"type":        "object",
				"description": "新的请求参数对象，键为脚本 io_schema.json inputs 中各条目的 name。缺省表示不修改；显式传空对象（{}）表示清空参数。",
			},
			"run":             tool.BooleanProperty("是否在更新后立即运行，可选，默认 true。传 false 时只更新节点，不提交运行。"),
			"timeout_seconds": tool.IntegerProperty("等待节点完成的超时（秒），可选，默认 600；run=false 时忽略。"),
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

			// run 可选且默认为 true：nil 视为未传，按默认运行处理。
			run := true
			if in.Run != nil {
				run = *in.Run
			}

			out := UpdateScriptAnalysisOutput{
				AnalysisNodeID: result.AnalysisNodeID,
				NodeName:       result.NodeName,
				ScriptID:       result.ScriptID,
				WorkspaceDir:   result.WorkspaceDir,
				ParamsPath:     result.ParamsPath,
				CommandPath:    result.CommandPath,
				RequestParam:   result.RequestParam,
				Warnings:       result.Warnings,
				Changed:        result.Changed,
				Ran:            run,
			}
			if !run {
				return out, nil
			}

			runner := tool.AnalysisNodeRunnerFrom(ctx)
			if runner == nil {
				out.Ran = false
				out.Error = "update_script_analysis: analysis node runner is not available in this runtime; the node was updated but not run"
				return out, nil
			}

			// 更新后的 request_param 已写入节点，传 nil 让 runner 使用节点上保存的参数。
			runResult, runErr := runner.RunAnalysisNode(ctx, result.AnalysisNodeID, nil, in.TimeoutSeconds)
			out.Status = runResult.Status
			out.Success = runResult.Success
			out.Log = runResult.Log
			out.LogPath = runResult.LogPath
			if runResult.CommandPath != "" {
				out.CommandPath = runResult.CommandPath
			}
			if runResult.ParamsPath != "" {
				out.ParamsPath = runResult.ParamsPath
			}
			if runResult.RequestParam != nil {
				out.RequestParam = runResult.RequestParam
			}
			if runErr != nil {
				out.Error = runErr.Error()
			}
			return out, nil
		},
	).WithPermissionPreview("write", "", "更新脚本节点（script / scheduler；写库并重建 params.json，默认更新后运行）", previewUpdateScriptAnalysis)
}

// previewUpdateScriptAnalysis 生成批准前预览：params.json 中请求参数的旧值 → 新值 diff。
//
// 未给出 request_param 时没有参数改动可预览（仅改名不会展示 diff）；读取不到节点
// 当前参数时退化为「覆盖 params.json」的预览，不影响授权流程本身。
func previewUpdateScriptAnalysis(ctx context.Context, in UpdateScriptAnalysisInput) tool.PermissionPreview {
	if in.RequestParam == nil {
		return tool.PermissionPreview{}
	}
	after := paramsJSONContent(in.RequestParam)
	if after == "" {
		return tool.PermissionPreview{}
	}

	path := "params.json"
	before := ""
	if current, ok := analysisNodeForPreview(ctx, UpdateScriptAnalysisToolName, in.AnalysisNodeID); ok {
		if current.ParamsPath != "" {
			path = current.ParamsPath
		}
		before = paramsJSONContent(current.RequestParam)
	}
	return textDiffPreview(path, before, after)
}
