package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

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
const agentToolDescription = `Launch one or more sub-agents that have access to the following tools: glob, grep, ls, view, sourcegraph. When you are searching for a keyword or file and are not confident that you will find the right match on the first try, use the agent tool to perform the search for you. For example:

- If you are searching for a keyword like "config" or "logger", or for questions like "which file does X?", the agent tool is strongly recommended
- If you want to read a specific file path, use the view or glob tool instead of the agent tool, to find the match more quickly
- If you are searching for a specific class definition like "class Foo", use the glob tool instead, to find the match more quickly

Usage notes:
1. To run several independent searches in parallel, call the agent tool ONCE with a "prompts" array (each item is {"name"?: string, "prompt": string}). All sub-agents run concurrently and their answers are merged, in the requested order, into a single tool result.
2. You may also emit multiple agent tool calls in the same assistant message: agent calls in the same turn are executed concurrently (other tools in that message still run one after another).
3. "max_concurrency" (default 4, max 8) caps how many sub-agents run at the same time in batch mode.
4. Each agent invocation is stateless and returns exactly one final message. The result is not visible to the user; summarize it in your own reply.
5. Your prompt must be a highly detailed, self-contained task description: the sub-agent cannot ask follow-up questions and cannot see this conversation. Specify exactly what information it must return.
6. The agent can only read files (glob / grep / ls / view / sourcegraph); it cannot use bash, edit, write or patch. Use those tools yourself when you need to modify files.
7. The agent's outputs should generally be trusted.`

// AgentParams 是 agent 工具的入参。
//
// 单任务用 prompt；多任务（并发）用 prompts。两者只能给一个。
type AgentParams struct {
	Prompt         string            `json:"prompt,omitempty"`
	Prompts        []AgentTaskParams `json:"prompts,omitempty"`
	MaxConcurrency int               `json:"max_concurrency,omitempty"`
}

// AgentTaskParams 是并发模式下的一个子任务。
type AgentTaskParams struct {
	// Name 是子任务名称，用于在汇总结果里标识来源；为空时自动编号。
	Name string `json:"name,omitempty"`
	// Prompt 是该子 Agent 的任务描述。
	Prompt string `json:"prompt"`
}

const (
	// defaultAgentConcurrency 是批量模式下默认的并发子 Agent 数。
	defaultAgentConcurrency = 4
	// MaxAgentConcurrency 是批量模式允许的最大并发子 Agent 数。
	MaxAgentConcurrency = 8
)

// AgentToolConfig 是构建子 Agent 工具所需的依赖。
//
// 它替代了 opencode NewAgentTool 的 (sessions, messages, lspClients) 参数：
// gobrave 中没有会话服务，子 Agent 的模型客户端与工具上下文由 Provider 注入。
type AgentToolConfig struct {
	// NewClient 构建子 Agent 的 LLM 客户端；每次执行时调用（批量模式下每个子任务各一次），
	// 返回的错误会转成该子任务的失败结果。
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
	// MaxConcurrency 是批量模式下默认的并发子 Agent 数；<=0 时使用 defaultAgentConcurrency，
	// 超过 MaxAgentConcurrency 时按上限截断。
	MaxConcurrency int
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
		InputSchema: tool.Schema("agent 工具入参（prompt 与 prompts 二选一）", map[string]any{
			"prompt": tool.StringProperty("The task for a single agent to perform"),
			"prompts": map[string]any{
				"type":        "array",
				"description": "Multiple tasks to run concurrently; their answers are merged into one tool result",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":   tool.StringProperty("Short label for this sub-task (used in the merged result)"),
						"prompt": tool.StringProperty("The task for this agent to perform"),
					},
					"required": []string{"prompt"},
				},
			},
			"max_concurrency": tool.IntegerProperty("Max sub-agents running at the same time in batch mode (default 4, max 8)"),
		}),
	}
}

// Execute 实现 tool.Tool：单任务直接返回；多任务并发执行并按输入顺序汇总。
func (t *agentTool) Execute(ctx context.Context, args json.RawMessage) (any, error) {
	var params AgentParams
	if err := json.Unmarshal(args, &params); err != nil {
		return nil, fmt.Errorf("error parsing parameters: %s", err)
	}
	tasks, err := params.tasks()
	if err != nil {
		return nil, err
	}
	if t.cfg.NewClient == nil {
		return nil, fmt.Errorf("agent tool: client factory is required")
	}

	// 单任务：保持与 opencode 一致的语义——失败直接作为工具错误返回。
	if len(tasks) == 1 {
		result := t.runTask(ctx, tasks[0])
		if result.err != nil {
			return nil, result.err
		}
		return result.content, nil
	}
	// 多任务：并发执行；单个子任务失败不阻塞其它子 Agent，结果按输入顺序汇总。
	return t.runBatch(ctx, tasks, params.MaxConcurrency), nil
}

// tasks 把入参归一化为子任务列表（prompt 与 prompts 二选一）。
func (p AgentParams) tasks() ([]AgentTaskParams, error) {
	tasks := make([]AgentTaskParams, 0, len(p.Prompts)+1)
	for i, item := range p.Prompts {
		prompt := strings.TrimSpace(item.Prompt)
		if prompt == "" {
			return nil, fmt.Errorf("prompts[%d].prompt is required", i)
		}
		tasks = append(tasks, AgentTaskParams{Name: strings.TrimSpace(item.Name), Prompt: prompt})
	}
	if prompt := strings.TrimSpace(p.Prompt); prompt != "" {
		if len(tasks) > 0 {
			return nil, fmt.Errorf("provide either prompt or prompts, not both")
		}
		tasks = append(tasks, AgentTaskParams{Prompt: prompt})
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("prompt or prompts is required")
	}
	return tasks, nil
}

// agentRunResult 是单个子任务的执行结果。
type agentRunResult struct {
	name    string
	content string
	err     error
}

// runBatch 并发执行多个子任务，并按输入顺序返回汇总文本。
//
// 单个子任务失败只会在汇总里标记 [error]，不会中断其它子 Agent。
func (t *agentTool) runBatch(ctx context.Context, tasks []AgentTaskParams, concurrency int) string {
	limit := t.concurrency(concurrency)
	results := make([]agentRunResult, len(tasks))
	sem := make(chan struct{}, limit)

	var wg sync.WaitGroup
	for i := range tasks {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = agentRunResult{name: tasks[i].Name, err: ctx.Err()}
				return
			}
			results[i] = t.runTask(ctx, tasks[i])
		}(i)
	}
	wg.Wait()

	return formatAgentBatch(results)
}

// runTask 构建并运行一个子 Agent，返回其最终回答。
func (t *agentTool) runTask(ctx context.Context, task AgentTaskParams) agentRunResult {
	result := agentRunResult{name: task.Name}

	client, err := t.cfg.NewClient(ctx)
	if err != nil {
		result.err = fmt.Errorf("error creating agent: %s", err)
		return result
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
	answer, err := sub.Run(ctx, agent.Request{
		Messages:     []agent.Message{{Role: agent.RoleUser, Content: task.Prompt}},
		SystemPrompt: systemPrompt,
		WorkingDir:   t.cfg.WorkingDir,
	}, agent.NewStandaloneRuntime(nil))
	if err != nil {
		result.err = fmt.Errorf("error generating agent: %s", err)
		return result
	}
	if answer == nil || strings.TrimSpace(answer.Content) == "" {
		result.err = fmt.Errorf("no response from agent")
		return result
	}
	result.content = answer.Content
	return result
}

// concurrency 解析本次批量执行的并发上限：入参 > 配置 > 默认值，并截断到上限。
func (t *agentTool) concurrency(override int) int {
	limit := t.cfg.MaxConcurrency
	if override > 0 {
		limit = override
	}
	if limit <= 0 {
		limit = defaultAgentConcurrency
	}
	if limit > MaxAgentConcurrency {
		limit = MaxAgentConcurrency
	}
	return limit
}

// formatAgentBatch 把并发结果按输入顺序汇总为一段 Markdown 文本。
func formatAgentBatch(results []agentRunResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ran %d sub-agents concurrently. Results in requested order:\n", len(results))
	for i, r := range results {
		name := strings.TrimSpace(r.name)
		if name == "" {
			name = fmt.Sprintf("agent-%d", i+1)
		}
		b.WriteString("\n## ")
		b.WriteString(name)
		b.WriteString("\n")
		if r.err != nil {
			b.WriteString("[error] ")
			b.WriteString(r.err.Error())
		} else {
			b.WriteString(strings.TrimSpace(r.content))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// tools 返回子 Agent 的工具注册表。
func (t *agentTool) tools() *tool.Registry {
	if t.cfg.Tools != nil {
		return t.cfg.Tools
	}
	return tool.NewRegistryWith(TaskAgentTools()...)
}
