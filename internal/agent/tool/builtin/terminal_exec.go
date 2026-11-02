package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// TerminalExecToolName 是「向用户的容器模板终端写入命令」的工具名。
const TerminalExecToolName = "terminal_exec"

// TerminalExecInput 是 terminal_exec 工具的入参。
//
// ProjectID / UserID 不由模型提供：它们取自执行事实（tool.ProjectIDFrom / tool.UserIDFrom）。
type TerminalExecInput struct {
	ContainerTemplateID string `json:"container_template_id"` // 容器模板 ID（支持 "@container:<id>"）
	Command             string `json:"command"`               // 要在终端里执行的 shell 命令
	TimeoutSeconds      int    `json:"timeout_seconds"`       // 等待命令结束的超时（秒），可选，默认 300
	Wait                *bool  `json:"wait"`                  // 是否等待结束，默认 true
	NotifyUI            *bool  `json:"notify_ui"`             // 是否通知前端打开终端，默认 true
	CreateIfMissing     *bool  `json:"create_if_missing"`     // 终端未打开时是否自动创建，默认 true
}

// TerminalExecOutput 是 terminal_exec 工具返回给模型的结果。
type TerminalExecOutput struct {
	ContainerTemplateID int64   `json:"container_template_id,string"`
	SessionID           string  `json:"session_id,omitempty"`
	Accepted            bool    `json:"accepted"`
	Running             bool    `json:"running,omitempty"`
	Success             bool    `json:"success"`
	ExitCode            int     `json:"exit_code"`
	TimedOut            bool    `json:"timed_out,omitempty"`
	DurationSeconds     float64 `json:"duration_seconds,omitempty"`
	Output              string  `json:"output,omitempty"`
	OutputTruncated     bool    `json:"output_truncated,omitempty"`
	Error               string  `json:"error,omitempty"`
}

// TerminalExec 返回「在用户容器模板终端里执行命令」的工具。
//
// 与 ProbeContainerEnv 的分工：
//   - probe_container_env 每次都新建一次性容器、同步执行、执行完删除：适合探测镜像环境；
//   - terminal_exec 复用用户正在使用的持久终端（同一个 PTY / 同一个容器），命令输出
//     会实时显示在前端 xterm 中，适合装包、跑长任务、排查用户现场。
//
// 该工具会向容器内写入命令，因此声明 execute 授权：与直接执行 shell 命令共用许可策略。
func TerminalExec(runner runtimecontext.ContainerTerminalRunner) tool.Tool {
	return tool.NewFunc(
		TerminalExecToolName,
		"向当前用户的容器模板终端（持久会话）写入命令并返回输出。用于在用户已经打开/曾经打开的终端容器里执行命令：命令会实时显示在前端终端中，用户能看到你执行了什么；同一个模板的多次调用复用同一个容器与 shell（会保留 cd / export 等状态）。默认会等待命令结束并返回退出码与输出尾部；设置 wait=false 只投递命令（适合长时间运行的任务，之后可用 wait=true 的空闲命令或再次调用查看结果）。若终端尚未打开，默认会自动创建（create_if_missing=false 可关闭）。与 probe_container_env 的区别：后者每次新建一次性容器、不保留状态、执行完即删除，适合探测镜像环境；需要复用用户现场或需要实时可见时用本工具。所属项目与用户取自当前会话上下文，无需传入。",
		tool.Schema("在容器模板终端中执行命令的入参", map[string]any{
			"container_template_id": tool.StringProperty("容器模板 ID（字符串以保精度），必须是已存在的模板；可直接使用用户消息里的 @container:<id>（冒号后的 ID）。"),
			"command":               tool.StringProperty("要在终端里执行的 shell 命令（可多行）。会以交互式 bash 的方式执行，保留 cd / export 等会话状态。"),
			"timeout_seconds":       tool.IntegerProperty("等待命令结束的超时（秒），可选，默认 300。超时返回 timed_out=true 与已产生的输出，命令仍在终端中继续运行。"),
			"wait":                  tool.BooleanProperty("是否等待命令结束，默认 true。false 时只投递命令并立即返回 accepted=true / running=true。"),
			"notify_ui":             tool.BooleanProperty("是否通过 realtime 通知前端打开终端标签，默认 true。"),
			"create_if_missing":     tool.BooleanProperty("终端会话不存在时是否自动创建（会创建容器），默认 true。"),
		}, "container_template_id", "command"),
		func(ctx context.Context, in TerminalExecInput) (TerminalExecOutput, error) {
			userID := tool.UserIDFrom(ctx)
			projectID := tool.ProjectIDFrom(ctx)
			if projectID <= 0 {
				return TerminalExecOutput{}, fmt.Errorf("terminal_exec: project_id is required (no active project in this conversation)")
			}

			containerTemplateID, err := parseContainerTemplateID(in.ContainerTemplateID)
			if err != nil {
				return TerminalExecOutput{}, fmt.Errorf("terminal_exec: %w", err)
			}
			if containerTemplateID <= 0 {
				return TerminalExecOutput{}, fmt.Errorf("terminal_exec: container_template_id is required")
			}
			command := strings.TrimSpace(in.Command)
			if command == "" {
				return TerminalExecOutput{}, fmt.Errorf("terminal_exec: command is required")
			}
			if runner == nil {
				return TerminalExecOutput{}, fmt.Errorf("terminal_exec: container terminal is not available in this runtime")
			}

			wait := boolValue(in.Wait, true)
			notifyUI := boolValue(in.NotifyUI, true)
			createIfMissing := boolValue(in.CreateIfMissing, true)

			result, execErr := runner.RunInTerminal(ctx, runtimecontext.ContainerTerminalExecInput{
				UserID:              userID,
				ProjectID:           projectID,
				ContainerTemplateID: containerTemplateID,
				Command:             command,
				TimeoutSeconds:      in.TimeoutSeconds,
				Wait:                wait,
				NotifyUI:            notifyUI,
				CreateIfMissing:     createIfMissing,
			})

			out := TerminalExecOutput{
				ContainerTemplateID: containerTemplateID,
				SessionID:           result.SessionID,
				Accepted:            result.Accepted,
				Running:             result.Running,
				ExitCode:            result.ExitCode,
				TimedOut:            result.TimedOut,
				DurationSeconds:     result.DurationSeconds,
				Output:              result.Output,
				OutputTruncated:     result.OutputTruncated,
			}
			if execErr != nil {
				out.Error = execErr.Error()
				return out, nil
			}
			out.Success = !result.TimedOut && result.ExitCode == 0
			if result.TimedOut {
				out.Error = "command timed out; it may still be running in the terminal"
			} else if result.ExitCode != 0 {
				out.Error = fmt.Sprintf("command exited with code %d", result.ExitCode)
			}
			return out, nil
		},
	).WithPermission("execute", "", "在用户的容器终端中执行命令并读取输出")
}

func boolValue(value *bool, fallback bool) bool {
	if value == nil {
		return fallback
	}
	return *value
}
