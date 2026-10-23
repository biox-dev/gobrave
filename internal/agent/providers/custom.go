package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	agentclient "github.com/biox-dev/gobrave/internal/agent/client"
	"github.com/biox-dev/gobrave/internal/agent/llm/models"
	"github.com/biox-dev/gobrave/internal/agent/llm/provider"
	"github.com/biox-dev/gobrave/internal/agent/native"
	agentruntime "github.com/biox-dev/gobrave/internal/agent/runtime"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/logger"
)

// NewCustom 创建原生 Agent Provider。
//
// 它直接使用 gobrave 自己的 LLM 协议客户端与工具注册表驱动 Agent 循环，
// 不再依赖 opencode SDK。Provider 名称仍保留为 "custom"（与内置 Profile 一致）。
func NewCustom() agentclient.Provider {
	return customProvider{}
}

type customProvider struct{}

func (customProvider) Name() string { return agenttypes.ProviderCustom }

func (customProvider) New(opts agentclient.Options) (agentinterfaces.Agent, error) {
	return &customAgent{opts: opts}, nil
}

// customAgent 是 ProviderCustom 的 Agent 实现。
type customAgent struct {
	opts agentclient.Options
}

func (a *customAgent) Name() string { return agenttypes.ProviderCustom }

func (a *customAgent) Invoke(ctx context.Context, req agenttypes.Request, rt agentinterfaces.Runtime) (*agenttypes.Result, error) {
	return a.run(ctx, req, rt)
}

func (a *customAgent) Stream(ctx context.Context, req agenttypes.Request, rt agentinterfaces.Runtime) (*agenttypes.Result, error) {
	return a.run(ctx, req, rt)
}

// run 组装 LLM 客户端与工具上下文，交给原生 Agent 循环执行。
func (a *customAgent) run(ctx context.Context, req agenttypes.Request, rt agentinterfaces.Runtime) (*agenttypes.Result, error) {
	if rt == nil {
		rt = agentruntime.NewStandaloneRuntime(nil)
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	facts := agentruntime.FactsOf(rt)

	llmOpts, err := a.llmOptions(ctx, req, facts)
	if err != nil {
		logger.Errorf(ctx, "custom provider: resolve llm options failed (model=%s session=%d): %v", req.ModelID, facts.SessionID, err)
		return nil, err
	}
	client, err := provider.New(llmOpts)
	if err != nil {
		err = fmt.Errorf("custom provider: create client: %w", err)
		logger.Errorf(ctx, "custom provider: %v", err)
		return nil, err
	}

	nativeAgent := &native.Config{
		Client:             client,
		Tools:              a.toolRegistry(req, rt),
		PrepareToolContext: a.toolContext(req, rt),
	}
	result, err := nativeAgent.Run(ctx, req, rt)
	if err != nil {
		logger.Errorf(ctx, "custom provider: agent run failed (model=%s session=%d): %v", req.ModelID, facts.SessionID, err)
		return nil, err
	}
	return result, nil
}

// llmOptions 把 gobrave 请求 + 配置表解析为一次 LLM 调用参数。
func (a *customAgent) llmOptions(ctx context.Context, req agenttypes.Request, facts agenttypes.TaskFacts) (provider.Options, error) {
	workingDir := firstNonEmpty(strings.TrimSpace(facts.WorkingDir), strings.TrimSpace(a.opts.WorkingDir))
	if workingDir == "" {
		err := fmt.Errorf("custom provider: working dir is required")
		logger.Errorf(ctx, "%v (session=%d)", err, facts.SessionID)
		return provider.Options{}, err
	}

	modelKey := strings.TrimSpace(req.ModelID)
	if modelKey == "" {
		err := fmt.Errorf("custom provider: model is required")
		logger.Errorf(ctx, "%v (session=%d)", err, facts.SessionID)
		return provider.Options{}, err
	}

	// 模型配置按模型名解析；未命中时凭模型名前缀推断厂商，其余字段走默认值。
	var pc agentclient.ModelProviderConfig
	if resolved := a.opts.ResolveProvider(modelKey); resolved != nil {
		pc = *resolved
	}

	providerName := providerType(pc)
	if providerName == "" {
		providerName = inferProviderFromModel(modelKey)
	}
	if providerName == "" {
		err := fmt.Errorf(
			"custom provider: provider (vendor) is required for model %q; configure it in agent.providers", modelKey)
		logger.Errorf(ctx, "%v (session=%d)", err, facts.SessionID)
		return provider.Options{}, err
	}

	apiModel := firstNonEmpty(strings.TrimSpace(pc.APIModel), modelKey)
	maxTokens := int64(req.MaxTokens)
	if maxTokens <= 0 {
		maxTokens = pc.DefaultMaxTokens
	}
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	return provider.Options{
		Provider: models.ModelProvider(providerName),
		BaseURL:  strings.TrimSpace(pc.BaseURL),
		APIKey:   strings.TrimSpace(pc.APIKey),
		// 系统提示词已由 AgentService 渲染（含 Profile / 技能 / 记忆），这里原样透传。
		SystemPrompt: strings.TrimSpace(req.SystemPrompt),
		Model: models.Model{
			ID:                  models.ModelID(modelKey),
			Name:                firstNonEmpty(strings.TrimSpace(pc.Name), modelKey),
			Provider:            models.ModelProvider(providerName),
			APIModel:            apiModel,
			ContextWindow:       pc.ContextWindow,
			DefaultMaxTokens:    pc.DefaultMaxTokens,
			CanReason:           pc.CanReason,
			SupportsAttachments: pc.SupportsAttachments,
			CostPer1MIn:         pc.CostPer1MIn,
			CostPer1MOut:        pc.CostPer1MOut,
			CostPer1MInCached:   pc.CostPer1MInCached,
			CostPer1MOutCached:  pc.CostPer1MOutCached,
		},
		MaxTokens:       maxTokens,
		ReasoningEffort: strings.TrimSpace(pc.ReasoningEffort),
	}, nil
}

// toolRegistry 组合本次调用可用的工具：注册表工具 + 内置 ask_user + 子 Agent 工具。
//
// ask_user 依赖 Runtime 阻塞等待用户作答，因此只在有 Runtime 时注册。
func (a *customAgent) toolRegistry(req agenttypes.Request, rt agentinterfaces.Runtime) *tool.Registry {
	items := make([]tool.Tool, 0)
	if a.opts.Tools != nil {
		items = append(items, a.opts.Tools.Tools()...)
	}
	if rt != nil {
		items = append(items, askUserTool(rt))
	}
	// 子 Agent 工具（迁移自 opencode NewAgentTool）：模型可把「多轮检索」委派给
	// 只读子 Agent，避免父 Agent 上下文被大量中间结果占满。
	items = append(items, a.newAgentTool(req, rt))
	return tool.NewRegistryWith(items...)
}

// newAgentTool 构建 agent 工具：复用父请求的模型 / 厂商配置启动一个只读子 Agent。
//
// 与 opencode NewAgentTool 的差异：gobrave 没有会话服务，子 Agent 不再创建 task
// 会话，而是同步运行原生 Agent 循环并直接返回最终回答（见 native.NewAgentTool）。
func (a *customAgent) newAgentTool(req agenttypes.Request, rt agentinterfaces.Runtime) tool.Tool {
	prepare := a.toolContext(req, rt)
	facts := agentruntime.FactsOf(rt)
	workingDir := firstNonEmpty(strings.TrimSpace(facts.WorkingDir), strings.TrimSpace(a.opts.WorkingDir))
	return native.NewAgentTool(native.AgentToolConfig{
		// 子 Agent 沿用父请求的模型 / 厂商配置，只替换系统提示词；配置错误在执行期
		// 转为工具失败结果，由父 Agent 决定是否重试。
		NewClient: func(ctx context.Context) (provider.Client, error) {
			subReq := req
			subReq.SystemPrompt = native.TaskAgentSystemPrompt
			llmOpts, err := a.llmOptions(ctx, subReq, facts)
			if err != nil {
				logger.Errorf(ctx, "custom provider: sub-agent llm options failed (model=%s session=%d): %v", subReq.ModelID, facts.SessionID, err)
				return nil, err
			}
			client, err := provider.New(llmOpts)
			if err != nil {
				logger.Errorf(ctx, "custom provider: sub-agent client creation failed (model=%s session=%d): %v", subReq.ModelID, facts.SessionID, err)
				return nil, err
			}
			return client, nil
		},
		PrepareToolContext: prepare,
		WorkingDir:         workingDir,
		SystemPrompt:       native.TaskAgentSystemPrompt,
		// 子会话落库：有父会话时创建 ParentSessionID = 父会话的子会话，并把子 Agent 的
		// reasoning / tool_call / tool_result / 最终回答写入 agent_messages。
		SessionStore: newSubAgentSessionStore(a.opts.Sessions, a.opts.Messages),
		Provider:     strings.TrimSpace(req.Provider),
		Model:        strings.TrimSpace(req.ModelID),
		// 回退值：工具执行 ctx 缺少 RuntimeContext 时也能拿到父会话 / 用户。
		ParentSessionID: facts.SessionID,
		UserID:          facts.UserID,
	})
}

// toolContext 返回工具执行前的 ctx 装饰函数：注入权限、请求事实与进程级服务能力。
func (a *customAgent) toolContext(req agenttypes.Request, rt agentinterfaces.Runtime) func(context.Context) context.Context {
	rc := agentruntime.ToolRuntimeContextFromRequest(req)
	services := a.opts
	return func(ctx context.Context) context.Context {
		ctx = agentruntime.WithToolPermission(ctx, rt)
		ctx = agentruntime.WithToolFacts(ctx, rt)
		ctx = agentruntime.WithToolRuntime(ctx, rc,
			services.AnalysisNodeRunner,
			services.FileReader,
			services.ScriptCreator,
			services.AnalysisNodeCreator,
			services.AnalysisNodeParamsBuilder,
			services.AnalysisNodeUpdater,
			services.AgentEnvUpdater,
			services.MemorySaver,
			services.FileHistory,
		)
		ctx = agentruntime.WithAgentAnalysisRunner(ctx, services.AgentAnalysisRunner)
		ctx = agentruntime.WithProjectResourceBrowser(ctx, services.ProjectResourceBrowser)
		ctx = agentruntime.WithSkills(ctx, services.Skills)
		if services.LSP != nil {
			ctx = agentruntime.WithLSPClients(ctx, services.LSP.Clients())
		}
		return ctx
	}
}

// askUserTool 构建 ask_user 工具：解析模型入参 → Runtime.AskUser 阻塞等待作答 →
// 把答案格式化为可读文本回填给模型。无 UI 时 Runtime 返回错误，执行器转为失败结果。
func askUserTool(rt agentinterfaces.Runtime) tool.Tool {
	def := agentruntime.AskUserToolDefinition()
	return tool.NewFunc(def.Name, def.Description, def.InputSchema,
		func(ctx context.Context, in agenttypes.AskUserInput) (string, error) {
			answer, err := rt.AskUser(ctx, in)
			if err != nil {
				return "", err
			}
			return formatAskUserAnswer(in, answer), nil
		})
}

// inferProviderFromModel 在未显式配置厂商类型时，按模型名前缀推断厂商。
func inferProviderFromModel(modelID string) string {
	name := strings.ToLower(strings.TrimSpace(modelID))
	switch {
	case name == "":
		return ""
	case strings.HasPrefix(name, "gpt-"), strings.HasPrefix(name, "o1"), strings.HasPrefix(name, "o3"), strings.HasPrefix(name, "o4"):
		return "openai"
	case strings.HasPrefix(name, "claude-"):
		return "anthropic"
	case strings.HasPrefix(name, "deepseek-"):
		return "deepseek"
	case strings.HasPrefix(name, "gemini-"):
		return "gemini"
	case strings.HasPrefix(name, "grok-"):
		return "xai"
	default:
		return ""
	}
}

// providerType 返回模型配置中的厂商类型（openai / anthropic / deepseek …）。
//
// 依次回退：Provider 字段 → Extra["provider"] → Extra["type"]。
func providerType(pc agentclient.ModelProviderConfig) string {
	if name := strings.TrimSpace(pc.Provider); name != "" {
		return strings.ToLower(name)
	}
	if name := strings.TrimSpace(pc.Extra["provider"]); name != "" {
		return strings.ToLower(name)
	}
	return strings.ToLower(strings.TrimSpace(pc.Extra["type"]))
}

// formatAskUserAnswer 把用户作答格式化为人类可读摘要（作为 ask_user 工具结果文本）。
func formatAskUserAnswer(in agenttypes.AskUserInput, answer agenttypes.AskUserAnswer) string {
	byID := make(map[string]agenttypes.QuestionAnswer, len(answer.Answers))
	for _, ans := range answer.Answers {
		if ans.ID != "" {
			byID[ans.ID] = ans
		}
	}

	var b strings.Builder
	if title := strings.TrimSpace(in.Title); title != "" {
		b.WriteString(title)
		b.WriteString("\n")
	}
	written := 0
	for _, q := range in.Questions {
		ans, ok := byID[q.ID]
		if !ok {
			continue
		}
		text := formatQuestionAnswer(ans)
		if text == "" {
			continue
		}
		if written > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- ")
		b.WriteString(strings.TrimSpace(q.Title))
		b.WriteString(": ")
		b.WriteString(text)
		written++
	}
	// questions 未声明但作答中出现的答案（模型自定义 ID）原样追加。
	declared := make(map[string]bool, len(in.Questions))
	for _, q := range in.Questions {
		declared[q.ID] = true
	}
	for _, ans := range answer.Answers {
		if declared[ans.ID] || ans.ID == "" {
			continue
		}
		text := formatQuestionAnswer(ans)
		if text == "" {
			continue
		}
		if written > 0 {
			b.WriteString("\n")
		}
		b.WriteString("- ")
		b.WriteString(ans.ID)
		b.WriteString(": ")
		b.WriteString(text)
		written++
	}
	if written == 0 {
		if data, err := json.Marshal(answer); err == nil {
			return string(data)
		}
		return "用户未作有效选择"
	}
	return b.String()
}

// formatQuestionAnswer 把单个答案拼接为文本（多选用逗号分隔，自定义输入用括号标注）。
func formatQuestionAnswer(ans agenttypes.QuestionAnswer) string {
	parts := make([]string, 0, len(ans.Values)+1)
	parts = append(parts, ans.Values...)
	if value := strings.TrimSpace(ans.Value); value != "" {
		parts = append(parts, value)
	}
	text := strings.Join(parts, ", ")
	if custom := strings.TrimSpace(ans.Custom); custom != "" {
		if text == "" {
			text = custom
		} else if text != custom {
			text += "（自定义：" + custom + "）"
		}
	}
	return text
}
