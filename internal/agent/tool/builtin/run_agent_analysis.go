package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// RunAgentAnalysisToolName 是「创建并运行 AgentAnalysis 容器任务」的工具名。
const RunAgentAnalysisToolName = "run_agent_analysis"

// RunAgentAnalysisInput 是 run_agent_analysis 工具的入参。
//
// ProjectID 不由模型提供：它取自 RuntimeContext.ProjectID（当前用户激活项目），
// 避免把任务写到别的项目下。
type RunAgentAnalysisInput struct {
	Name string `json:"name"` // 任务名称（也用作容器资源名）
	// TemplateID 可选：为空时使用系统默认容器模板（is_default）。
	TemplateID     string `json:"template_id"`     // 容器模板 ID（字符串以保精度）
	Command        string `json:"command"`         // 要执行的 shell 命令
	WorkspaceDir   string `json:"workspace_dir"`   // 工作目录（可选，为空时使用默认目录）
	TimeoutSeconds int    `json:"timeout_seconds"` // 等待完成的超时（秒），可选
}

// RunAgentAnalysisOutput 是 run_agent_analysis 工具返回给模型的结果。
type RunAgentAnalysisOutput struct {
	AgentAnalysisID     int64  `json:"agent_analysis_id,string"`
	ContainerInstanceID int64  `json:"container_instance_id,string,omitempty"`
	Status              string `json:"status"`
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

// RunAgentAnalysis 返回一个「创建并运行 AgentAnalysis 容器任务」工具。
//
// 行为：
//   - 从 RuntimeContext 取当前项目 ID；
//   - 在 <projectDir>/agent-analysis/<id> 创建工作目录并写入 run.sh（内容为 command）；
//   - 调用 CreateByTemplate 入队容器创建（OwnerType=agent_analysis），等待容器进入终态；
//   - 返回任务状态、退出码与命令日志。
//
// 该工具会执行任意 shell 命令，因此声明 execute 授权，与其它命令执行类工具共用许可策略。
func RunAgentAnalysis() tool.Tool {
	return tool.NewFunc(
		RunAgentAnalysisToolName,
		"创建并运行一个一次性的容器任务（AgentAnalysis）：给定任务名称、一段 shell 命令（容器模板 ID 可选，留空时使用系统默认模板），系统在项目目录下生成独立工作目录与 run.sh，提交容器执行并等待结束，返回状态、退出码与命令日志。所属项目取自当前会话上下文，无需传入。",
		tool.Schema("创建 AgentAnalysis 容器任务的入参", map[string]any{
			"name":            tool.StringProperty("任务名称（简短英文/数字，也用作容器资源名）。"),
			"template_id":     tool.StringProperty("容器模板 ID（整数，字符串以保精度），必须是已存在的容器模板。可选：留空时使用系统默认容器模板（is_default）。"),
			"command":         tool.StringProperty("要执行的 shell 命令（多行可用换行分隔）。命令会写入工作目录的 run.sh 后执行，工作目录即项目目录下的 agent-analysis/<任务 ID>。"),
			"workspace_dir":   tool.StringProperty("工作目录（可选）。为空时使用默认目录 <projectDir>/agent-analysis/<任务 ID>；非空时必须是 storage.base_dir 下的路径。"),
			"timeout_seconds": tool.IntegerProperty("等待任务完成的超时（秒），可选，默认 1800。"),
		}, "name", "command"),
		func(ctx context.Context, in RunAgentAnalysisInput) (RunAgentAnalysisOutput, error) {
			rc := tool.RuntimeContextFrom(ctx)
			if rc == nil || rc.ProjectID <= 0 {
				return RunAgentAnalysisOutput{}, fmt.Errorf("run_agent_analysis: runtime context with project_id is required (no active project in this conversation)")
			}

			name := strings.TrimSpace(in.Name)
			if name == "" {
				return RunAgentAnalysisOutput{}, fmt.Errorf("run_agent_analysis: name is required")
			}
			// template_id 可选：为空时解析为 0，由 runner 回退到系统默认容器模板。
			templateID, err := parseContainerTemplateID(in.TemplateID)
			if err != nil {
				return RunAgentAnalysisOutput{}, fmt.Errorf("run_agent_analysis: %w", err)
			}
			command := strings.TrimSpace(in.Command)
			if command == "" {
				return RunAgentAnalysisOutput{}, fmt.Errorf("run_agent_analysis: command is required")
			}

			runner := tool.AgentAnalysisRunnerFrom(ctx)
			if runner == nil {
				return RunAgentAnalysisOutput{}, fmt.Errorf("run_agent_analysis: agent analysis runner is not available in this runtime")
			}

			result, runErr := runner.RunAgentAnalysis(ctx, tool.AgentAnalysisInput{
				ProjectID:      rc.ProjectID,
				Name:           name,
				TemplateID:     templateID,
				Command:        command,
				WorkspaceDir:   strings.TrimSpace(in.WorkspaceDir),
				TimeoutSeconds: in.TimeoutSeconds,
			})

			out := RunAgentAnalysisOutput{
				AgentAnalysisID:     result.AgentAnalysisID,
				ContainerInstanceID: result.ContainerInstanceID,
				Status:              result.Status,
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
	).WithPermission("execute", "", "创建并运行 AgentAnalysis 容器任务（执行 shell 命令）")
}
