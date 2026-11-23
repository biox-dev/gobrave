package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// GetAnalysisNodeToolName 是「按 ID 查看分析节点」工具名。
const GetAnalysisNodeToolName = "get_analysis_node"

// GetAnalysisNodeInput 是 get_analysis_node 工具的入参。
//
// AnalysisNodeID 为必填：模型必须显式给出要查看的分析节点 ID，不再从运行时上下文回退。
// 可以直接传用户消息里的 @analysis_node:<id> 引用文本（前缀会被容错剥离）。
// 项目归属取自执行事实（tool.ProjectIDFrom），节点必须属于当前激活项目。
type GetAnalysisNodeInput struct {
	AnalysisNodeID string   `json:"analysis_node_id"` // 分析节点主键 ID（必填，支持 "@analysis_node:<id>" 形式，字符串以保精度）
	Files          []string `json:"files"`            // command / params；留空时不回传文件正文
}

// GetAnalysisNodeOutput 是 get_analysis_node 工具返回给模型的结果。
//
// WorkspaceDir 是节点根目录（绝对路径），ParamsPath 与 OutputDir 均由它派生。
//
// 节点类型只有三种，由 CreationSource 标识，NodeType 是其归一化结果（两者取值一致）：
//   - agent：Agent 命令节点（无脚本，run.sh 由 Agent 生成），用 update_analysis_node_agent 修改；
//   - script：脚本节点（前端独立节点 / create_analysis_node_script 创建），用 update_analysis_node_script 修改；
//   - scheduler：DAG 调度器节点（绑定脚本），用 update_analysis_node_script 修改。
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
	// OutputDir 是节点输出目录的绝对路径（WorkspaceDir/output），每次运行前会被清空。
	OutputDir string `json:"output_dir"`
	// RequestParam 仅 script / scheduler 节点返回（键为脚本 io_schema inputs 的 name）；agent 节点不返回。
	RequestParam map[string]any `json:"request_param,omitempty"`
	// Files 是指定文件的正文（command → run.sh，params → params.json，output → 产物正文）。
	Files map[string]runtimecontext.ResourceFileContent `json:"files,omitempty"`
	Error string                                        `json:"error,omitempty"`
}

// analysisNodeTypeAgent 是 Agent 命令节点的归一化类型值（与 types.AnalysisNode.NodeType 取值一致）。
//
// 这里用字面量而不是引用 types 包，是为了避免 builtin → types → agent → builtin 的循环依赖。
const analysisNodeTypeAgent = "agent"

// getAnalysisNodeDescription 返回 get_analysis_node 的工具描述（Markdown 格式）。
//
// 与 bash 工具（bashDescription）同风格：把回传字段、按节点类型区分的字段口径、
// 「解释分析结果时优先读 output_dir/output.md」的要求直接写进描述。产物文件的登记记录
// （file_id / role 等元数据）已拆到独立工具 list_analysis_node_files，这里只保留
// files=["output"] 读取正文的说明并指向该工具。
func getAnalysisNodeDescription() string {
	return `## 用途

按 analysis_node_id 查看分析节点。用于回答用户对 @analysis_node:<id> 引用的提问：直接使用冒号后的 ID 作为 analysis_node_id（例如 @analysis_node:2121123876845916160 → analysis_node_id 传 2121123876845916160）。

## 返回字段

- **node_name**：节点名称。
- **node_type / creation_source**：节点类型，取值为 agent（Agent 命令节点）、script（脚本节点）、scheduler（DAG 调度器节点）。
- **node_type_description**：类型说明及可用工具。
- **workspace_dir**：节点工作目录绝对路径，output / params.json / run.sh / command.log 均在其下。
- **params_path**：节点 params.json 路径。
- **output_dir**：节点输出目录绝对路径（即 workspace_dir/output），运行前会被清空，运行产物（含分析报告 output.md）在此目录下可直接读取。
- **files**：本次显式请求读取的正文（入参 files 指定：command → run.sh，params → params.json，output → 节点 output 目录下的产物正文）。

回传字段按节点类型区分：

- node_type=agent：返回 **command_path**（节点 run.sh 绝对路径，可直接读取以复核命令），不返回 script_id / request_param。
- node_type=script / scheduler：返回 **script_id** 与 **request_param**（节点上已保存的请求参数，键为脚本 io_schema 中的 name），不返回 command_path。

## 产物文件：用 list_analysis_node_files

本工具不回传产物文件的登记记录（file_id / file_name / path / format / role / file_key / size / md5 / created_at）。系统在节点运行成功、输出校验通过后会把节点产物自动登记到文件表 go_file（go_file.analysis_node_id 指向该节点）；需要查看这些记录时调用 **list_analysis_node_files**（同样只传 analysis_node_id）。

需要直接读取产物正文时，可以用本工具 files=["output"]：先读 output/output.md（分析报告），再按登记顺序拼接已登记产物正文；每个文件前有 "===== <path> =====" 分隔。

- 自动跳过二进制 / 不存在的文件；总长度上限约 200KB，超出会截断并在末尾注明。
- 需要单个大产物（如 csv）正文时，优先用 list_analysis_node_files 返回的 file_id 调 get_file（支持 offset / limit 分批读取）。

## 解释分析结果

当用户输入 @analysis_node:<id>（如 @analysis_node:xxxxxxx）并询问对分析结果的解释时，必须查看本工具返回的 **output_dir** 路径下的内容，基于真实产物进行解释：

1. **优先读取 output_dir/output.md**：它是该节点的分析报告（脚本节点为渲染后的分析结论），先完整读其内容，据此解释分析结果。也可以用 files=["output"] 一次把 output.md 与已登记产物正文都读回来。
2. output.md 不存在或为空时，先调用 **list_analysis_node_files** 看登记产物（用返回的 file_id 调 get_file 读正文），再尝试读取 output_dir 下的其它文件。
3. 禁止只凭节点名称 / 参数臆造结果；产物缺失或为空时必须明确说明。

## 后续工具

修改节点前先看 node_type：agent 类型用 update_analysis_node_agent，script / scheduler 类型用 update_analysis_node_script；三种类型都可以用 run_analysis_node 运行，脚本型节点重算参数用 build_analysis_node_params；查看系统自动登记的产物文件用 list_analysis_node_files。`
}

// GetAnalysisNode 返回一个「按 ID 查看分析节点」工具。
//
// 行为：
//   - 解析必填入参 analysis_node_id（容错剥离 @analysis_node: 引用前缀）；
//   - 项目归属取自执行事实（tool.ProjectIDFrom），节点必须属于当前激活项目，否则报错。
//   - 回传节点名称、节点类型（creation_source / node_type：agent / script / scheduler）、
//     工作目录（绝对路径）、params.json 路径与输出目录（output）绝对路径；
//   - 按类型区分回传字段：agent 节点回传 command_path（run.sh 绝对路径），
//     不回传 script_id / request_param；script / scheduler 节点回传 script_id 与
//     已保存的 request_param，不回传 command_path。
//   - 入参 files 支持 output：一次性读回节点产物正文（output.md + 已登记产物，见
//     runtimecontext.ResourceFileOutput）；产物文件的登记记录（file_id / role 等元数据）
//     由独立工具 list_analysis_node_files 提供。
//   - 入参 files 支持 output：一次性读回节点产物正文（output.md + 已登记产物，见
//     runtimecontext.ResourceFileOutput），与 output_files 元数据互补。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func GetAnalysisNode(browser runtimecontext.AnalysisNodeBrowser) tool.Tool {
	return tool.NewFunc(
		GetAnalysisNodeToolName,
		getAnalysisNodeDescription(),
		tool.Schema("按 ID 查看分析节点的入参", map[string]any{
			"analysis_node_id": tool.StringProperty("分析节点主键 ID（必填，整数，字符串以保精度）。来自用户输入里的 @analysis_node:<id> 引用时直接使用冒号后的 ID。"),
			"files":            resourceFilesSchema("要直接读取正文的资源列表：command → run.sh，params → params.json，output → 节点 output 目录下的产物正文（先 output.md，再拼接已登记产物）；留空时不回传正文。", runtimecontext.ResourceFileCommand, runtimecontext.ResourceFileParams, runtimecontext.ResourceFileOutput),
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
				OutputDir:           node.OutputDir,
			}
			if isAgentAnalysisNode(node.NodeType) {
				// Agent 命令节点没有脚本与脚本参数，只回传 run.sh 路径。
				out.CommandPath = node.CommandPath
			} else {
				// script / scheduler 节点绑定脚本，回传脚本 ID 与已保存的脚本参数。
				out.ScriptID = node.ScriptID
				out.RequestParam = node.RequestParam
			}

			// 产物文件的登记记录由独立工具 list_analysis_node_files 提供，
			// 本工具只负责节点详情与 files 指定的正文读取。

			files, err := collectResourceFiles(in.Files, GetAnalysisNodeToolName, nil,
				runtimecontext.ResourceFileCommand, runtimecontext.ResourceFileParams, runtimecontext.ResourceFileOutput)
			if err != nil {
				out.Error = err.Error()
				return out, nil
			}
			if len(files) > 0 {
				contents, err := readResourceFiles(files, func(file runtimecontext.ResourceFile) (runtimecontext.ResourceFileContent, error) {
					return browser.ReadAnalysisNodeFile(ctx, projectID, nodeID, file)
				})
				if err != nil {
					out.Error = err.Error()
					return out, nil
				}
				out.Files = contents
			}
			return out, nil
		},
	).WithPermission("read", "", "按 ID 查看分析节点名称、工作目录与参数")
}

// isAgentAnalysisNode 报告归一化节点类型是否为 agent（Agent 命令节点）。
func isAgentAnalysisNode(nodeType string) bool {
	return strings.EqualFold(strings.TrimSpace(nodeType), analysisNodeTypeAgent)
}
