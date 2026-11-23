package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// ListAnalysisNodeFilesToolName 是「列出分析节点系统登记产物」工具名。
const ListAnalysisNodeFilesToolName = "list_analysis_node_files"

// ListAnalysisNodeFilesInput 是 list_analysis_node_files 工具的入参。
//
// AnalysisNodeID 为必填，口径与 get_analysis_node 一致：直接传节点主键 ID，
// 也容忍用户消息里 "@analysis_node:<id>" 的引用文本（前缀会被剥离）。
// 项目归属取自执行事实（tool.ProjectIDFrom），节点必须属于当前激活项目。
type ListAnalysisNodeFilesInput struct {
	AnalysisNodeID string `json:"analysis_node_id"` // 分析节点主键 ID（必填，支持 "@analysis_node:<id>" 形式，字符串以保精度）
}

// ListAnalysisNodeFilesOutput 是 list_analysis_node_files 工具返回给模型的结果。
//
// OutputFiles 是系统自动登记到 go_file 的节点产物（go_file.analysis_node_id 绑定），
// 不是用户手工添加的记录；没有产物时为空数组。
type ListAnalysisNodeFilesOutput struct {
	AnalysisNodeID int64 `json:"analysis_node_id,string"`
	// OutputFiles 每项含 file_id（go_file 主键，可直接传给 get_file 读正文）、
	// file_name、path（绝对路径）、format、role、file_key、size、md5、created_at。
	OutputFiles []runtimecontext.AnalysisNodeFile `json:"output_files"`
	Total       int                               `json:"total"`
	Message     string                            `json:"message,omitempty"`
	Error       string                            `json:"error,omitempty"`
}

// listAnalysisNodeFilesDescription 返回 list_analysis_node_files 的工具描述（Markdown 格式）。
//
// 把「谁在什么时候登记产物」「不同节点类型的登记口径」「没有产物意味着什么」
// 「怎么让产物被登记（用哪个更新工具）」「怎么读正文」直接写进描述，模型只看
// 本工具描述就能判断产物缺失该改哪里、拿到 file_id 后该调什么工具。
func listAnalysisNodeFilesDescription() string {
	return `## 用途

按 analysis_node_id 列出该节点「由系统自动登记」的产物文件。用于回答用户对 @analysis_node:<id> 的产物提问：直接使用冒号后的 ID 作为 analysis_node_id（例如 @analysis_node:2121123876845916160 → analysis_node_id 传 2121123876845916160）。

## 返回字段

- **analysis_node_id**：节点主键（字符串形式）。
- **output_files**：登记记录列表，每条包含：
  - file_id：go_file 主键，可直接传给 get_file 读正文（支持 offset / limit 分批读取大文件）；
  - file_name / path：展示名与产物绝对路径；
  - format / role / file_key / size / md5；
  - created_at：登记时间（RFC3339）。
- **total**：登记记录条数；没有产物时 output_files 为空数组。
- **message**：列表为空时的提示（说明可能的原因与下一步）。

## 这些记录由系统自动添加，不是用户手工登记

节点运行结束、输出校验通过后，系统会把节点产物自动登记到文件表 go_file（go_file.analysis_node_id 指向该节点）。登记逻辑由节点类型决定：

- **agent 命令节点**（create_analysis_node_agent 创建）：节点没有 output_patterns，系统遍历命令写出的 output/outputs.json，把其中「指向真实存在的文件」的条目全部登记；file_key 为 outputs.json 的 key，role 固定为 DEFAULT。没写进 outputs.json 的文件、或路径不存在的条目都不会出现。
- **script / scheduler 节点**（create_analysis_node_script 创建 / 绑定脚本）：系统按节点的 output_patterns 中 type=file 的条目登记。output_patterns 来自脚本 io_schema.json 的 outputs；file_key 为该输出的句柄名，role 取该条目的 role 字段（缺省 DEFAULT）。io_schema outputs 里没有声明为 type=file 的产物不会被登记。
- 只有运行成功（输出校验无错）时才登记；失败或输出缺失的节点可能没有 output_files。重复运行按 (analysis_node_id, file_key) 覆盖更新，不会重复堆积。

## 让产物出现在 output_files：要改什么、用哪个工具

- **agent 命令节点**：用 update_analysis_node_agent 修改 command（或用 file/old_string/new_string 直接编辑 run.sh），让命令把产物写到 workspace_dir/output/ 下，并在命令最后写 output/outputs.json（key 为语义化名称，value 为产物绝对路径），然后重新运行（该工具默认 run=true）。运行成功后这些文件才会出现在 output_files。
- **script / scheduler 节点**：先用 update_script 修改脚本 io_schema.json 的 outputs（把产物句柄声明为 {"type": "file"}，需要固定角色时给出 role），并确保脚本运行结束时在 output/outputs.json 中按句柄写出真实产物路径；再调用 update_analysis_node_script（默认 run=true，运行前会按最新 io_schema 刷新 output_patterns）或 run_analysis_node 重新运行。运行成功后系统才会把这些文件登记到 output_files。

## 读取产物正文

- 想一次读回节点产物正文（output.md + 已登记产物）：用 get_analysis_node 的 files=["output"]（先 output/output.md，再拼接已登记产物，约 200KB 上限）；也可以直接用本工具返回的 file_id 调 get_file 分别读取。
- 需要节点的工作目录 / params.json / run.sh 或节点类型时，用 get_analysis_node。`
}

// ListAnalysisNodeFiles 返回一个「列出分析节点系统登记产物」工具。
//
// 行为：
//   - 解析必填入参 analysis_node_id（容错剥离 @analysis_node: 引用前缀）；
//   - 项目归属取自执行事实（tool.ProjectIDFrom），节点必须属于当前激活项目，否则报错；
//   - 通过构造注入的 AnalysisNodeBrowser 返回系统登记到 go_file 的产物记录
//     （go_file.analysis_node_id 绑定），只读。
//
// 该工具是只读的，因此声明 read 授权：上层在执行前会先走权限门禁。
func ListAnalysisNodeFiles(browser runtimecontext.AnalysisNodeBrowser) tool.Tool {
	return tool.NewFunc(
		ListAnalysisNodeFilesToolName,
		listAnalysisNodeFilesDescription(),
		tool.Schema("按 ID 列出分析节点系统登记产物的入参", map[string]any{
			"analysis_node_id": tool.StringProperty("分析节点主键 ID（必填，整数，字符串以保精度）。来自用户输入里的 @analysis_node:<id> 引用时直接使用冒号后的 ID。"),
		}, "analysis_node_id"),
		func(ctx context.Context, in ListAnalysisNodeFilesInput) (ListAnalysisNodeFilesOutput, error) {
			projectID, err := requireProjectID(ctx, ListAnalysisNodeFilesToolName)
			if err != nil {
				return ListAnalysisNodeFilesOutput{}, err
			}

			nodeID, err := parseAnalysisNodeID(in.AnalysisNodeID)
			if err != nil {
				return ListAnalysisNodeFilesOutput{}, fmt.Errorf("%s: %w", ListAnalysisNodeFilesToolName, err)
			}

			if browser == nil {
				return ListAnalysisNodeFilesOutput{}, fmt.Errorf("%s: project resource browser is not available in this runtime", ListAnalysisNodeFilesToolName)
			}

			files, err := browser.ListAnalysisNodeFiles(ctx, projectID, nodeID)
			if err != nil {
				return ListAnalysisNodeFilesOutput{
					AnalysisNodeID: nodeID,
					OutputFiles:    []runtimecontext.AnalysisNodeFile{},
					Error:          err.Error(),
				}, nil
			}

			out := ListAnalysisNodeFilesOutput{
				AnalysisNodeID: nodeID,
				OutputFiles:    make([]runtimecontext.AnalysisNodeFile, 0, len(files)),
				Total:          len(files),
			}
			out.OutputFiles = append(out.OutputFiles, files...)
			if out.Total == 0 {
				out.Message = "该节点目前没有登记产物：可能尚未运行、运行失败 / 输出校验未通过，或产物没有写进 output/outputs.json（agent 命令节点）或脚本 io_schema 的 outputs（script / scheduler 节点）。"
			}
			return out, nil
		},
	).WithPermission("read", "", "列出分析节点由系统自动登记的产物文件")
}
