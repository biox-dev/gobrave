package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// UpdateAnalysisNodeAgentToolName 是「更新命令型分析节点」工具名。
const UpdateAnalysisNodeAgentToolName = "update_analysis_node_agent"

// UpdateAnalysisNodeAgentInput 是 update_analysis_node_agent 工具的入参。
//
// AnalysisNodeID 为必填：模型必须显式给出要更新的 command 节点 ID。
// 其余字段均可选：留空表示不修改对应字段；至少需要给出一个可更新字段。
type UpdateAnalysisNodeAgentInput struct {
	AnalysisNodeID      string  `json:"analysis_node_id"`      // 分析节点 ID（必填，字符串以保精度）
	NodeName            string  `json:"node_name"`             // 新节点名称，留空表示不修改
	Command             string  `json:"command"`               // 新 shell 命令（写入 run.sh），留空表示不修改
	ContainerTemplateID string  `json:"container_template_id"` // 新容器模板 ID（字符串以保精度），留空表示不修改
	File                string  `json:"file"`                  // 固定为 command（run.sh 文件编辑）
	Content             *string `json:"content"`               // 整体覆盖 run.sh 正文
	OldString           string  `json:"old_string"`            // 文本替换原文
	NewString           string  `json:"new_string"`            // 文本替换后的内容
	// Run 可选：更新后是否立即运行该节点。nil / true 表示运行（默认），false 表示只更新不运行。
	Run *bool `json:"run"`
	// TimeoutSeconds 是等待节点完成的超时（秒），可选；run=false 时忽略。
	TimeoutSeconds int `json:"timeout_seconds"`
}

// UpdateAnalysisNodeAgentOutput 是 update_analysis_node_agent 工具返回给模型的结果。
type UpdateAnalysisNodeAgentOutput struct {
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

// updateAnalysisNodeAgentDescription 返回 update_analysis_node_agent 的工具描述（Markdown 格式）。
//
// 描述里写清适用节点类型、可更新字段、「更新后默认运行」，以及 agent 命令节点的
// 产物如何被系统登记为 get_analysis_node 的 output_files、要新增 / 调整登记产物时
// 应改 command 后重跑（而不是新建节点）。
func updateAnalysisNodeAgentDescription() string {
	return `## 用途

更新 Agent 命令节点（creation_source=agent，无绑定脚本，run.sh 由 Agent 生成），默认更新后运行。仅适用于 get_analysis_node 返回 node_type=agent 的节点；script / scheduler 类型（绑定脚本）请改用 update_analysis_node_script。

## 入参

- **analysis_node_id**（必填）：要更新的节点 ID（整数，字符串以保精度）。
- 下列可更新字段至少给出一个，未给出的保持原值：
  - **node_name**：新的节点名称；
  - **command**：新的 shell 命令（多行可用换行分隔），会写入节点 run.sh；
  - **container_template_id**：新的容器模板 ID（必须是已存在的容器模板）；
  - **file / content / old_string / new_string**：直接编辑 run.sh（content 整体覆盖，或 old_string → new_string 唯一替换）。
- **run**（可选，默认 true）：为 true 时更新成功后立即运行并等待结束，返回状态、执行日志与日志路径；为 false 时只更新不运行（之后可用 run_analysis_node）。
- **timeout_seconds**（可选，默认 600；run=false 时忽略）。

command 会直接写入节点 run.sh（命令不落库），并重建 env.sh / params.json、清空节点输出目录（output）。

## 产物登记（output_files）：让文件出现在 get_analysis_node 的结果里

agent 命令节点没有 output_patterns：系统在节点运行成功、输出校验通过后，只把 output/outputs.json 中列出、且路径真实存在的文件登记到文件表 go_file（file_key 为 outputs.json 的 key，role 固定 DEFAULT）。get_analysis_node 返回的 output_files 就是这些登记记录。

要让新增 / 修改后的产物出现在 output_files 中：

1. 用 command（或 file/old_string/new_string 编辑 run.sh）让命令把产物写到 workspace_dir/output/ 下；
2. 在命令最后写 output/outputs.json（key 为语义化名称，value 为产物绝对路径）；
3. 保持 run=true（默认）重新运行。运行失败或路径不存在的条目不登记。

未登记进 outputs.json 的文件不会出现在 output_files 里，也无法被下游节点 / 数据集直接引用。`
}

// UpdateAnalysisNodeAgent 返回一个「更新 Agent 命令节点」工具。
//
// 适用节点类型：仅 creation_source=agent（get_analysis_node 返回 node_type=agent）的
// 命令型节点，即 create_analysis_node_agent 创建、无绑定脚本、run.sh 由 Agent 生成的节点。
// script / scheduler 类型（绑定脚本）请改用 update_analysis_node_script。
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
func UpdateAnalysisNodeAgent(updater runtimecontext.AnalysisNodeUpdater, runner runtimecontext.AnalysisNodeRunner, browser runtimecontext.AnalysisNodeBrowser) tool.Tool {
	return tool.NewFunc(
		UpdateAnalysisNodeAgentToolName,
		updateAnalysisNodeAgentDescription(),
		tool.Schema("更新 agent（命令）节点的入参", map[string]any{
			"analysis_node_id": tool.StringProperty("要更新的分析节点 ID（必填，整数，字符串以保精度）。"),
			"node_name":        tool.StringProperty("新的节点名称，留空表示不修改。"),
			"command":          tool.StringProperty("新的 shell 命令（多行可用换行分隔），留空表示不修改。命令会写入节点 run.sh，更新后立即生效。"),
			"container_template_id": tool.StringProperty(
				"新的容器模板 ID（整数，字符串以保精度），留空表示不修改；给出时必须是已存在的容器模板。"),
			"file":            tool.StringProperty("要直接编辑的文件：command（run.sh）。留空表示不改文件。"),
			"content":         tool.StringProperty("整体覆盖 run.sh 正文；与 old_string/new_string 二选一。"),
			"old_string":      tool.StringProperty("文本替换模式：要替换的原文，必须在 run.sh 中唯一出现。"),
			"new_string":      tool.StringProperty("文本替换模式：替换后的文本。"),
			"run":             tool.BooleanProperty("是否在更新后立即运行，可选，默认 true。传 false 时只更新节点，不提交运行。"),
			"timeout_seconds": tool.IntegerProperty("等待节点完成的超时（秒），可选，默认 600；run=false 时忽略。"),
		}, "analysis_node_id"),
		func(ctx context.Context, in UpdateAnalysisNodeAgentInput) (UpdateAnalysisNodeAgentOutput, error) {
			projectID, err := requireProjectID(ctx, UpdateAnalysisNodeAgentToolName)
			if err != nil {
				return UpdateAnalysisNodeAgentOutput{}, err
			}

			nodeID, err := parseAnalysisNodeID(in.AnalysisNodeID)
			if err != nil {
				return UpdateAnalysisNodeAgentOutput{}, wrapToolNameError(UpdateAnalysisNodeAgentToolName, err)
			}

			name := strings.TrimSpace(in.NodeName)
			command := strings.TrimSpace(in.Command)

			var templateID *int64
			if strings.TrimSpace(in.ContainerTemplateID) != "" {
				id, err := parseContainerTemplateID(in.ContainerTemplateID)
				if err != nil {
					return UpdateAnalysisNodeAgentOutput{}, wrapToolNameError(UpdateAnalysisNodeAgentToolName, err)
				}
				if id <= 0 {
					return UpdateAnalysisNodeAgentOutput{}, fmt.Errorf("%s: container_template_id must be a positive integer", UpdateAnalysisNodeAgentToolName)
				}
				templateID = &id
			}

			file, err := parseResourceFile(in.File, UpdateAnalysisNodeAgentToolName, runtimecontext.ResourceFileCommand)
			if err != nil {
				return UpdateAnalysisNodeAgentOutput{}, err
			}
			edit, err := resourceEditFrom(UpdateAnalysisNodeAgentToolName, file, in.Content, in.OldString, in.NewString)
			if err != nil {
				return UpdateAnalysisNodeAgentOutput{}, err
			}

			if name == "" && command == "" && templateID == nil && edit.IsZero() {
				return UpdateAnalysisNodeAgentOutput{}, fmt.Errorf("%s: at least one of node_name / command / container_template_id / file edit must be provided", UpdateAnalysisNodeAgentToolName)
			}

			if updater == nil {
				return UpdateAnalysisNodeAgentOutput{}, fmt.Errorf("%s: analysis node updater is not available in this runtime", UpdateAnalysisNodeAgentToolName)
			}

			preview, err := previewUpdateAnalysisNodeAgent(ctx, browser, projectID, nodeID, command, edit)
			if err != nil {
				return UpdateAnalysisNodeAgentOutput{AnalysisNodeID: nodeID, Error: err.Error()}, nil
			}
			if err := authorizeResourceWrite(ctx, UpdateAnalysisNodeAgentToolName, fmt.Sprintf("Update analysis node %d", nodeID), preview); err != nil {
				return UpdateAnalysisNodeAgentOutput{}, err
			}

			input := runtimecontext.AgentAnalysisUpdateInput{
				ProjectID:           projectID,
				AnalysisNodeID:      nodeID,
				ContainerTemplateID: templateID,
				File:                edit.File,
				Content:             edit.Content,
				OldString:           edit.OldString,
				NewString:           edit.NewString,
			}
			if name != "" {
				input.NodeName = &name
			}
			if command != "" {
				input.Command = &command
			}

			result, err := updater.UpdateAgentAnalysis(ctx, input)
			if err != nil {
				return UpdateAnalysisNodeAgentOutput{
					AnalysisNodeID: nodeID,
					Error:          err.Error(),
				}, nil
			}

			// run 可选且默认为 true：nil 视为未传，按默认运行处理。
			run := true
			if in.Run != nil {
				run = *in.Run
			}

			out := UpdateAnalysisNodeAgentOutput{
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

			if runner == nil {
				out.Ran = false
				out.Error = "update_analysis_node_agent: analysis node runner is not available in this runtime; the node was updated but not run"
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
	)
}

// previewUpdateAnalysisNodeAgent 生成批准前预览：run.sh 旧内容 → 新内容的 diff。
//
// 旧内容通过浏览器直接读取（命令不再落库）；读取失败时退化为「新增 / 覆盖 run.sh」的预览。
func previewUpdateAnalysisNodeAgent(ctx context.Context, browser runtimecontext.AnalysisNodeBrowser, projectID, nodeID int64, command string, edit runtimecontext.ResourceFileEdit) (tool.PermissionPreview, error) {
	if edit.IsZero() && strings.TrimSpace(command) == "" {
		return tool.PermissionPreview{}, nil
	}
	if browser == nil {
		// 无浏览器时退化为「无旧值」的预览，不影响授权与写入本身。
		if !edit.IsZero() && edit.Content != nil {
			return textDiffPreview("run.sh", "", runScriptContent(*edit.Content)), nil
		}
		if edit.IsZero() {
			return textDiffPreview("run.sh", "", runScriptContent(command)), nil
		}
		return tool.PermissionPreview{}, nil
	}
	current, err := browser.ReadAnalysisNodeFile(ctx, projectID, nodeID, runtimecontext.ResourceFileCommand)
	if err != nil {
		return tool.PermissionPreview{}, err
	}
	path := current.Path
	if path == "" {
		path = runScriptPath("", "")
	}
	after := ""
	if !edit.IsZero() {
		after, err = edit.Apply(current.Content)
		if err != nil {
			return tool.PermissionPreview{}, err
		}
	} else {
		after = runScriptContent(command)
	}
	return textDiffPreview(path, current.Content, after), nil
}
