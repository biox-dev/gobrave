package providers

import (
	"context"
	"fmt"
	"strings"

	agentclient "github.com/biox-dev/gobrave/internal/agent/client"
	"github.com/biox-dev/gobrave/internal/agent/llm/models"
	"github.com/biox-dev/gobrave/internal/agent/llm/provider"
	"github.com/biox-dev/gobrave/internal/agent/native"
	agentruntime "github.com/biox-dev/gobrave/internal/agent/runtime"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/tool/coder/hostshell"
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
		return nil, agentclient.ErrRuntimeRequired
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
		Tools:              a.toolRegistry(ctx, req, rt),
		PrepareToolContext: a.toolContext(req, rt),
		// 宿主机持久 shell 的生命周期 = 一次 Agent 请求：会话内复用（cwd/env 保留），
		// 请求结束立即退出进程。
		// 注意：Run 传入的 ctx 未注入 tool.TaskFacts（facts 只在 PrepareToolContext 里
		// 注入工具ctx），因此这里直接捕获会话 ID，不能用 tool.SessionIDFrom(ctx)。
		OnFinish: func(context.Context) {
			hostshell.CloseShell(facts.SessionID)
		},
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

// toolRegistry 组合本次调用可用的工具：注册表工具（含内置 ask_user / agent）。
//
// 注册表工具按 req.Tools（由 Profile.Tools 覆盖而来）裁剪：白名单为空时注入全部工具，
// 非空时只暴露白名单内的工具。ask_user / agent 都已在 builtin 工具集中登记（与其它
// 内置工具一致），它们的请求级 Runtime / 客户端工厂由 toolContext 注入；这里不再另行追加。
func (a *customAgent) toolRegistry(ctx context.Context, req agenttypes.Request, rt agentinterfaces.Runtime) *tool.Registry {
	return tool.NewRegistryWith(a.profileTools(ctx, req, rt)...)
}

// profileTools 返回本次调用暴露给模型的注册表工具。
//
// 白名单（req.Tools）为空表示不限制；非空时按名称裁剪，未注册的名称只告警不阻断
// （常见于 Profile 里写错工具名或工具被下线），避免整次任务因配置陈旧而失败。
func (a *customAgent) profileTools(ctx context.Context, req agenttypes.Request, rt agentinterfaces.Runtime) []tool.Tool {
	if a.opts.Tools == nil {
		return nil
	}
	tools := a.opts.Tools.Filter(req.Tools)
	if missing := a.opts.Tools.Missing(req.Tools); len(missing) > 0 {
		logger.Warnf(ctx, "[Agent] profile tools not registered (session=%d): %s",
			agentruntime.FactsOf(rt).SessionID, strings.Join(missing, ", "))
	}
	return tools
}

// toolContext 返回工具执行前的 ctx 装饰函数：注入权限、请求事实、运行时业务变量，
// 以及注册表级工具（ask_user / agent）需要的请求级能力。
//
// 进程级静态服务（分析节点执行器、脚本 / 报告浏览器、技能解析等）已在构建工具注册表时
// 通过构造注入（builtin.Deps / coder.Deps），这里不再逐项注入 ctx。
func (a *customAgent) toolContext(req agenttypes.Request, rt agentinterfaces.Runtime) func(context.Context) context.Context {
	rc := agentruntime.ToolRuntimeContextFromRequest(req)
	facts := agentruntime.FactsOf(rt)
	return func(ctx context.Context) context.Context {
		ctx = agentruntime.WithToolPermission(ctx, rt)
		ctx = agentruntime.WithToolFacts(ctx, rt)
		// Runtime 本身也注入 ctx：注册表级工具（ask_user / agent）在执行期靠它阻塞交互。
		ctx = agentruntime.WithToolRuntime(ctx, rt)
		ctx = agentruntime.WithToolRuntimeContext(ctx, rc)
		// 子 Agent 工具（agent）需要请求级的 LLM 客户端工厂与溯源信息（模型 / 厂商）。
		ctx = native.WithSubAgentOptions(ctx, native.SubAgentOptions{
			NewClient: a.subAgentClientFactory(req, facts),
			Provider:  strings.TrimSpace(req.Provider),
			Model:     strings.TrimSpace(req.ModelID),
		})
		return ctx
	}
}

// subAgentClientFactory 返回子 Agent 的 LLM 客户端工厂：沿用父请求的模型 / 厂商配置，
// 只把系统提示词换成子 Agent 专用的 TaskAgentSystemPrompt。
func (a *customAgent) subAgentClientFactory(req agenttypes.Request, facts agenttypes.TaskFacts) func(context.Context) (provider.Client, error) {
	return func(ctx context.Context) (provider.Client, error) {
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
	}
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
