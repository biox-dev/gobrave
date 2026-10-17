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

// withRequestFacts 在请求级上下文上补齐「用户 + 会话」事实，返回新的 RuntimeContext。
//
// 用户 ID 与 SessionID 不属于某个业务 env，而是「一次调用」的属性，因此在组装 Request
// 时统一写入 RuntimeContext（它是请求级事实的唯一载体，见 Request.RequestUserID）。
func withRequestFacts(rc *tool.RuntimeContext, userID int64, sessionID int64) *tool.RuntimeContext {
	return tool.WithUserIDForContext(rc, userID, sessionID)
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
// FileReader / ScriptCreator / AnalysisNodeParamsBuilder / AgentEnvUpdater / MemorySaver / Skills）来自 Options
// （不可序列化、进程级复用），因此分开注入、互不影响。任一为 nil 时跳过对应注入。
//
// 归属用户不再作为参数单独注入：RuntimeContext.UserID 就是请求级用户事实的唯一来源，
// 工具通过 tool.RuntimeContextFrom(ctx).UserID 读取（见 save_memory）。
func WithToolRuntime(ctx context.Context, rc *tool.RuntimeContext, runner tool.AnalysisNodeRunner, reader tool.FileReader, creator tool.ScriptCreator, paramsBuilder tool.AnalysisNodeParamsBuilder, nodeUpdater tool.AnalysisNodeUpdater, envUpdater tool.AgentEnvUpdater, memorySaver tool.MemorySaver) context.Context {
	ctx = WithToolRuntimeContext(ctx, rc)
	ctx = tool.WithAnalysisNodeRunner(ctx, runner)
	ctx = tool.WithFileReader(ctx, reader)
	ctx = tool.WithScriptCreator(ctx, creator)
	ctx = tool.WithScriptUpdater(ctx, scriptUpdaterFromCreator(creator))
	ctx = tool.WithAnalysisNodeParamsBuilder(ctx, paramsBuilder)
	ctx = tool.WithAnalysisNodeUpdater(ctx, nodeUpdater)
	ctx = tool.WithAgentEnvUpdater(ctx, envUpdater)
	return tool.WithMemorySaver(ctx, memorySaver)
}

// WithSkills 把技能解析能力注入工具执行 ctx，供 get_skill / load_skill_resource 工具使用。
//
// skills 为 nil 时原样返回 ctx。
func WithSkills(ctx context.Context, skills tool.SkillResolver) context.Context {
	return tool.WithSkillResolver(ctx, skills)
}

// WithProjectResourceBrowser 把「浏览项目内脚本 / 分析节点」的服务能力注入工具执行 ctx。
//
// 该能力只被 list_project_scripts / get_script / get_analysis_node 等只读浏览工具使用，
// 由 Provider 在执行工具前调用（见 buildHostTools）；browser 为 nil 时原样返回 ctx。
func WithProjectResourceBrowser(ctx context.Context, browser tool.ProjectResourceBrowser) context.Context {
	return tool.WithProjectResourceBrowser(ctx, browser)
}

// scriptUpdaterFromCreator 从创建器里取出「更新脚本基本信息」能力。
//
// 同一份脚本业务能力（container.scriptCreator）同时实现创建与更新，Options 只需暴露
// ScriptCreator 一个字段；未实现 ScriptUpdater 时返回 nil（工具执行期会报能力缺失）。
func scriptUpdaterFromCreator(creator tool.ScriptCreator) tool.ScriptUpdater {
	updater, _ := creator.(tool.ScriptUpdater)
	return updater
}
