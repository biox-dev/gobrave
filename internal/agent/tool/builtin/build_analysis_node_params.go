package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// BuildAnalysisNodeParamsToolName 是「生成分析节点参数」工具名。
const BuildAnalysisNodeParamsToolName = "build_analysis_node_params"

// BuildAnalysisNodeParamsInput 是 build_analysis_node_params 工具的入参。
//
// AnalysisNodeID 为必填：模型必须显式给出要生成参数的分析节点 ID，不再从运行时上下文回退。
//
// RequestParam 的键必须与脚本 io_schema.json 中的 name 一一对应；它是对象而不是字符串，
// 因此这里声明为 map，由工具框架直接把模型生成的 JSON 对象反序列化进来。
type BuildAnalysisNodeParamsInput struct {
	AnalysisNodeID string         `json:"analysis_node_id"`
	RequestParam   map[string]any `json:"request_param"`
}

// BuildAnalysisNodeParamsOutput 是 build_analysis_node_params 工具返回给模型的结果。
//
// ParamsJSON 是写入 params.json 的实际正文：模型据此确认 io_schema 参数到运行期参数的
// 映射是否正确，随后可直接调用 run_analysis_node 以这份参数运行脚本。
type BuildAnalysisNodeParamsOutput struct {
	AnalysisNodeID int64          `json:"analysis_node_id,string"`
	ScriptID       int64          `json:"script_id,string,omitempty"`
	WorkspaceDir   string         `json:"workspace_dir,omitempty"`
	ParamsPath     string         `json:"params_path,omitempty"`
	CommandPath    string         `json:"command_path,omitempty"`
	ParamsJSON     string         `json:"params_json,omitempty"`
	ResolvedInputs map[string]any `json:"resolved_inputs,omitempty"`
	Warnings       []string       `json:"warnings,omitempty"`
	Error          string         `json:"error,omitempty"`
}

// BuildAnalysisNodeParams 返回一个「按 io_schema 生成分析节点参数」工具。
//
// 使用流程（供模型理解）：
//  1. 读脚本 io_schema.json（create_script 返回的 io_schema_path，或工作目录里的软链）；
//  2. 按 io_schema 的 inputs 拼出 request_param（键为各条目 name）；
//  3. 调用本工具：按脚本表单解析 request_param，刷新节点的 inputs_patterns /
//     output_patterns / resolved_inputs / request_param，并生成 params.json；
//  4. 调用 run_analysis_node 以刚生成的 params.json 运行脚本。
//
// 该工具会写库并写盘（params.json / run.sh 等运行期文件），因此声明 write 授权：上层在
// 执行前会先走权限门禁。返回的 Error 为空即表示 params.json 已就绪。
func BuildAnalysisNodeParams() tool.Tool {
	return tool.NewFunc(
		BuildAnalysisNodeParamsToolName,
		"根据脚本的 io_schema.json 生成分析节点的运行参数：把 request_param（键为 io_schema 中的 name）解析成节点结构化的输入/输出和解析后输入，刷新节点记录并写出 params.json。返回 params_path 与 params.json 的实际内容。生成后可直接调用 run_analysis_node 以这份参数运行脚本。",
		tool.Schema("生成分析节点参数的入参", map[string]any{
			"analysis_node_id": tool.StringProperty("要生成参数的分析节点 ID（必填，字符串以保精度）。"),
			"request_param": map[string]any{
				"type":        "object",
				"description": "按脚本 io_schema.json 的 inputs 生成的请求参数对象，键为各输入条目的 name，值为该输入的实际取值（如文件 ID、枚举值、数值等）。",
			},
		}, "analysis_node_id", "request_param"),
		func(ctx context.Context, in BuildAnalysisNodeParamsInput) (BuildAnalysisNodeParamsOutput, error) {
			projectID := tool.ProjectIDFrom(ctx)
			if projectID <= 0 {
				return BuildAnalysisNodeParamsOutput{}, fmt.Errorf("build_analysis_node_params: project_id is required (no active project in this conversation)")
			}

			nodeID, err := parseAnalysisNodeID(in.AnalysisNodeID)
			if err != nil {
				return BuildAnalysisNodeParamsOutput{}, wrapToolNameError(BuildAnalysisNodeParamsToolName, err)
			}
			if len(in.RequestParam) == 0 {
				return BuildAnalysisNodeParamsOutput{}, fmt.Errorf("build_analysis_node_params: request_param is required and must be a non-empty object")
			}

			builder := tool.AnalysisNodeParamsBuilderFrom(ctx)
			if builder == nil {
				return BuildAnalysisNodeParamsOutput{}, fmt.Errorf("build_analysis_node_params: analysis node params builder is not available in this runtime")
			}

			result, buildErr := builder.BuildAnalysisNodeParams(ctx, tool.AnalysisNodeParamsInput{
				AnalysisNodeID: nodeID,
				ProjectID:      projectID,
				RequestParam:   in.RequestParam,
			})

			out := BuildAnalysisNodeParamsOutput{
				AnalysisNodeID: result.AnalysisNodeID,
				ScriptID:       result.ScriptID,
				WorkspaceDir:   result.WorkspaceDir,
				ParamsPath:     result.ParamsPath,
				CommandPath:    result.CommandPath,
				ParamsJSON:     result.ParamsJSON,
				ResolvedInputs: result.ResolvedInputs,
				Warnings:       result.Warnings,
			}
			if out.AnalysisNodeID == 0 {
				out.AnalysisNodeID = nodeID
			}
			if buildErr != nil {
				out.Error = buildErr.Error()
			}
			return out, nil
		},
	).WithPermission("write", "", "生成分析节点参数（写库并写出 params.json 等运行期文件）")
}

// wrapToolNameError 给已有错误补上工具名前缀，避免重复前缀。
func wrapToolNameError(toolName string, err error) error {
	if err == nil {
		return nil
	}
	if strings.HasPrefix(err.Error(), toolName+":") {
		return err
	}
	return fmt.Errorf("%s: %w", toolName, err)
}
