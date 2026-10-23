package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/dag/prepare"
)

// UpdateAgentAnalysisToolName 是「更新命令型分析节点」工具名。
const UpdateAgentAnalysisToolName = "update_agent_analysis"

// UpdateAgentAnalysisInput 是 update_agent_analysis 工具的入参。
//
// AnalysisNodeID 为必填：模型必须显式给出要更新的 command 节点 ID。
// 其余字段均可选：留空表示不修改对应字段；至少需要给出一个可更新字段。
type UpdateAgentAnalysisInput struct {
	AnalysisNodeID      string `json:"analysis_node_id"`      // 分析节点 ID（必填，字符串以保精度）
	NodeName            string `json:"node_name"`             // 新节点名称，留空表示不修改
	Command             string `json:"command"`               // 新 shell 命令（写入 run.sh），留空表示不修改
	ContainerTemplateID string `json:"container_template_id"` // 新容器模板 ID（字符串以保精度），留空表示不修改
	// Run 可选：更新后是否立即运行该节点。nil / true 表示运行（默认），false 表示只更新不运行。
	Run *bool `json:"run"`
	// TimeoutSeconds 是等待节点完成的超时（秒），可选；run=false 时忽略。
	TimeoutSeconds int `json:"timeout_seconds"`
}

// UpdateAgentAnalysisOutput 是 update_agent_analysis 工具返回给模型的结果。
type UpdateAgentAnalysisOutput struct {
	AnalysisNodeID      int64    `json:"analysis_node_id,string"`
	NodeName            string   `json:"node_name,omitempty"`
	Command             string   `json:"command,omitempty"`
	ContainerTemplateID int64    `json:"container_template_id,string,omitempty"`
	WorkspaceDir        string   `json:"workspace_dir,omitempty"`
	CommandPath         string   `json:"command_path,omitempty"`
	ParamsPath          string   `json:"params_path,omitempty"`
	Changed             []string `json:"changed,omitempty"`
	// Ran 表示本次是否提交了运行（run=false 时为 false，Status/Log 等运行期字段为空）。
	Ran    bool   `json:"ran"`
	Status string `json:"status,omitempty"` // 节点终态（success / failed / timeout ...）
	// Success 为 true 表示节点以成功终态结束。
	Success bool   `json:"success"`
	Log     string `json:"log,omitempty"`
	LogPath string `json:"log_path,omitempty"`
	Error   string `json:"error,omitempty"`
}

// UpdateAgentAnalysis 返回一个「更新 Agent 命令节点」工具。
//
// 适用节点类型：仅 creation_source=agent（get_analysis_node 返回 node_type=agent）的
// 命令型节点，即 create_agent_analysis 创建、无绑定脚本、run.sh 由 Agent 生成的节点。
// script / scheduler 类型（绑定脚本）请改用 update_script_analysis。
//
// 行为：
//   - analysis_node_id 必填；node_name / command / container_template_id 至少给出一个；
//   - 项目归属取自执行事实（tool.ProjectIDFrom），节点必须属于当前激活项目；
//   - 只更新给出的字段，未给出的保持原值；
//   - command 会直接写入节点 run.sh（命令不落库），并重建 env.sh / params.json、
//     清空节点输出目录（output）；
//   - run 可选且默认为 true：更新成功后立即运行该节点并等待结束，返回状态与执行日志；
//     run=false 时只更新节点，不提交运行。
//
// 该工具会修改业务数据并重写运行期文件，并在默认情况下触发一次节点运行，因此声明 write 授权。
func UpdateAgentAnalysis() tool.Tool {
	return tool.NewFunc(
		UpdateAgentAnalysisToolName,
		"更新 Agent 命令节点（creation_source=agent，无绑定脚本，run.sh 由 Agent 生成），默认更新后运行。仅适用于 get_analysis_node 返回 node_type=agent 的节点；script / scheduler 类型（绑定脚本）请改用 update_script_analysis。analysis_node_id 必填；可选给出 node_name / command / container_template_id，至少给出一个；未给出的字段保持原值。command 会直接写入节点 run.sh（命令不落库），并清空节点输出目录（output）。run 可选，默认 true：为 true 时更新成功后立即运行该节点并等待结束，返回状态、执行日志与日志路径；为 false 时只更新节点不运行（之后可用 run_analysis_node 运行）。timeout_seconds 可选，默认 600，run=false 时忽略。",
		tool.Schema("更新 agent（命令）节点的入参", map[string]any{
			"analysis_node_id": tool.StringProperty("要更新的分析节点 ID（必填，整数，字符串以保精度）。"),
			"node_name":        tool.StringProperty("新的节点名称，留空表示不修改。"),
			"command":          tool.StringProperty("新的 shell 命令（多行可用换行分隔），留空表示不修改。命令会写入节点 run.sh，更新后立即生效。"),
			"container_template_id": tool.StringProperty(
				"新的容器模板 ID（整数，字符串以保精度），留空表示不修改；给出时必须是已存在的容器模板。"),
			"run":             tool.BooleanProperty("是否在更新后立即运行，可选，默认 true。传 false 时只更新节点，不提交运行。"),
			"timeout_seconds": tool.IntegerProperty("等待节点完成的超时（秒），可选，默认 600；run=false 时忽略。"),
		}, "analysis_node_id"),
		func(ctx context.Context, in UpdateAgentAnalysisInput) (UpdateAgentAnalysisOutput, error) {
			projectID, err := requireProjectID(ctx, UpdateAgentAnalysisToolName)
			if err != nil {
				return UpdateAgentAnalysisOutput{}, err
			}

			nodeID, err := parseAnalysisNodeID(in.AnalysisNodeID)
			if err != nil {
				return UpdateAgentAnalysisOutput{}, wrapToolNameError(UpdateAgentAnalysisToolName, err)
			}

			name := strings.TrimSpace(in.NodeName)
			command := strings.TrimSpace(in.Command)

			var templateID *int64
			if strings.TrimSpace(in.ContainerTemplateID) != "" {
				id, err := parseContainerTemplateID(in.ContainerTemplateID)
				if err != nil {
					return UpdateAgentAnalysisOutput{}, wrapToolNameError(UpdateAgentAnalysisToolName, err)
				}
				if id <= 0 {
					return UpdateAgentAnalysisOutput{}, fmt.Errorf("%s: container_template_id must be a positive integer", UpdateAgentAnalysisToolName)
				}
				templateID = &id
			}

			if name == "" && command == "" && templateID == nil {
				return UpdateAgentAnalysisOutput{}, fmt.Errorf("%s: at least one of node_name / command / container_template_id must be provided", UpdateAgentAnalysisToolName)
			}

			updater := tool.AnalysisNodeUpdaterFrom(ctx)
			if updater == nil {
				return UpdateAgentAnalysisOutput{}, fmt.Errorf("%s: analysis node updater is not available in this runtime", UpdateAgentAnalysisToolName)
			}

			input := tool.AgentAnalysisUpdateInput{
				ProjectID:           projectID,
				AnalysisNodeID:      nodeID,
				ContainerTemplateID: templateID,
			}
			if name != "" {
				input.NodeName = &name
			}
			if command != "" {
				input.Command = &command
			}

			result, err := updater.UpdateAgentAnalysis(ctx, input)
			if err != nil {
				return UpdateAgentAnalysisOutput{
					AnalysisNodeID: nodeID,
					Error:          err.Error(),
				}, nil
			}

			// run 可选且默认为 true：nil 视为未传，按默认运行处理。
			run := true
			if in.Run != nil {
				run = *in.Run
			}

			out := UpdateAgentAnalysisOutput{
				AnalysisNodeID:      result.AnalysisNodeID,
				NodeName:            result.NodeName,
				Command:             result.Command,
				ContainerTemplateID: result.ContainerTemplateID,
				WorkspaceDir:        result.WorkspaceDir,
				CommandPath:         result.CommandPath,
				ParamsPath:          result.ParamsPath,
				Changed:             result.Changed,
				Ran:                 run,
			}
			if !run {
				return out, nil
			}

			runner := tool.AnalysisNodeRunnerFrom(ctx)
			if runner == nil {
				out.Ran = false
				out.Error = "update_agent_analysis: analysis node runner is not available in this runtime; the node was updated but not run"
				return out, nil
			}

			// 命令型节点直接运行节点自身的 run.sh，无请求参数。
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
			if runErr != nil {
				out.Error = runErr.Error()
			}
			return out, nil
		},
	).WithPermissionPreview("write", "", "更新 Agent 命令节点（写库并重建 run.sh，默认更新后运行）", previewUpdateAgentAnalysis)
}

// previewUpdateAgentAnalysis 生成批准前预览：run.sh 旧内容 → 新内容的 diff。
//
// 旧内容直接从磁盘上的 run.sh 读取（命令不再落库）；读取失败时退化为「新增 / 覆盖
// run.sh」的预览，不影响授权流程本身。
func previewUpdateAgentAnalysis(ctx context.Context, in UpdateAgentAnalysisInput) tool.PermissionPreview {
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return tool.PermissionPreview{}
	}

	path := runScriptPath("", "")
	before := ""
	if current, ok := analysisNodeForPreview(ctx, UpdateAgentAnalysisToolName, in.AnalysisNodeID); ok {
		path = runScriptPath(current.CommandPath, current.WorkspaceDir)
		if content, err := prepare.ReadCommandRunScript(current.CommandPath); err == nil {
			before = runScriptContent(content)
		}
	}
	return textDiffPreview(path, before, runScriptContent(command))
}
