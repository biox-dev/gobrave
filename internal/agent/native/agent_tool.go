package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/agent/llm/provider"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/tool/coder"
)

// 本文件迁移自 opencode internal/llm/agent/agent-tool.go。
//
// opencode 的 agentTool 会创建 task 子会话并持有 session / message 服务；gobrave 的
// 原生 Agent 不绑定会话持久化（见 agent.go 的包注释），因此这里改成「同步运行一个
// 只读子 Agent」：
//
//   - 子 Agent 复用父请求的模型 / 厂商配置（由 AgentToolConfig.NewClient 构建）；
//   - 子 Agent 只拿到只读工具（glob / grep / ls / view / sourcegraph），不能改文件；
//   - 子 Agent 的流式事件写入独立 Runtime（默认丢弃），不会混入父 Agent 的时间线；
//   - 子 Agent 的最终回答作为本次工具调用的结果返回给父 Agent。

// AgentToolName 是子 Agent 工具名（与 opencode 的 AgentToolName 保持一致）。
const AgentToolName = "agent"

// TaskAgentSystemPrompt 是子 Agent（task agent）的系统提示词。
//
// 迁移自 opencode internal/llm/prompt/task.go。与 opencode 的差异：环境信息与技能
// 元信息由宿主统一维护，子 Agent 只拿到这段聚焦「检索并如实回答」的提示词。
const TaskAgentSystemPrompt = `You are an agent for GoBrave. Given the user's prompt, you should use the tools available to you to answer the user's question.
Notes:
1. IMPORTANT: You should be concise, direct, and to the point. Answer the user's question directly, without elaboration, explanation, or details. Avoid introductions, conclusions, and explanations. You MUST avoid text before/after your response, such as "The answer is <answer>.", "Here is the content of the file..." or "Based on the information provided, the answer is..." or "Here is what I will do next...".
2. When relevant, share file names and code snippets relevant to the query.
3. Any file paths you return in your final response MUST be absolute. DO NOT use relative paths.
4. You can only read files (glob / grep / ls / view); you cannot modify the workspace.`

// agentToolDescription 描述子 Agent 工具的能力与使用方式（对齐 opencode AgentTool 描述）。
const agentToolDescription = `Launch a new agent that has access to the following tools: glob, grep, ls, view, sourcegraph. When you are searching for a keyword or file and are not confident that you will find the right match on the first try, use the agent tool to perform the search for you. For example:

- If you are searching for a keyword like "config" or "logger", or for questions like "which file does X?", the agent tool is strongly recommended
- If you want to read a specific file path, use the view or glob tool instead of the agent tool, to find the match more quickly
- If you are searching for a specific class definition like "class Foo", use the glob tool instead, to find the match more quickly

Usage notes:
1. Launch multiple agents concurrently whenever possible, to maximize performance; to do that, use a single message with multiple tool uses
2. When the agent is done, it will return a single message back to you. The result returned by the agent is not visible to the user. To show the user the result, you should send a text message back to the user with a concise summary of the result.
3. Each agent invocation is stateless. You will not be able to send additional messages to the agent, nor will the agent be able to communicate with you outside of its final report. Therefore, your prompt should contain a highly detailed task description for the agent to perform autonomously and you should specify exactly what information the agent should return back to you in its final and only message to you.
4. The agent's outputs should generally be trusted
5. IMPORTANT: The agent can not use bash, edit, write or patch, so can not modify files. If you want to use these tools, use them directly instead of going through the agent.`

// AgentParams 是 agent 工具的入参。
type AgentParams struct {
	Prompt string `json:"prompt"`
}

// AgentToolConfig 是构建子 Agent 工具所需的依赖。
//
// 它替代了 opencode NewAgentTool 的 (sessions, messages, lspClients) 参数：
// gobrave 中没有会话服务，子 Agent 的模型客户端与工具上下文由 Provider 注入。
type AgentToolConfig struct {
	// NewClient 构建子 Agent 的 LLM 客户端；每次执行时调用，返回的错误会转成工具失败结果。
	//
	// 由 Provider 基于父请求的模型 / 厂商配置实现（见 providers.customAgent）。
	NewClient func(ctx context.Context) (provider.Client, error)
	// Tools 是子 Agent 可用的工具集合；为 nil 时使用 TaskAgentTools()（只读集合）。
	Tools *tool.Registry
	// PrepareToolContext 在子 Agent 每次工具执行前装饰 ctx（权限 / 运行时上下文 / 技能等）。
	//
	// 通常直接复用父 Agent 的 toolContext：子 Agent 与父 Agent 处在同一个工作目录、
	// 同一个业务上下文里，只读工具也不需要额外的授权声明。
	PrepareToolContext func(context.Context) context.Context
	// WorkingDir 是子 Agent 的工作目录；为空时沿用工具执行 ctx 中的运行时上下文。
	WorkingDir string
	// SystemPrompt 是子 Agent 的系统提示词；为空时回退到 TaskAgentSystemPrompt。
	SystemPrompt string
}

// TaskAgentTools 返回子 Agent 的只读工具集合（对齐 opencode TaskAgentTools）。
func TaskAgentTools() []tool.Tool {
	return []tool.Tool{
		coder.NewGlob(),
		coder.NewGrep(),
		coder.NewLs(),
		coder.NewView(),
		coder.NewSourcegraph(),
	}
}

// NewAgentTool 构建 agent 工具：启动一个只读子 Agent 执行 prompt，并返回其最终回答。
func NewAgentTool(cfg AgentToolConfig) tool.Tool {
	return &agentTool{cfg: cfg}
}

type agentTool struct {
	cfg AgentToolConfig
}

// Definition 实现 tool.Tool。
func (t *agentTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        AgentToolName,
		Description: agentToolDescription,
		InputSchema: tool.Schema("agent 工具入参", map[string]any{
			"prompt": tool.StringProperty("The task for the agent to perform"),
		}, "prompt"),
	}
}

// Execute 实现 tool.Tool：构建子 Agent → 同步运行 → 返回最终回答。
func (t *agentTool) Execute(ctx context.Context, args json.RawMessage) (any, error) {
	var params AgentParams
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("error parsing parameters: %s", err)
	}
	prompt := strings.TrimSpace(params.Prompt)
	if prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}
	if t.cfg.NewClient == nil {
		return nil, fmt.Errorf("agent tool: client factory is required")
	}

	client, err := t.cfg.NewClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("error creating agent: %s", err)
	}

	systemPrompt := strings.TrimSpace(t.cfg.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = TaskAgentSystemPrompt
	}

	sub := &Config{
		Client:             client,
		Tools:              t.tools(),
		PrepareToolContext: t.cfg.PrepareToolContext,
	}
	// 子 Agent 使用独立的 Runtime：默认丢弃流式事件，避免子 Agent 的增量 / 工具调用
	// 混入父 Agent 的时间线；无 UI 场景下权限策略走默认策略。
	result, err := sub.Run(ctx, agent.Request{
		Messages:     []agent.Message{{Role: agent.RoleUser, Content: prompt}},
		SystemPrompt: systemPrompt,
		WorkingDir:   t.cfg.WorkingDir,
	}, agent.NewStandaloneRuntime(nil))
	if err != nil {
		return nil, fmt.Errorf("error generating agent: %s", err)
	}
	if result == nil || strings.TrimSpace(result.Content) == "" {
		return nil, fmt.Errorf("no response from agent")
	}
	return result.Content, nil
}

// tools 返回子 Agent 的工具注册表。
func (t *agentTool) tools() *tool.Registry {
	if t.cfg.Tools != nil {
		return t.cfg.Tools
	}
	return tool.NewRegistryWith(TaskAgentTools()...)
}
