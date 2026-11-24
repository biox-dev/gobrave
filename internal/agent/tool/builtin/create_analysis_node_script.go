package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// CreateAnalysisNodeScriptToolName 是「为已有脚本创建分析节点（可选运行）」工具名。
const CreateAnalysisNodeScriptToolName = "create_analysis_node_script"

// CreateAnalysisNodeScriptInput 是 create_analysis_node_script 工具的入参。
//
// ProjectID 不由模型提供：它取自执行事实（tool.ProjectIDFrom，当前用户激活项目）。
type CreateAnalysisNodeScriptInput struct {
	ScriptID     string         `json:"script_id"`     // 脚本主键 ID（必填，字符串以保精度）
	NodeName     string         `json:"node_name"`     // 节点名称，留空时用脚本名称
	RequestParam map[string]any `json:"request_param"` // 初始请求参数（键为 io_schema inputs 的 name），可留空
	// Run 可选：是否在创建后立即运行。nil / true 表示运行（默认），false 表示只创建节点。
	Run            *bool `json:"run"`
	TimeoutSeconds int   `json:"timeout_seconds"` // 等待节点完成的超时（秒），可选
}

// CreateAnalysisNodeScriptOutput 是 create_analysis_node_script 工具返回给模型的结果。
//
// 除节点自身的路径（WorkspaceDir / ParamsPath）之外，也回传脚本在磁盘上的关键路径：
//   - ScriptFilePath：脚本主文件（如 main.R），由 types.Script.Files 推导；
//   - IOSchemaPath：脚本目录下的 io_schema.json（节点 patterns 的来源）。
type CreateAnalysisNodeScriptOutput struct {
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

// createAnalysisNodeScriptDescription 返回 create_analysis_node_script 的工具描述（Markdown 格式）。
//
// 把「节点如何从脚本 io_schema 初始化输出契约」「系统在什么条件下把产物登记为
// output_files」以及「要新增 / 调整登记产物时应改 io_schema 再重跑」写进描述，
// 模型只看工具描述就能判断该节点会收录哪些文件、缺文件时改哪里。
func createAnalysisNodeScriptDescription() string {
	return `## 用途

为已有脚本创建一个新的独立分析节点（可反复创建，用于同一脚本的多次独立分析）。所属项目取自当前会话上下文，无需传入。

## 入参

- **script_id**（必填）：脚本主键 ID（整数，字符串以保精度）。
- **node_name**（可选）：节点名称，留空时使用脚本名称。
- **request_param**（可选）：节点初始请求参数，键为脚本 io_schema.json inputs 中各条目的 name；传入时按 io_schema 解析并生成 params.json。
- **run**（可选，默认 true）：为 true 时创建后立即运行并等待结束，返回状态、执行日志与日志路径；为 false 时只创建节点，之后用 run_analysis_node 运行。
- **timeout_seconds**（可选，默认 600；run=false 时忽略）。

## 返回字段

节点 ID、节点名称、脚本 ID / 名称 / 类型、工作目录（workspace_dir）、params.json 路径、脚本主文件路径（script_file_path）、io_schema.json 路径（io_schema_path），以及运行状态 / 日志 / 日志路径。

## 产物文件（output_files）：系统自动登记

脚本节点（creation_source=script）在创建 / 每次运行时按脚本最新 io_schema.json 的 outputs 初始化节点的 output_patterns。节点运行成功且输出校验通过后，系统会把 output_patterns 中 type=file 的条目对应的真实产物登记到文件表 go_file（analysis_node_id 指向该节点），之后可用 get_analysis_node 的 output_files 查看，或在数据集 / 下游节点里引用：

- file_key 为该输出的句柄名（io_schema outputs 条目的 name），role 取该条目的 role 字段（缺省 DEFAULT）。
- 脚本必须把产物写到节点的 output/ 目录，并在 output/outputs.json 中按输出句柄给出真实产物路径；outputs.json 缺少句柄（或路径不存在）时输出校验失败，节点判失败且不会登记文件。
- 只有运行成功才登记；重复运行按 (analysis_node_id, file_key) 覆盖更新。

如果希望某些产物被系统收录：先用 update_script 修改脚本 io_schema.json 的 outputs（把产物句柄声明为 {"type": "file"}，必要时给出 role），确保脚本写出对应文件并登记到 output/outputs.json，再用 update_analysis_node_script（默认 run=true，运行前按最新 io_schema 刷新 output_patterns）或 run_analysis_node 重新运行，不要重复新建节点。`
}

// CreateAnalysisNodeScript 返回一个「为已有脚本创建分析节点（可选运行）」工具。
//
// 行为：按脚本最新 io_schema.json 初始化节点的 inputs_patterns / output_patterns；
// request_param 非空时按脚本表单解析出 resolved_inputs 并写入节点，同时生成
// params.json / run.sh 等运行期文件。run 为 true（默认）时紧接着用通用节点运行链路
// 提交运行并等待终态，返回状态、执行日志与关键路径；run 为 false 时只创建节点。
//
// 节点的产物由系统在运行成功后按 output_patterns 中 type=file 的条目自动登记到
// go_file（用 get_analysis_node 的 output_files 查看），相关规则写在工具描述里。
//
// 该工具会创建业务数据（写库 + 写盘），并在默认情况下触发一次节点运行，因此声明
// write 授权。
func CreateAnalysisNodeScript(creator runtimecontext.AnalysisNodeCreator, runner runtimecontext.AnalysisNodeRunner) tool.Tool {
	return tool.NewFunc(
		CreateAnalysisNodeScriptToolName,
		createAnalysisNodeScriptDescription(),
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
		func(ctx context.Context, in CreateAnalysisNodeScriptInput) (CreateAnalysisNodeScriptOutput, error) {
			projectID := tool.ProjectIDFrom(ctx)
			if projectID <= 0 {
				return CreateAnalysisNodeScriptOutput{}, fmt.Errorf("create_analysis_node_script: project_id is required (no active project in this conversation)")
			}

			scriptID, err := parseScriptID(in.ScriptID)
			if err != nil {
				return CreateAnalysisNodeScriptOutput{}, fmt.Errorf("create_analysis_node_script: %w", err)
			}

			if creator == nil {
				return CreateAnalysisNodeScriptOutput{}, fmt.Errorf("create_analysis_node_script: analysis node creator is not available in this runtime")
			}

			if err := authorizeResourceWrite(ctx, CreateAnalysisNodeScriptToolName, fmt.Sprintf("Create analysis node from script %d", scriptID), previewCreateAnalysisNodeScript(ctx, in)); err != nil {
				return CreateAnalysisNodeScriptOutput{}, err
			}

			result, err := creator.CreateAnalysisNode(ctx, runtimecontext.AnalysisNodeCreateInput{
				ProjectID:    projectID,
				ScriptID:     scriptID,
				NodeName:     in.NodeName,
				RequestParam: in.RequestParam,
			})
			if err != nil {
				return CreateAnalysisNodeScriptOutput{Error: err.Error()}, nil
			}

			// run 可选且默认为 true：nil 视为未传，按默认运行处理。
			run := true
			if in.Run != nil {
				run = *in.Run
			}

			out := CreateAnalysisNodeScriptOutput{
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

			if runner == nil {
				out.Ran = false
				out.Error = "create_analysis_node_script: analysis node runner is not available in this runtime; the node was created but not run"
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
	)
}

// previewCreateAnalysisNodeScript 生成批准前预览：新节点 params.json 将写入的请求参数
// （新增文件 diff）。未给出 request_param 时没有可预览的内容。
func previewCreateAnalysisNodeScript(_ context.Context, in CreateAnalysisNodeScriptInput) tool.PermissionPreview {
	if in.RequestParam == nil {
		return tool.PermissionPreview{}
	}
	content := paramsJSONContent(in.RequestParam)
	if content == "" {
		return tool.PermissionPreview{}
	}
	return textDiffPreview("params.json", "", content)
}
