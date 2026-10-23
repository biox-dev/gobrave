package agentruntime

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/lsp"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
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

// WithToolFacts 把 Runtime 绑定的执行事实注入工具 ctx。
//
// 工具通过 tool.UserIDFrom / SessionIDFrom / ProjectIDFrom 读取，因此这些事实
// 只需要在 Provider 组装工具 ctx 时注入一次，不必逐层传参。
func WithToolFacts(ctx context.Context, rt Runtime) context.Context {
	return tool.WithTaskFacts(ctx, FactsOf(rt))
}

// WithToolPermission 把工具执行期的授权请求桥接到 Agent Runtime：
//
//	tool.PermissionRequest → agent.Operation → Runtime.RequestPermission
//
// 工具在真正操作文件 / 执行命令前自行调用 tool.RequirePermission，Provider 只需
// 在每次执行工具前注入本函数。rt 为 nil 时原样返回 ctx（工具侧视为免授权）。
func WithToolPermission(ctx context.Context, rt Runtime) context.Context {
	if rt == nil {
		return ctx
	}
	return tool.WithPermissionRequester(ctx, toolPermissionRequester{rt: rt})
}

type toolPermissionRequester struct {
	rt Runtime
}

// RequestPermission 实现 tool.PermissionRequester：把工具授权请求映射为 Operation
// 并交由 Runtime 决策；出错一律按拒绝处理（安全默认）。
func (r toolPermissionRequester) RequestPermission(ctx context.Context, req tool.PermissionRequest) bool {
	decision, err := r.rt.RequestPermission(ctx, operationFromToolPermission(ctx, req))
	return err == nil && decision == DecisionAllow
}

// FactsOf 返回 Runtime 绑定的任务事实快照；Runtime 未实现 TaskAware 时返回零值。
func FactsOf(rt Runtime) TaskFacts {
	if aware, ok := rt.(TaskAware); ok {
		return aware.Facts()
	}
	return TaskFacts{}
}

// operationFromToolPermission 把工具授权请求映射为框架内的 Operation 描述。
//
// Description 走 Operation 的一等字段（而非 Metadata），便于 UI 统一展示“这个操作
// 到底要做什么”；diff / file_path 仍放在 Metadata（仅写文件时才有，供批准前查看改动）。
func operationFromToolPermission(ctx context.Context, req tool.PermissionRequest) Operation {
	meta := map[string]any{"tool": req.ToolName}
	// 记录发起权限请求的工具调用 ID，供后端重启后的精确重放（见 PermissionManager.FindByToolCall）。
	if callID := ToolCallIDFromContext(ctx); callID != "" {
		meta[OperationMetadataToolCallID] = callID
	}
	diff, filePath := diffFromParams(req.Params)
	if diff != "" {
		meta["diff"] = diff
	}
	if filePath != "" {
		meta["file_path"] = filePath
	}

	path := strings.TrimSpace(req.Path)
	if path == "" {
		// 部分工具把路径放在 params 里（Path 为空），从 diff 参数中回填。
		path = filePath
	}
	desc := strings.TrimSpace(req.Description)

	switch strings.ToLower(strings.TrimSpace(req.Action)) {
	case "execute":
		return Operation{
			Type:        OperationExecute,
			Path:        path,
			Command:     commandFromParams(req.Params, desc),
			Description: desc,
			Metadata:    meta,
		}
	case "write", "create", "update", "edit":
		return Operation{Type: OperationWrite, Path: path, Description: desc, Metadata: meta}
	case "delete":
		return Operation{Type: OperationDelete, Path: path, Description: desc, Metadata: meta}
	case "read":
		return Operation{Type: OperationRead, Path: path, Description: desc, Metadata: meta}
	case "fetch", "network":
		return Operation{Type: OperationNetwork, Path: path, Description: desc, Metadata: meta}
	case "analysis_node", "analysisnode":
		return Operation{Type: OperationAnalysisNode, Path: path, Description: desc, Metadata: meta}
	default:
		return Operation{Type: OperationExecute, Path: path, Description: desc, Metadata: meta}
	}
}

// commandFromParams 尽力从授权参数中抽取命令文本，失败时回退到描述。
func commandFromParams(params any, fallback string) string {
	if data, err := json.Marshal(params); err == nil {
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			if cmd, ok := m["command"].(string); ok && cmd != "" {
				return cmd
			}
		}
	}
	return fallback
}

// diffFromParams 尽力从授权参数中抽取 file_path / diff（edit / write / patch 工具）。
func diffFromParams(params any) (diff, filePath string) {
	data, err := json.Marshal(params)
	if err != nil {
		return "", ""
	}
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		return "", ""
	}
	if d, ok := m["diff"].(string); ok {
		diff = d
	}
	if p, ok := m["file_path"].(string); ok {
		filePath = p
	}
	return diff, filePath
}

// WithToolRuntimeContext 把运行时上下文注入工具执行 ctx；rc 为 nil 时原样返回 ctx。
func WithToolRuntimeContext(ctx context.Context, rc *tool.RuntimeContext) context.Context {
	if rc == nil {
		return ctx
	}
	return tool.WithRuntimeContext(ctx, rc)
}

// WithToolRuntime 把「业务上下文（RuntimeContext）+ 进程级服务能力」一次性注入工具执行 ctx。
//
// 二者来源与生命周期不同：RuntimeContext 来自请求（可序列化），服务能力（AnalysisNodeRunner /
// FileReader / ScriptCreator / AnalysisNodeCreator / AnalysisNodeParamsBuilder / AgentEnvUpdater / MemorySaver / FileHistory / Skills）来自 Options
// （不可序列化、进程级复用），因此分开注入、互不影响。任一为 nil 时跳过对应注入。
//
// 执行事实（用户 / 会话 / 项目）不在 RuntimeContext 里：它们由 TaskFacts 承载，
// 经 WithToolFacts 注入，工具通过 tool.UserIDFrom / SessionIDFrom / ProjectIDFrom 读取。
func WithToolRuntime(ctx context.Context, rc *tool.RuntimeContext, runner tool.AnalysisNodeRunner, reader tool.FileReader, creator tool.ScriptCreator, nodeCreator tool.AnalysisNodeCreator, paramsBuilder tool.AnalysisNodeParamsBuilder, nodeUpdater tool.AnalysisNodeUpdater, envUpdater tool.AgentEnvUpdater, memorySaver tool.MemorySaver, files HistoryService) context.Context {
	ctx = WithToolRuntimeContext(ctx, rc)
	ctx = tool.WithAnalysisNodeRunner(ctx, runner)
	ctx = tool.WithFileReader(ctx, reader)
	ctx = tool.WithScriptCreator(ctx, creator)
	ctx = tool.WithAnalysisNodeCreator(ctx, nodeCreator)
	ctx = tool.WithScriptUpdater(ctx, scriptUpdaterFromCreator(creator))
	ctx = tool.WithAnalysisNodeParamsBuilder(ctx, paramsBuilder)
	ctx = tool.WithAnalysisNodeUpdater(ctx, nodeUpdater)
	ctx = tool.WithAgentEnvUpdater(ctx, envUpdater)
	ctx = tool.WithMemorySaver(ctx, memorySaver)
	return tool.WithFileHistory(ctx, files)
}

// WithAgentAnalysisRunner 把「创建并等待 AgentAnalysis 容器任务」的服务能力注入工具执行 ctx。
//
// runner 为 nil 时原样返回 ctx。
func WithAgentAnalysisRunner(ctx context.Context, runner tool.AgentAnalysisRunner) context.Context {
	return tool.WithAgentAnalysisRunner(ctx, runner)
}

// WithLSPClients 把 LSP 客户端集合注入工具执行 ctx，供 diagnostics / view / edit / write / patch 使用。
//
// clients 来自 lsp/manager.Manager.Clients()；为空时原样返回 ctx。
func WithLSPClients(ctx context.Context, clients map[string]*lsp.Client) context.Context {
	return tool.WithLSPClients(ctx, clients)
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
