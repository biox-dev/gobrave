package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/biox-dev/gobrave/internal/agent/llm/provider"
	"github.com/biox-dev/gobrave/internal/agent/logging"
	agentruntime "github.com/biox-dev/gobrave/internal/agent/runtime"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/tool/coder"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// 本文件迁移自 opencode internal/llm/agent/agent-tool.go。
//
// opencode 的 agentTool 会创建 task 子会话并持有 session / message 服务；gobrave 的
// 原生 Agent 不直接持有这些服务，而是通过 SubAgentSessionStore 端口落库，因此这里保持
// 「同步运行一个只读子 Agent」：
//
//   - 子 Agent 复用父请求的模型 / 厂商配置（由 AgentToolConfig.NewClient 构建）；
//   - 子 Agent 只拿到只读工具（glob / grep / ls / view / sourcegraph），不能改文件；
//   - 子 Agent 始终拥有自己的会话：父会话存在时建子会话，否则建独立会话；
//     流式事件只写入该会话，不会混入父 Agent 的时间线；
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
// 它对应 opencode NewAgentTool 的 (sessions, messages, lspClients) 参数：
// 会话 / 消息持久化经 SessionStore 端口注入，模型客户端与工具上下文由 Provider 注入。
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
	// SessionStore 把子 Agent 的会话与消息写入数据库（必填）。
	//
	// 子 Agent 始终拥有自己的会话：父请求有会话 ID 时创建 ParentSessionID = 父会话的
	// 子会话，否则（如 AI 摘要等无会话任务）创建一个独立会话；会话创建失败会让该
	// 子任务失败，不再降级为「无会话、事件丢弃」的执行。
	SessionStore SubAgentSessionStore
	// ParentRuntime 是父 Agent 的 Runtime：子 Agent 的权限 / 提问委托给它，
	// 事件仍只写入子会话（不污染父任务时间线）。
	ParentRuntime agentinterfaces.Runtime
	// Provider / Model 是写入子会话的溯源信息（可为空）。
	Provider string
	Model    string
	// ParentSessionID / UserID 是父请求的会话与用户 ID（回退值）。
	//
	// 正常情况下从工具执行 ctx 的 RuntimeContext 读取；ctx 没有时用这里的值，
	// 保证 Request.SessionID 设置了但 RuntimeContext 缺失的场景也能落库。
	ParentSessionID int64
	UserID          int64
}

// SubAgentSessionInfo 是创建子会话所需的信息。
type SubAgentSessionInfo struct {
	// ProjectID 是会话所属项目（与父请求保持一致，可为 0）。
	ProjectID int64
	// ParentSessionID 是父会话 ID（写入 agent_sessions.parent_session_id）；
	// 为 0 表示创建独立会话。
	ParentSessionID int64
	// Title 是子会话标题（优先使用子任务名，否则截取 prompt 首行）。
	Title string
	// UserID 是会话归属用户（从父请求的 RuntimeContext 读取）。
	UserID int64
	// Provider / Model 记录本次子 Agent 使用的 Provider 与模型。
	Provider string
	Model    string
}

// SubAgentSessionStore 是子 Agent 的会话 / 消息持久化端口。
//
// 由 Provider 用 agentinterfaces.SessionService + agentinterfaces.MessageService 实现（见 providers.subAgentSessionStore），
// native 包只按语义调用，不直接依赖 session / message 的具体类型。
type SubAgentSessionStore interface {
	// CreateChildSession 创建 ParentSessionID = info.ParentSessionID 的子会话并返回会话 ID。
	CreateChildSession(ctx context.Context, info SubAgentSessionInfo) (int64, error)
	// CreateSession 创建一个独立会话（无父会话时使用）并返回会话 ID。
	CreateSession(ctx context.Context, info SubAgentSessionInfo) (int64, error)
	// AppendUserMessage 追加子任务的 user 消息。
	AppendUserMessage(ctx context.Context, sessionID int64, content string) error
	// AppendEvent 把一条流式事件投影为子会话消息（投影规则由实现方决定，通常复用
	// agentservice.ProjectTimelineEvent；不产生消息的事件直接忽略）。
	AppendEvent(ctx context.Context, sessionID int64, event agenttypes.StreamEvent) error
	// AppendFinalMessage 追加子 Agent 的最终回答（或错误信息）。
	AppendFinalMessage(ctx context.Context, sessionID int64, content string) error
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

// runTask 构建并运行一个子 Agent，返回其最终回答（可同时写入子会话）。
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

	// 子 Agent 始终拥有自己的会话 + 记录型 Runtime。
	sessionID, rt, err := t.sessionRuntime(ctx, task)
	if err != nil {
		result.err = fmt.Errorf("error preparing agent session: %s", err)
		return result
	}

	answer, err := sub.Run(ctx, agenttypes.Request{
		Messages:     []agenttypes.Message{{Role: agenttypes.RoleUser, Content: task.Prompt}},
		SystemPrompt: systemPrompt,
	}, rt)
	if err != nil {
		result.err = fmt.Errorf("error generating agent: %s", err)
		t.appendFinal(ctx, sessionID, "error: "+result.err.Error())
		return result
	}
	if answer == nil || strings.TrimSpace(answer.Content) == "" {
		result.err = fmt.Errorf("no response from agent")
		t.appendFinal(ctx, sessionID, "error: "+result.err.Error())
		return result
	}
	result.content = answer.Content
	t.appendFinal(ctx, sessionID, answer.Content)
	return result
}

// sessionRuntime 为一次子 Agent 执行准备会话与 Runtime。
//
// 子 Agent 与父 Agent 一样始终拥有完整会话：
//   - 父请求有会话 ID：创建 ParentSessionID = 父会话的子会话；
//   - 否则（如 AI 摘要等无会话任务）：创建一个独立会话。
//
// 会话创建失败视为该子任务失败，不再降级为无会话执行。返回的 recordingRuntime 把
// reasoning / tool_call / tool_result 写入子会话，权限 / 提问委托给父 Runtime。
func (t *agentTool) sessionRuntime(ctx context.Context, task AgentTaskParams) (int64, agentinterfaces.Runtime, error) {
	if t.cfg.SessionStore == nil {
		return 0, nil, fmt.Errorf("agent tool: session store is required")
	}

	// 子 Agent 沿用父执行的执行事实（用户 / 项目 / 工作目录），并补上父会话回退值。
	facts := tool.TaskFactsFrom(ctx)
	if facts.WorkingDir == "" {
		facts.WorkingDir = t.cfg.WorkingDir
	}
	if facts.SessionID <= 0 {
		facts.SessionID = t.cfg.ParentSessionID
	}
	if facts.UserID <= 0 {
		facts.UserID = t.cfg.UserID
	}

	userID := facts.UserID
	info := SubAgentSessionInfo{
		ProjectID:       facts.ProjectID,
		ParentSessionID: facts.SessionID,
		Title:           subAgentSessionTitle(task),
		UserID:          userID,
		Provider:        t.cfg.Provider,
		Model:           t.cfg.Model,
	}

	var (
		sessionID int64
		err       error
	)
	if info.ParentSessionID > 0 {
		sessionID, err = t.cfg.SessionStore.CreateChildSession(ctx, info)
	} else {
		sessionID, err = t.cfg.SessionStore.CreateSession(ctx, info)
	}
	if err != nil {
		return 0, nil, fmt.Errorf("create session: %w", err)
	}
	if sessionID <= 0 {
		return 0, nil, fmt.Errorf("create session: empty session id")
	}

	if err := t.cfg.SessionStore.AppendUserMessage(ctx, sessionID, task.Prompt); err != nil {
		logging.Warn("sub-agent: append user message failed", "sessionID", sessionID, "err", err)
	}
	return sessionID, &recordingRuntime{store: t.cfg.SessionStore, sessionID: sessionID, parent: t.cfg.ParentRuntime}, nil
}

// appendFinal 把子 Agent 的最终回答（或错误）写入子会话；未落库时直接跳过。
func (t *agentTool) appendFinal(ctx context.Context, sessionID int64, content string) {
	if sessionID <= 0 || t.cfg.SessionStore == nil {
		return
	}
	if err := t.cfg.SessionStore.AppendFinalMessage(ctx, sessionID, content); err != nil {
		logging.Warn("sub-agent: append final message failed", "sessionID", sessionID, "err", err)
	}
}

// recordingRuntime 把子 Agent 的流式事件交给 SubAgentSessionStore 投影落库。
//
// 投影规则由实现方复用 agentservice.ProjectTimelineEvent（reasoning / tool_call / tool_result
// 产生消息，增量与生命周期事件忽略）；最终回答由 appendFinal 统一写入。
// 权限与提问委托给父 Runtime（与父 Agent 共用同一套策略 / 人工确认链路）；
// 父 Runtime 缺失时按拒绝 / 无法提问处理。
type recordingRuntime struct {
	store     SubAgentSessionStore
	sessionID int64
	parent    agentinterfaces.Runtime
}

func (r *recordingRuntime) Emit(ctx context.Context, event agenttypes.StreamEvent) error {
	err := r.store.AppendEvent(ctx, r.sessionID, event)
	if err != nil {
		logging.Warn("sub-agent: persist message failed", "sessionID", r.sessionID, "event", event.Type, "err", err)
	}
	return err
}

// Facts 实现 TaskAware：子 Agent 沿用父执行的执行事实（用户 / 项目 / 工作目录）。
func (r *recordingRuntime) Facts() agenttypes.TaskFacts {
	return agentruntime.FactsOf(r.parent)
}

func (r *recordingRuntime) RequestPermission(ctx context.Context, op agenttypes.Operation) (agenttypes.PermissionDecision, error) {
	if r.parent == nil {
		return agenttypes.DecisionDeny, agentruntime.ErrNoPermissionResolver
	}
	return r.parent.RequestPermission(ctx, op)
}

func (r *recordingRuntime) WaitPermission(ctx context.Context, permissionID int64) (agenttypes.PermissionDecision, error) {
	if r.parent == nil {
		return agenttypes.DecisionDeny, agentruntime.ErrNoPermissionResolver
	}
	return r.parent.WaitPermission(ctx, permissionID)
}

func (r *recordingRuntime) AskUser(ctx context.Context, input agenttypes.AskUserInput) (agenttypes.AskUserAnswer, error) {
	if r.parent == nil {
		return agenttypes.AskUserAnswer{}, agenttypes.ErrNoUserInputResolver
	}
	return r.parent.AskUser(ctx, input)
}

// subAgentSessionTitle 生成子会话标题：优先子任务名，否则取 prompt 首行前 60 个字符。
func subAgentSessionTitle(task AgentTaskParams) string {
	if name := strings.TrimSpace(task.Name); name != "" {
		return name
	}
	prompt := strings.TrimSpace(task.Prompt)
	if idx := strings.IndexAny(prompt, "\r\n"); idx >= 0 {
		prompt = strings.TrimSpace(prompt[:idx])
	}
	runes := []rune(prompt)
	if len(runes) > 60 {
		prompt = string(runes[:60]) + "…"
	}
	if prompt == "" {
		return "Sub Agent Session"
	}
	return prompt
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
