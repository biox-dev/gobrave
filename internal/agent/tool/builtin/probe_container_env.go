package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// ProbeContainerEnvToolName 是「在临时容器中同步执行命令」的工具名。
const ProbeContainerEnvToolName = "probe_container_env"

// ProbeContainerEnvInput 是 probe_container_env 工具的入参。
//
// ProjectID 不由模型提供：它取自执行事实（tool.ProjectIDFrom，当前用户激活项目）。
type ProbeContainerEnvInput struct {
	ContainerTemplateID string `json:"container_template_id"` // 容器模板 ID（支持 "@container:<id>"）
	Command             string `json:"command"`               // 要在容器内执行的 shell 命令
	TimeoutSeconds      int    `json:"timeout_seconds"`       // 等待命令结束的超时（秒），可选，默认 300
}

// ProbeContainerEnvOutput 是 probe_container_env 工具返回给模型的结果。
type ProbeContainerEnvOutput struct {
	ContainerTemplateID int64   `json:"container_template_id,string"`
	RuntimeName         string  `json:"runtime_name,omitempty"`
	Success             bool    `json:"success"`
	ExitCode            int     `json:"exit_code"`
	TimedOut            bool    `json:"timed_out,omitempty"`
	DurationSeconds     float64 `json:"duration_seconds,omitempty"`
	Log                 string  `json:"log,omitempty"`
	LogTruncated        bool    `json:"log_truncated,omitempty"`
	Error               string  `json:"error,omitempty"`
}

// ProbeContainerEnv 返回一个「临时容器探测」工具。
//
// 行为：
//   - 按 container_template_id 读取容器模板（镜像 / 资源 / 挂载 / 环境变量）；
//   - 用与 executeCreate 相同的变量链路补齐运行时变量（含 buildPackageVariables 的
//     R_PACKAGE_DIR / PYTHON_PACKAGE_DIR / CONDA_PACKAGE_DIR 等），并解析 env / mounts
//     中的占位符；
//   - 创建一次性容器（k8s 为 job），执行 command 并同步等待结束；
//   - 返回 stdout/stderr 日志、退出码与是否超时；
//   - 无论成功、失败还是超时，都会自动删除容器。
//
// 该工具会创建并运行容器，因此声明 execute 授权：与直接执行 shell 命令共用许可策略。
func ProbeContainerEnv(probe runtimecontext.ContainerEnvProbe) tool.Tool {
	return tool.NewFunc(
		ProbeContainerEnvToolName,
		"在临时容器中同步执行命令并返回输出。用于探测容器模板镜像里的运行环境（R/Python 版本、可执行文件、包目录等）：按 container_template_id 使用模板的镜像 / 资源 / 挂载 / 环境变量创建一次性容器，命令执行结束后自动删除容器，不留残余。命令通过 bash -lc 执行，可用 $R_PACKAGE_DIR、$PYTHON_PACKAGE_DIR、$CONDA_PACKAGE_DIR、$PROJECT_DIR 等运行时变量；默认工作目录是当前项目目录。只返回 stdout/stderr 日志与退出码，不写数据库、不创建分析节点。命令失败或超时不会中断工具调用，先看 log 定位原因再调整命令。所属项目取自当前会话上下文，无需传入。",
		tool.Schema("在临时容器中执行命令的入参", map[string]any{
			"container_template_id": tool.StringProperty("容器模板 ID（字符串以保精度），必须是已存在的模板；可直接使用用户消息里的 @container:<id>（冒号后的 ID）。"),
			"command":               tool.StringProperty("要在容器内执行的 shell 命令（可多行）。通过 bash -lc 执行，适合探测 which / --version / 目录内容等。"),
			"timeout_seconds":       tool.IntegerProperty("等待命令结束的超时（秒），可选，默认 300。超时后容器仍会被删除，返回 timed_out=true 与已产生的日志。"),
		}, "container_template_id", "command"),
		func(ctx context.Context, in ProbeContainerEnvInput) (ProbeContainerEnvOutput, error) {
			projectID := tool.ProjectIDFrom(ctx)
			if projectID <= 0 {
				return ProbeContainerEnvOutput{}, fmt.Errorf("probe_container_env: project_id is required (no active project in this conversation)")
			}

			containerTemplateID, err := parseContainerTemplateID(in.ContainerTemplateID)
			if err != nil {
				return ProbeContainerEnvOutput{}, fmt.Errorf("probe_container_env: %w", err)
			}
			if containerTemplateID <= 0 {
				return ProbeContainerEnvOutput{}, fmt.Errorf("probe_container_env: container_template_id is required")
			}
			command := strings.TrimSpace(in.Command)
			if command == "" {
				return ProbeContainerEnvOutput{}, fmt.Errorf("probe_container_env: command is required")
			}
			if probe == nil {
				return ProbeContainerEnvOutput{}, fmt.Errorf("probe_container_env: container probe is not available in this runtime")
			}

			result, probeErr := probe.ProbeContainerEnv(ctx, runtimecontext.ContainerEnvProbeInput{
				ProjectID:           projectID,
				ContainerTemplateID: containerTemplateID,
				Command:             command,
				TimeoutSeconds:      in.TimeoutSeconds,
			})

			out := ProbeContainerEnvOutput{
				ContainerTemplateID: containerTemplateID,
				RuntimeName:         result.RuntimeName,
				Success:             result.Success,
				ExitCode:            result.ExitCode,
				TimedOut:            result.TimedOut,
				DurationSeconds:     result.DurationSeconds,
				Log:                 result.Log,
				LogTruncated:        result.LogTruncated,
				Error:               result.Error,
			}
			if probeErr != nil {
				out.Error = probeErr.Error()
			}
			return out, nil
		},
	).WithPermission("execute", "", "在临时容器中执行命令并读取输出")
}
