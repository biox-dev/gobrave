package agentruntime

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
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
func ToolRuntimeContextFromRequest(req Request) *runtimecontext.RuntimeContext {
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

// toolRuntimeKey 是 ToolRuntimeFrom 的 ctx key。
type toolRuntimeKey struct{}

// WithToolRuntime 把 Runtime 本身注入工具执行 ctx。
//
// 供注册表级（进程内单例）工具在执行期取回 Runtime 的阻塞交互能力，典型场景是
// ask_user：工具在容器装配时只注册一次，执行时再从 ctx 拿到本轮任务的 Runtime
// 阻塞等待用户作答。rt 为 nil 时原样返回 ctx。
func WithToolRuntime(ctx context.Context, rt Runtime) context.Context {
	if rt == nil {
		return ctx
	}
	return context.WithValue(ctx, toolRuntimeKey{}, rt)
}

// ToolRuntimeFrom 返回 WithToolRuntime 注入的 Runtime；未注入时返回 nil。
func ToolRuntimeFrom(ctx context.Context) Runtime {
	rt, _ := ctx.Value(toolRuntimeKey{}).(Runtime)
	return rt
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
func WithToolRuntimeContext(ctx context.Context, rc *runtimecontext.RuntimeContext) context.Context {
	if rc == nil {
		return ctx
	}
	return runtimecontext.WithRuntimeContext(ctx, rc)
}
