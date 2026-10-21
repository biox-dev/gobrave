package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// CreateAgentAnalysisToolName 是「创建命令型分析节点（可选运行）」的工具名。
const CreateAgentAnalysisToolName = "create_agent_analysis"

// CreateAgentAnalysisInput 是 create_agent_analysis 工具的入参。
//
// ProjectID 不由模型提供：它取自 RuntimeContext.ProjectID（当前用户激活项目），
// 避免把任务写到别的项目下。
type CreateAgentAnalysisInput struct {
	Name string `json:"name"` // 节点名称（也用作容器资源名）
	// ContainerTemplateID 可选：为空时使用系统默认容器模板（is_default）。
	ContainerTemplateID string `json:"container_template_id"` // 容器模板 ID（字符串以保精度）
	Command             string `json:"command"`               // 要执行的 shell 命令
	// Run 可选：是否在创建后立即运行。nil / true 表示运行（默认），false 表示只创建节点。
	Run            *bool `json:"run"`
	TimeoutSeconds int   `json:"timeout_seconds"` // 等待完成的超时（秒），可选
}

// CreateAgentAnalysisOutput 是 create_agent_analysis 工具返回给模型的结果。
type CreateAgentAnalysisOutput struct {
	AgentAnalysisID     int64  `json:"agent_analysis_id,string"`
	ContainerInstanceID int64  `json:"container_instance_id,string,omitempty"`
	Status              string `json:"status"`
	Ran                 bool   `json:"ran"` // 本次是否提交了运行（run=false 时为 false）
	Success             bool   `json:"success"`
	ExitCode            int    `json:"exit_code"`
	// Log 是命令日志（超出上限时截断保留末尾，可用 LogPath 读取完整内容）。
	Log string `json:"log,omitempty"`
	// LogPath / WorkspaceDir / CommandPath 便于模型在任务结束后复核产物与命令。
	LogPath      string `json:"log_path,omitempty"`
	WorkspaceDir string `json:"workspace_dir,omitempty"`
	CommandPath  string `json:"command_path,omitempty"`
	Error        string `json:"error,omitempty"`
}

// createAgentAnalysisDescription 返回 create_agent_analysis 的工具描述。
//
// 与 bash 工具（bashDescription）同风格：把执行步骤、产物目录约定与 outputs.json 的登记
// 规则直接写进描述，模型只看工具描述就能写出「命令 + output/ 产物 + outputs.json 索引」的
// 正确用法，而无需额外翻查技能文档。
func createAgentAnalysisDescription() string {
	return `创建一个命令型分析节点：给定节点名称与一段 shell 命令（container_template_id 可选，留空时使用系统默认容器模板），系统在节点工作目录下生成 run.sh。run 可选，默认 true：为 true 时立即提交容器执行并等待结束，返回状态、退出码与命令日志；为 false 时只创建节点不运行，之后可用 run_analysis_node 再运行该节点。所属项目取自当前会话上下文，无需传入。

Reference chips（界面插入的引用）：用户消息里可能出现 @container:<id>（选中的容器模板）、@dataset:<id>（选中的数据集）、@file:<id>（文件）。遇到 @container:<id> 时，直接把冒号后的 ID 作为 container_template_id 传入，不要丢弃或改写；需要确认模板用途时先调用 get_container_template（返回 display_name 与 description）。遇到 @dataset:<id> 时可用 get_dataset 获取其工作目录绝对路径，并在 command 中直接使用该路径。

Before executing the command, please follow these steps:

1. Directory Verification（目录校验）:
 - 节点的 workspace_dir（工具返回）就是命令的运行目录，command 会写入该目录下的 run.sh。
 - 命令必须以 'mkdir -p output' 开头，把所有需要保留的结果统一写入运行目录下的 output/。
 - 最终交付产物（结果表、报告等）直接放在 output/ 第一级，即 output/<文件名>；中间文件 / 临时文件
   放到 output/ 的子目录（如 output/tmp/、output/work/），不要写到运行目录根、/tmp 或家目录。
 - 产物文件名固定、可预期，便于运行后用 workspace_dir/output/ 下的文件核对结果。

2. Output Processing（重要产物必须登记到 outputs.json）:
 - 需要用户查看、或可给下游分析节点接着用的表格 / 文本产物，必须写入 output/outputs.json。
 - 只登记重要产物：csv / tsv / xlsx / txt / json / md / html 等表格或文本结果。
 - 不要登记图片（png / jpg / jpeg / pdf / svg 等绘图产物），也不要登记日志
   （command.log、stdout / stderr 输出等）。
 - 不要登记中间文件、临时文件，也不要登记 outputs.json 自身。
 - outputs.json 是一个 JSON 对象：key 为语义化、稳定的 snake_case 名称（下游按该名称取产物，
   如 gene_presence_absence_csv），value 为产物的绝对路径（含文件名）。
 - 在命令的最后（所有产物都生成之后）写 outputs.json，保证登记的路径真实存在；同一节点只写
   一个 outputs.json，重复写会覆盖。

 示例：
 ~~~bash
 OD="$PWD/output"
 mkdir -p "$OD"
 # ... 生成 "$OD/gene_presence_absence.csv" 等产物 ...
 cat > "$OD/outputs.json" <<EOF
 {
   "gene_presence_absence_csv": "$OD/gene_presence_absence.csv",
   "gene_presence_absence_Rtab": "$OD/gene_presence_absence.Rtab",
   "summary_statistics_txt": "$OD/summary_statistics.txt"
 }
 EOF
 ~~~

3. Return Result（运行与返回）:
 - run=true（默认）时立即执行命令，返回 status、success、exit_code 与命令日志 log。
 - log 超出长度上限时会截断（保留末尾），完整日志用 log_path 读取；日志只用于排错，不要写进
   outputs.json。
 - 同时返回 workspace_dir / command_path / log_path，便于复核产物、命令与日志。
 - run=false 时只创建节点并生成 run.sh，不产生任何产物；之后用 run_analysis_node 运行，
   outputs.json 必须由 command 在执行时自行生成。

4. Failure Handling（失败处理）:
 - 返回 success=false 或带 error 时，先读 log_path 定位原因，再修复后重新运行，不要反复新建节点。
 - 修复方式二选一：直接修改 command_path 指向的 run.sh，再调用 run_analysis_node；或调用
   update_agent_analysis 更新 command（会重写 run.sh），再用 run_analysis_node 运行。

5. Usage notes:
 - name 必填，简短英文 / 数字，同时用作容器资源名。
 - container_template_id 可选：留空时使用系统默认容器模板；给出时必须是已存在的容器模板。
 - timeout_seconds 可选：等待节点完成的超时（秒）；留空时使用服务端默认超时。
 - command 可多行（换行分隔）；路径、样本名、分组等不要硬编码。
 - 该工具会创建分析节点（写库并写出 run.sh），默认还会触发一次运行，因此声明 write 授权，
   与其它业务写入类工具共用许可策略。`
}

// CreateAgentAnalysis 返回一个「创建命令型分析节点（可选运行）」工具。
//
// 行为：
//   - 从 RuntimeContext 取当前项目 ID；
//   - 创建一个命令型 AnalysisNode（ContainerTemplateID + Command），run.sh 由 command 生成；
//   - run 为 true（默认）时复用通用节点链路提交运行并等待终态；
//     run 为 false 时只创建节点并生成 run.sh，不提交运行；
//   - 返回节点状态、是否运行、退出码与命令日志。
//
// 该工具会创建分析节点（写库并写出 run.sh），默认还会触发一次运行，因此声明 write 授权，
// 与其它业务写入类工具共用许可策略。
func CreateAgentAnalysis() tool.Tool {
	return tool.NewFunc(
		CreateAgentAnalysisToolName,
		createAgentAnalysisDescription(),
		tool.Schema("创建命令型分析节点的入参", map[string]any{
			"name":                  tool.StringProperty("节点名称（简短英文/数字，也用作容器资源名）。"),
			"container_template_id": tool.StringProperty("容器模板 ID（整数，字符串以保精度），必须是已存在的容器模板；可直接使用用户消息里的 @container:<id>（冒号后的 ID）。可选：留空时使用系统默认容器模板（is_default）。"),
			"command":               tool.StringProperty("要执行的 shell 命令（多行可用换行分隔）。命令会写入节点工作目录的 run.sh 后执行。"),
			"run":                   tool.BooleanProperty("是否在创建后立即运行，可选，默认 true。传 false 时只创建节点并生成 run.sh，不提交运行。"),
			"timeout_seconds":       tool.IntegerProperty("等待节点完成的超时（秒），可选，默认 1800；run=false 时忽略。"),
		}, "name", "command"),
		func(ctx context.Context, in CreateAgentAnalysisInput) (CreateAgentAnalysisOutput, error) {
			rc := tool.RuntimeContextFrom(ctx)
			if rc == nil || rc.ProjectID <= 0 {
				return CreateAgentAnalysisOutput{}, fmt.Errorf("create_agent_analysis: runtime context with project_id is required (no active project in this conversation)")
			}

			name := strings.TrimSpace(in.Name)
			if name == "" {
				return CreateAgentAnalysisOutput{}, fmt.Errorf("create_agent_analysis: name is required")
			}
			// container_template_id 可选：为空时解析为 0，由 runner 回退到系统默认容器模板。
			containerTemplateID, err := parseContainerTemplateID(in.ContainerTemplateID)
			if err != nil {
				return CreateAgentAnalysisOutput{}, fmt.Errorf("create_agent_analysis: %w", err)
			}
			command := strings.TrimSpace(in.Command)
			if command == "" {
				return CreateAgentAnalysisOutput{}, fmt.Errorf("create_agent_analysis: command is required")
			}

			runner := tool.AgentAnalysisRunnerFrom(ctx)
			if runner == nil {
				return CreateAgentAnalysisOutput{}, fmt.Errorf("create_agent_analysis: agent analysis runner is not available in this runtime")
			}

			// run 可选且默认为 true：nil 视为未传，按默认运行处理。
			run := true
			if in.Run != nil {
				run = *in.Run
			}

			result, runErr := runner.CreateAgentAnalysis(ctx, tool.AgentAnalysisInput{
				ProjectID:           rc.ProjectID,
				Name:                name,
				ContainerTemplateID: containerTemplateID,
				Command:             command,
				Run:                 &run,
				TimeoutSeconds:      in.TimeoutSeconds,
			})

			out := CreateAgentAnalysisOutput{
				AgentAnalysisID:     result.AgentAnalysisID,
				ContainerInstanceID: result.ContainerInstanceID,
				Status:              result.Status,
				Ran:                 result.Ran,
				Success:             result.Success,
				ExitCode:            result.ExitCode,
				Log:                 result.Log,
				LogPath:             result.LogPath,
				WorkspaceDir:        result.WorkspaceDir,
				CommandPath:         result.CommandPath,
			}
			if runErr != nil {
				out.Error = runErr.Error()
			}
			return out, nil
		},
	).WithPermissionPreview("write", "", "创建命令型分析节点（默认立即运行该节点）", previewCreateAgentAnalysis)
}

// previewCreateAgentAnalysis 生成批准前预览：新节点 run.sh 将写入的命令内容（新增文件 diff）。
func previewCreateAgentAnalysis(_ context.Context, in CreateAgentAnalysisInput) tool.PermissionPreview {
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return tool.PermissionPreview{}
	}
	return textDiffPreview("run.sh", "", runScriptContent(command))
}
