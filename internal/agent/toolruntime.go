package agent

import (
	"context"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// ToolRuntimeContextFromRequest 取出一次调用的运行时上下文，用于注入工具执行上下文。
//
// 运行时上下文由 HTTP Handler 的 RuntimeContextResolver 解析 env(type,id) 产出并放入
// Request.RuntimeContext；Provider 在每次调用时把它注入工具执行 ctx，供 builtin 工具
// （如 run_analysis_node）在执行期读取 SystemPromptVars。
//
// 返回 nil 表示本轮没有运行时上下文（普通调用 / 无业务 env）。
func ToolRuntimeContextFromRequest(req Request) *tool.RuntimeContext {
	return req.RuntimeContext
}

// WithToolRuntimeContext 把运行时上下文注入工具执行 ctx；rc 为 nil 时原样返回 ctx。
func WithToolRuntimeContext(ctx context.Context, rc *tool.RuntimeContext) context.Context {
	if rc == nil {
		return ctx
	}
	return tool.WithRuntimeContext(ctx, rc)
}

// WithToolRuntime 把「运行时上下文（业务事实）+ 进程级服务能力」一次性注入工具执行 ctx。
//
// 二者来源与生命周期不同：RuntimeContext 来自请求（可序列化），服务能力（AnalysisNodeRunner /
// FileReader / ScriptCreator / AnalysisNodeParamsBuilder）来自 Options（不可序列化、进程级复用），
// 因此分开注入、互不影响。任一为 nil 时跳过对应注入。
func WithToolRuntime(ctx context.Context, rc *tool.RuntimeContext, runner tool.AnalysisNodeRunner, reader tool.FileReader, creator tool.ScriptCreator, paramsBuilder tool.AnalysisNodeParamsBuilder) context.Context {
	ctx = WithToolRuntimeContext(ctx, rc)
	ctx = tool.WithAnalysisNodeRunner(ctx, runner)
	ctx = tool.WithFileReader(ctx, reader)
	ctx = tool.WithScriptCreator(ctx, creator)
	return tool.WithAnalysisNodeParamsBuilder(ctx, paramsBuilder)
}
