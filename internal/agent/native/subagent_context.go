package native

import (
	"context"

	"github.com/biox-dev/gobrave/internal/agent/llm/provider"
)

// SubAgentOptions 是注册表级 agent 工具（tool/builtin.AgentTool）执行期所需的请求级依赖。
//
// agent 工具在注册表里是进程内单例（会话存储等进程级服务经 Deps 注入），但「用哪个模型 /
// 厂商」是每次请求才确定的：Provider 在执行工具前用 WithSubAgentOptions 把它注入工具 ctx，
// 工具执行时再取出来组装 native.AgentToolConfig。
type SubAgentOptions struct {
	// NewClient 构建子 Agent 的 LLM 客户端（由 Provider 基于父请求的模型 / 厂商配置实现）。
	// 批量模式下每个子任务各调用一次；返回的错误会转成该子任务的失败结果。
	NewClient func(ctx context.Context) (provider.Client, error)
	// Provider / Model 是写入子会话的溯源信息（可为空）。
	Provider string
	Model    string
}

// subAgentOptionsKey 是 SubAgentOptionsFrom 的 ctx key。
type subAgentOptionsKey struct{}

// WithSubAgentOptions 把子 Agent 的请求级依赖注入工具执行 ctx。
func WithSubAgentOptions(ctx context.Context, opts SubAgentOptions) context.Context {
	return context.WithValue(ctx, subAgentOptionsKey{}, opts)
}

// SubAgentOptionsFrom 返回 WithSubAgentOptions 注入的选项；未注入时返回零值。
func SubAgentOptionsFrom(ctx context.Context) SubAgentOptions {
	opts, _ := ctx.Value(subAgentOptionsKey{}).(SubAgentOptions)
	return opts
}
