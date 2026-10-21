package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// CreateScriptAnalysisToolName 是「为已有脚本创建分析节点（可选运行）」工具名。
const CreateScriptAnalysisToolName = "create_script_analysis"

// CreateScriptAnalysisInput 是 create_script_analysis 工具的入参。
//
// ProjectID 不由模型提供：它取自 RuntimeContext.ProjectID（当前用户激活项目）。
type CreateScriptAnalysisInput struct {
	ScriptID     string         `json:"script_id"`     // 脚本主键 ID（必填，字符串以保精度）
	NodeName     string         `json:"node_name"`     // 节点名称，留空时用脚本名称
	RequestParam map[string]any `json:"request_param"` // 初始请求参数（键为 io_schema inputs 的 name），可留空
	// Run 可选：是否在创建后立即运行。nil / true 表示运行（默认），false 表示只创建节点。
	Run            *bool `json:"run"`
	TimeoutSeconds int   `json:"timeout_seconds"` // 等待节点完成的超时（秒），可选
}

// CreateScriptAnalysisOutput 是 create_script_analysis 工具返回给模型的结果。
//
// 除节点自身的路径（WorkspaceDir / ParamsPath）之外，也回传脚本在磁盘上的关键路径：
//   - ScriptFilePath：脚本主文件（如 main.R），由 utils.GetScriptFile 推导；
//   - IOSchemaPath：脚本目录下的 io_schema.json（节点 patterns 的来源）。
type CreateScriptAnalysisOutput struct {
	AnalysisNodeID int64          `json:"analysis_node_id,string,omitempty"`
	NodeName       string         `json:"node_name,omitempty"`
	ScriptID       int64          `json:"script_id,string,omitempty"`
	ScriptName     string         `json:"script_name,omitempty"`
	ScriptType     string         `json:"script_type,omitempty"`
	ScriptFilePath string         `json:"script_file_path,omitempty"`
	IOSchemaPath   string         `json:"io_schema_path,omitempty"`
	WorkspaceDir   string         `json:"workspace_dir,omitempty"`
	ParamsPath     string         `json:"params_path,omitempty"`
	RequestParam   map[string]any `json:"request_param,omitempty"`
	// Ran 表示本次是否提交了运行（run=false 时为 false，Status/Log 等运行期字段为空）。
	Ran    bool   `json:"ran"`
	Status string `json:"status,omitempty"` // 节点终态（success / failed / timeout ...）
	// Success 为 true 表示节点以成功终态结束。
	Success bool `json:"success"`
	// Log 是命令日志（超出上限时截断保留末尾，可用 LogPath 读取完整内容）。
	Log string `json:"log,omitempty"`
	// LogPath / CommandPath 便于模型复核本次运行的日志与 run.sh。
	LogPath     string `json:"log_path,omitempty"`
	CommandPath string `json:"command_path,omitempty"`
	Error       string `json:"error,omitempty"`
}

// CreateScriptAnalysis 返回一个「为已有脚本创建分析节点（可选运行）」工具。
//
// 行为：按脚本最新 io_schema.json 初始化节点的 inputs_patterns / output_patterns；
// request_param 非空时按脚本表单解析出 resolved_inputs 并写入节点，同时生成
// params.json / run.sh 等运行期文件。run 为 true（默认）时紧接着用通用节点运行链路
// 提交运行并等待终态，返回状态、执行日志与关键路径；run 为 false 时只创建节点。
//
// 该工具会创建业务数据（写库 + 写盘），并在默认情况下触发一次节点运行，因此声明
// write 授权。
func CreateScriptAnalysis() tool.Tool {
	return tool.NewFunc(
		CreateScriptAnalysisToolName,
		"为已有脚本创建一个新的独立分析节点（可反复创建，用于同一脚本的多次独立分析）。script_id 必填；node_name 留空时使用脚本名称；可选传入 request_param（键为脚本 io_schema 的 name）作为节点的初始参数，传入时会按 io_schema 解析并生成 params.json。run 可选，默认 true：为 true 时创建后立即运行该节点并等待结束，返回状态、执行日志与日志路径；为 false 时只创建节点不运行（之后可用 run_analysis_node 运行）。返回节点 ID、工作目录、params.json 路径，以及脚本主文件路径（script_file_path）与 io_schema.json 路径（io_schema_path）。所属项目取自当前会话上下文，无需传入。",
		tool.Schema("为已有脚本创建分析节点的入参", map[string]any{
			"script_id": tool.StringProperty("脚本主键 ID（必填，整数，字符串以保精度）。"),
			"node_name": tool.StringProperty("节点名称（可选），留空时使用脚本名称。"),
			"request_param": map[string]any{
				"type":        "object",
				"description": "节点的初始请求参数对象，键为脚本 io_schema.json inputs 中各条目的 name。可留空，留空时仅按 io_schema 初始化节点输入输出契约。",
			},
			"run":             tool.BooleanProperty("是否在创建后立即运行，可选，默认 true。传 false 时只创建节点，不提交运行。"),
			"timeout_seconds": tool.IntegerProperty("等待节点完成的超时（秒），可选，默认 600；run=false 时忽略。"),
		}, "script_id"),
		func(ctx context.Context, in CreateScriptAnalysisInput) (CreateScriptAnalysisOutput, error) {
			rc := tool.RuntimeContextFrom(ctx)
			if rc == nil || rc.ProjectID <= 0 {
				return CreateScriptAnalysisOutput{}, fmt.Errorf("create_script_analysis: runtime context with project_id is required (no active project in this conversation)")
			}

			scriptID, err := parseScriptID(in.ScriptID)
			if err != nil {
				return CreateScriptAnalysisOutput{}, fmt.Errorf("create_script_analysis: %w", err)
			}

			creator := tool.ScriptCreatorFrom(ctx)
			if creator == nil {
				return CreateScriptAnalysisOutput{}, fmt.Errorf("create_script_analysis: script creator is not available in this runtime")
			}

			result, err := creator.CreateAnalysisNode(ctx, tool.AnalysisNodeCreateInput{
				ProjectID:    rc.ProjectID,
				ScriptID:     scriptID,
				NodeName:     in.NodeName,
				RequestParam: in.RequestParam,
			})
			if err != nil {
				return CreateScriptAnalysisOutput{Error: err.Error()}, nil
			}

			// run 可选且默认为 true：nil 视为未传，按默认运行处理。
			run := true
			if in.Run != nil {
				run = *in.Run
			}

			out := CreateScriptAnalysisOutput{
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
				Ran:            run,
			}
			if !run {
				return out, nil
			}

			runner := tool.AnalysisNodeRunnerFrom(ctx)
			if runner == nil {
				out.Ran = false
				out.Error = "create_script_analysis: analysis node runner is not available in this runtime; the node was created but not run"
				return out, nil
			}

			// request_param 传 nil 时由 runner 回退到节点上刚保存的 request_param。
			runResult, runErr := runner.RunAnalysisNode(ctx, result.AnalysisNodeID, in.RequestParam, in.TimeoutSeconds)
			out.Status = runResult.Status
			out.Success = runResult.Success
			out.Log = runResult.Log
			out.LogPath = runResult.LogPath
			out.CommandPath = runResult.CommandPath
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
	).WithPermission("write", "", "为已有脚本创建分析节点（默认立即运行该节点）")
}
