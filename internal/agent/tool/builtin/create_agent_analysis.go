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

// CreateAgentAnalysis 返回一个「创建命令型分析节点（可选运行）」工具。
//
// 行为：
//   - 从 RuntimeContext 取当前项目 ID；
//   - 创建一个命令型 AnalysisNode（ContainerTemplateID + Command），run.sh 由 command 生成；
//   - run 为 true（默认）时复用通用节点链路提交运行并等待终态；
//     run 为 false 时只创建节点并生成 run.sh，不提交运行；
//   - 返回节点状态、是否运行、退出码与命令日志。
//
// 该工具会执行任意 shell 命令（默认即运行），因此声明 execute 授权，与其它命令执行类工具共用许可策略。
func CreateAgentAnalysis() tool.Tool {
	return tool.NewFunc(
		CreateAgentAnalysisToolName,
		"创建一个命令型分析节点：给定节点名称、一段 shell 命令（container_template_id 可选，留空时使用系统默认模板），系统在分析节点目录下生成 run.sh。run 可选，默认 true：为 true 时立即提交容器执行并等待结束，返回状态、退出码与命令日志；为 false 时只创建节点不运行，之后可用 run_analysis_node 再运行该节点。所属项目取自当前会话上下文，无需传入。",
		tool.Schema("创建命令型分析节点的入参", map[string]any{
			"name":                  tool.StringProperty("节点名称（简短英文/数字，也用作容器资源名）。"),
			"container_template_id": tool.StringProperty("容器模板 ID（整数，字符串以保精度），必须是已存在的容器模板。可选：留空时使用系统默认容器模板（is_default）。"),
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
	).WithPermission("execute", "", "创建命令型分析节点（默认执行 shell 命令）")
}
