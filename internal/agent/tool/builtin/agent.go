package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/native"
	agentruntime "github.com/biox-dev/gobrave/internal/agent/runtime"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// AgentTool 返回「子 Agent」工具（agent）。
//
// 与 ask_user 一样是注册表单例：进程级依赖（子会话存储）经 Deps 注入；请求级依赖
// （LLM 客户端工厂 / Provider / Model）由 Provider 在执行工具前注入工具 ctx
// （native.WithSubAgentOptions），Runtime 与执行事实从 ctx 读取（WithToolRuntime /
// TaskFactsFrom）。每次 Execute 都按当前 ctx 组装一个 native agent 工具并委托执行，
// 因此注册表里只保留这一个无状态实例。
func AgentTool(d Deps) tool.Tool {
	return &agentTool{store: d.SubAgentSessions}
}

type agentTool struct {
	store native.SubAgentSessionStore
}

// Definition 实现 tool.Tool。
func (t *agentTool) Definition() tool.Definition { return native.AgentToolDefinition() }

// Execute 实现 tool.Tool：按执行 ctx 组装子 Agent 工具并委托执行。
//
// 子 Agent 的工具 ctx 直接复用父 ctx（Provider 的 PrepareToolContext 已注入权限 /
// facts / Runtime / RuntimeContext），因此这里不再重复装饰。
func (t *agentTool) Execute(ctx context.Context, args json.RawMessage) (any, error) {
	opts := native.SubAgentOptionsFrom(ctx)
	if opts.NewClient == nil {
		return nil, fmt.Errorf("agent tool: llm client factory is required in tool context")
	}
	rt := agentruntime.ToolRuntimeFrom(ctx)
	if rt == nil {
		return nil, fmt.Errorf("agent tool: runtime is required in tool context")
	}
	if t.store == nil {
		return nil, fmt.Errorf("agent tool: session store is not configured")
	}

	facts := tool.TaskFactsFrom(ctx)
	inner := native.NewAgentTool(native.AgentToolConfig{
		NewClient:       opts.NewClient,
		WorkingDir:      strings.TrimSpace(facts.WorkingDir),
		SystemPrompt:    native.TaskAgentSystemPrompt,
		SessionStore:    t.store,
		ParentRuntime:   rt,
		Provider:        opts.Provider,
		Model:           opts.Model,
		ParentSessionID: facts.SessionID,
		UserID:          facts.UserID,
	})
	return inner.Execute(ctx, args)
}
