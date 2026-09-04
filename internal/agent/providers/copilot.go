package providers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/agent/skill"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	copilot "github.com/github/copilot-sdk/go"
	copilotrpc "github.com/github/copilot-sdk/go/rpc"
)

// copilotProvider 通过 GitHub Copilot SDK 实现真实的 Copilot 调用。
//
// 说明：
//   - 未配置 BaseURL 时走 GitHub Copilot 官方 API（使用登录态或 GitHubToken 鉴权）。
//   - 配置了 BaseURL 时走 BYOK（自定义 OpenAI/Azure/Anthropic 兼容服务）。
//   - 每次 Invoke / Stream 都会创建独立的 Client 与 Session，保证调用无状态。
type copilotProvider struct{}

// NewCopilot 创建 Copilot Provider。
func NewCopilot() agent.Provider { return copilotProvider{} }

func (copilotProvider) Name() string { return agent.ProviderCopilot }

func (copilotProvider) New(opts agent.Options) (agent.Agent, error) {
	return &copilotAgent{opts: opts}, nil
}

// copilotAgent 是 Copilot Provider 的 Agent 实现。
type copilotAgent struct {
	opts agent.Options
}

func (a *copilotAgent) Name() string { return agent.ProviderCopilot }

// copilotStreamResult 是流式调用内部使用的终态结果。
type copilotStreamResult struct {
	content string
	err     error
}

// Invoke 执行一次性任务：创建会话 → 发送 prompt → 等待 session.idle → 返回最终内容。
//
// 通过 SDK 的 SendAndWait 等待 session.idle。由于 SendAndWait 在 ctx 无 deadline 时
// 会强加 60s 硬超时，这里通过 withTimeout 为 ctx 附加默认 10 分钟超时（req.Timeout > 0
// 时优先使用 req.Timeout），避免推理类模型或耗时较长的 Copilot 调用被误判为超时。
func (a *copilotAgent) Invoke(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	if rt == nil {
		rt = agent.NewStandaloneRuntime(nil)
	}

	ctx, cancel := a.withTimeout(ctx, req.Timeout)
	defer cancel()

	model, err := a.resolveModel(req)
	if err != nil {
		return nil, err
	}

	client := copilot.NewClient(a.clientOptions())
	if err := client.Start(ctx); err != nil {
		return nil, fmt.Errorf("copilot: start client: %w", err)
	}
	defer func() { _ = client.Stop() }()

	session, err := client.CreateSession(ctx, a.sessionConfig(ctx, req, model, rt))
	if err != nil {
		return nil, fmt.Errorf("copilot: create session: %w", err)
	}
	defer func() { _ = session.Disconnect() }()

	event, err := session.SendAndWait(ctx, copilot.MessageOptions{Prompt: buildPrompt(req)})
	if err != nil {
		return nil, fmt.Errorf("copilot: send and wait: %w", err)
	}

	content := ""
	var messageData *copilot.AssistantMessageData
	if event != nil {
		if data, ok := event.Data.(*copilot.AssistantMessageData); ok {
			messageData = data
			content = data.Content
		}
	}

	// 将最终结果也通过 Runtime.Emit 输出，保证调用方无论走 Invoke 还是 Stream 都能收到统一事件流。
	// 这里输出“完整消息块”（而非 text 增量），使其可作为时间线数据源落库。
	if messageData != nil {
		for _, tr := range messageData.ToolRequests {
			if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventToolCall, Data: agent.ToolCall{
				ID:        tr.ToolCallID,
				Name:      tr.Name,
				Arguments: tr.Arguments,
			}}); err != nil {
				return nil, err
			}
		}
		if len(messageData.ToolRequests) == 0 {
			if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventMessage, Data: toMessageBlock(messageData)}); err != nil {
				return nil, err
			}
		}
	}
	if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventDone}); err != nil {
		return nil, err
	}
	return &agent.Result{Content: content}, nil
}

// Stream 执行流式请求：把 Copilot 的增量事件转换为 agent.StreamEvent，通过 Runtime.Emit 输出。
func (a *copilotAgent) Stream(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	if rt == nil {
		rt = agent.NewStandaloneRuntime(nil)
	}

	ctx, cancel := a.withTimeout(ctx, req.Timeout)
	defer cancel()

	model, err := a.resolveModel(req)
	if err != nil {
		return nil, err
	}

	client := copilot.NewClient(a.clientOptions())
	if err := client.Start(ctx); err != nil {
		return nil, fmt.Errorf("copilot: start client: %w", err)
	}
	defer func() { _ = client.Stop() }()

	session, err := client.CreateSession(ctx, a.sessionConfig(ctx, req, model, rt))
	if err != nil {
		return nil, fmt.Errorf("copilot: create session: %w", err)
	}
	defer func() { _ = session.Disconnect() }()

	resultCh := make(chan copilotStreamResult, 1)
	var final strings.Builder

	unsubscribe := session.On(func(event copilot.SessionEvent) {
		switch data := event.Data.(type) {
		// —— 完整块：时间线数据源，落库 ——
		case *copilot.AssistantTurnStartData:
			if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventTurnStart, Data: turnBlock(data.TurnID, data.Model)}); err != nil {
				sendStreamResult(resultCh, copilotStreamResult{content: final.String(), err: err})
			}

		case *copilot.AssistantTurnEndData:
			if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventTurnEnd, Data: turnBlock(data.TurnID, data.Model)}); err != nil {
				sendStreamResult(resultCh, copilotStreamResult{content: final.String(), err: err})
			}

		// —— 增量：仅实时渲染，默认不落库 ——
		case *copilot.AssistantMessageDeltaData:
			if data.DeltaContent != "" {
				final.WriteString(data.DeltaContent)
				if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventText, Content: data.DeltaContent}); err != nil {
					sendStreamResult(resultCh, copilotStreamResult{content: final.String(), err: err})
				}
			}
		case *copilot.AssistantMessageData:
			for _, tr := range data.ToolRequests {
				if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventToolCall, Data: agent.ToolCall{
					ID:        tr.ToolCallID,
					Name:      tr.Name,
					Arguments: tr.Arguments,
				}}); err != nil {
					sendStreamResult(resultCh, copilotStreamResult{content: final.String(), err: err})
				}
			}
			// 非流式兜底：若没有收到任何 delta，则用最终消息补齐内容。
			// if final.Len() == 0 && data.Content != "" {
			// 	final.WriteString(data.Content)
			// }
			if len(data.ToolRequests) == 0 {
				if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventMessage, Data: toMessageBlock(data)}); err != nil {
					sendStreamResult(resultCh, copilotStreamResult{content: final.String(), err: err})
				}
			}
		case *copilot.AssistantReasoningDeltaData:
			if data.DeltaContent != "" {
				if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventReasoningDelta, Content: data.DeltaContent}); err != nil {
					sendStreamResult(resultCh, copilotStreamResult{content: final.String(), err: err})
				}
			}
		case *copilot.AssistantReasoningData:
			if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventReasoning, Data: agent.ReasoningBlock{ID: data.ReasoningID, Content: data.Content}}); err != nil {
				sendStreamResult(resultCh, copilotStreamResult{content: final.String(), err: err})
			}

		// —— 终态 ——
		case *copilot.SessionErrorData:
			sendStreamResult(resultCh, copilotStreamResult{content: final.String(), err: fmt.Errorf("copilot: session error: %s", data.Message)})
		case *copilot.SessionIdleData:
			sendStreamResult(resultCh, copilotStreamResult{content: final.String()})
		}
	})
	defer unsubscribe()

	if _, err := session.Send(ctx, copilot.MessageOptions{Prompt: buildPrompt(req)}); err != nil {
		return nil, fmt.Errorf("copilot: send: %w", err)
	}

	select {
	case res := <-resultCh:
		if res.err != nil {
			_ = session.Abort(context.Background())
			_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventError, Err: res.err})
			return nil, res.err
		}
		if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventDone}); err != nil {
			return nil, err
		}
		return &agent.Result{Content: res.content}, nil
	case <-ctx.Done():
		_ = session.Abort(context.Background())
		return nil, ctx.Err()
	}
}

// sendStreamResult 以非阻塞方式投递流式终态结果；事件按序到达，只保留第一个终态。
func sendStreamResult(ch chan<- copilotStreamResult, res copilotStreamResult) {
	select {
	case ch <- res:
	default:
	}
}

// clientOptions 根据 Options 构建 Copilot Client 配置。
func (a *copilotAgent) clientOptions() *copilot.ClientOptions {
	opts := &copilot.ClientOptions{}

	// 默认压低运行时日志，避免污染业务日志；可通过 Extra["log_level"] 覆盖。
	logLevel := strings.TrimSpace(a.opts.Extra["log_level"])
	if logLevel == "" {
		logLevel = "error"
	}
	opts.LogLevel = logLevel

	// 可选：连接一个已启动的 copilot 运行时（host:port 或 http://host:port）。
	if cliURL := strings.TrimSpace(a.opts.Extra["cli_url"]); cliURL != "" {
		opts.Connection = copilot.URIConnection{URL: cliURL}
	}

	return opts
}

// sessionConfig 根据 Options 与单次请求构建 Copilot Session 配置。
func (a *copilotAgent) sessionConfig(ctx context.Context, req agent.Request, model string, rt agent.Runtime) *copilot.SessionConfig {
	cfg := &copilot.SessionConfig{
		Model:               model,
		WorkingDirectory:    firstNonEmpty(strings.TrimSpace(req.WorkingDir), strings.TrimSpace(a.opts.WorkingDir)),
		Streaming:           copilot.Bool(true),
		OnPermissionRequest: a.permissionHandler(ctx, rt),
		Tools:               append(a.buildTools(rt), a.buildSkills(rt)...),
	}

	system := strings.TrimSpace(req.SystemPrompt)
	if instr := a.buildSkillInstructions(); instr != "" {
		if system != "" {
			system += "\n\n" + instr
		} else {
			system = instr
		}
	}
	if system != "" {
		cfg.SystemMessage = &copilot.SystemMessageConfig{Content: system}
	}

	if provider := a.providerConfig(model); provider != nil {
		cfg.Provider = provider
	}

	if token := strings.TrimSpace(a.opts.Extra["github_token"]); token != "" {
		cfg.GitHubToken = token
	}

	return cfg
}

// buildTools 把 opts.Tools（tool.Registry）转换为 Copilot 的 Tool 列表，每个 Tool 附带一个
// Handler，桥接到框架的 ToolRunner 执行工具并把结果回传给 Copilot。
//
// Copilot SDK 采用「handler 自动执行」模型：模型发起工具调用时，SDK 会广播
// ExternalToolRequested 事件并在独立 goroutine 中调用对应 Handler，再把返回的 ToolResult
// 通过 RPC 回填给模型，多轮 tool-call 循环由 SDK 内部完成，Provider 无需自己驱动循环。
//
// 桥接过程（见 agent.ToolRunner.Run）：
//
//	ToolInvocation → agent.ToolCall → 输出 tool_call 事件 → 执行 → tool.Result
//	  → 输出 tool_result 事件 → copilot.ToolResult → 回传给模型
func (a *copilotAgent) buildTools(rt agent.Runtime) []copilot.Tool {
	if a.opts.Tools == nil {
		return nil
	}

	runner := agent.NewToolRunner(tool.NewExecutor(a.opts.Tools), rt)
	defs := a.opts.Tools.List()

	out := make([]copilot.Tool, 0, len(defs))
	for _, def := range defs {
		name := def.Name
		out = append(out, copilot.Tool{
			Name:        def.Name,
			Description: def.Description,
			Parameters:  def.InputSchema,
			Handler: func(inv copilot.ToolInvocation) (copilot.ToolResult, error) {
				res := runner.Run(inv.TraceContext, agent.ToolCall{
					ID:        inv.ToolCallID,
					Name:      name,
					Arguments: inv.Arguments,
				})
				return toCopilotToolResult(res), nil
			},
		})
	}
	return out
}

// toCopilotToolResult 把框架的 tool.Result 转换为 Copilot 的 ToolResult。
//
// 工具执行失败（IsError=true）映射为 failure 结果，错误信息写入 Error 字段，
// 使模型能看到错误并尝试纠正；成功映射为 success 结果。
func toCopilotToolResult(res tool.Result) copilot.ToolResult {
	if res.IsError {
		return copilot.ToolResult{
			TextResultForLLM: res.Content,
			ResultType:       "failure",
			Error:            res.Content,
		}
	}
	return copilot.ToolResult{
		TextResultForLLM: res.Content,
		ResultType:       "success",
	}
}

// buildSkills 把 opts.Skills（skill.Registry）转换为 Copilot 的 Tool 列表，每个 Skill 附带一个
// Handler，桥接到框架的 SkillRunner 执行技能并把结果回传给 Copilot。
//
// 技能在 Copilot 侧与工具共用同一套 function-calling 通道：模型发起技能调用时，SDK 会广播
// 事件并在独立 goroutine 中调用对应 Handler，再把返回的 ToolResult 通过 RPC 回填给模型。
// 此外，技能的指令正文（Instructions）会由 buildSkillInstructions 注入系统提示词，使模型
// 在函数调用之外也能感知技能语义。
//
// 桥接过程（见 agent.SkillRunner.Run）：
//
//	SkillInvocation → agent.SkillCall → 输出 skill_call 事件 → 执行 → skill.Result
//	  → 输出 skill_result 事件 → copilot.ToolResult → 回传给模型
func (a *copilotAgent) buildSkills(rt agent.Runtime) []copilot.Tool {
	if a.opts.Skills == nil {
		return nil
	}

	runner := agent.NewSkillRunner(skill.NewInvoker(a.opts.Skills), rt)
	defs := a.opts.Skills.List()

	out := make([]copilot.Tool, 0, len(defs))
	for _, def := range defs {
		name := def.Name
		out = append(out, copilot.Tool{
			Name:        def.Name,
			Description: def.Description,
			Parameters:  def.InputSchema,
			Handler: func(inv copilot.ToolInvocation) (copilot.ToolResult, error) {
				res := runner.Run(inv.TraceContext, agent.SkillCall{
					ID:        inv.ToolCallID,
					Name:      name,
					Arguments: inv.Arguments,
				})
				return toCopilotSkillResult(res), nil
			},
		})
	}
	return out
}

// toCopilotSkillResult 把框架的 skill.Result 转换为 Copilot 的 ToolResult。
//
// 语义与 toCopilotToolResult 一致：失败映射为 failure，成功映射为 success。
func toCopilotSkillResult(res skill.Result) copilot.ToolResult {
	if res.IsError {
		return copilot.ToolResult{
			TextResultForLLM: res.Content,
			ResultType:       "failure",
			Error:            res.Content,
		}
	}
	return copilot.ToolResult{
		TextResultForLLM: res.Content,
		ResultType:       "success",
	}
}

// buildSkillInstructions 把所有技能的指令正文拼接为一个 markdown 块，用于注入系统提示词，
// 让模型能感知可用技能及其用法（技能区别于工具的关键：携带上下文指令）。
//
// 无技能注册或全部技能均无指令正文时返回空字符串。
func (a *copilotAgent) buildSkillInstructions() string {
	if a.opts.Skills == nil {
		return ""
	}
	manifests := a.opts.Skills.Instructions()
	if len(manifests) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## Available skills\n\n")
	for _, m := range manifests {
		b.WriteString("### ")
		b.WriteString(m.Name)
		if desc := strings.TrimSpace(m.Description); desc != "" {
			b.WriteString(": ")
			b.WriteString(desc)
		}
		b.WriteString("\n\n")
		if instr := strings.TrimSpace(m.Instructions); instr != "" {
			b.WriteString(instr)
			b.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(b.String())
}

// permissionHandler 将 Copilot 的权限请求映射为 agent.Operation，并通过 Runtime 请求权限，
// 阻塞等待策略 / UI 决策后再把结果翻译回 Copilot 的 PermissionDecision。
func (a *copilotAgent) permissionHandler(ctx context.Context, rt agent.Runtime) copilot.PermissionHandlerFunc {
	return func(request copilot.PermissionRequest, _ copilot.PermissionInvocation) (copilotrpc.PermissionDecision, error) {
		op := toOperation(request)

		decision, err := rt.RequestPermission(ctx, op)
		if err != nil {
			// 权限等待失败（例如无解析器 / 被取消），交给 SDK 决定如何降级。
			return nil, err
		}

		switch decision {
		case agent.DecisionAllow:
			return &copilotrpc.PermissionDecisionApproveOnce{}, nil
		case agent.DecisionDeny:
			feedback := fmt.Sprintf("permission denied: %s", op.Type)
			return &copilotrpc.PermissionDecisionReject{Feedback: &feedback}, nil
		default:
			// DecisionAsk 不应在此出现（RequestPermission 阻塞直到 allow/deny），保守拒绝。
			feedback := "permission decision unresolved"
			return &copilotrpc.PermissionDecisionReject{Feedback: &feedback}, nil
		}
	}
}

// toOperation 将 Copilot 的权限请求映射为框架内的 Operation 描述。
func toOperation(request copilot.PermissionRequest) agent.Operation {
	switch request.Kind() {
	case copilot.PermissionRequestKindRead:
		if r, ok := request.(*copilot.PermissionRequestRead); ok {
			return agent.Operation{Type: agent.OperationRead, Path: r.Path,
				Metadata: map[string]any{"intention": r.Intention},
			}
		}
	case copilot.PermissionRequestKindWrite:
		if w, ok := request.(*copilot.PermissionRequestWrite); ok {
			return agent.Operation{Type: agent.OperationWrite, Path: w.FileName,
				Content: w.Diff}
		}
	case copilot.PermissionRequestKindShell:
		if s, ok := request.(*copilot.PermissionRequestShell); ok {
			typ := agent.OperationExecute
			if s.HasWriteFileRedirection {
				typ = agent.OperationWrite
			}
			return agent.Operation{
				Type:    typ,
				Command: s.FullCommandText,
				Metadata: map[string]any{
					"paths":        s.PossiblePaths,
					"intention":    s.Intention,
					"has_redirect": s.HasWriteFileRedirection,
				},
			}
		}
	case copilot.PermissionRequestKindURL:
		if u, ok := request.(*copilot.PermissionRequestURL); ok {
			return agent.Operation{
				Type:     agent.OperationNetwork,
				Metadata: map[string]any{"url": u.URL, "intention": u.Intention},
			}
		}
	}
	return agent.Operation{
		Type:     agent.OperationExecute,
		Metadata: map[string]any{"kind": string(request.Kind())},
	}
}

// providerConfig 构建 BYOK Provider 配置；未配置 BaseURL 时返回 nil，走官方 Copilot API。
func (a *copilotAgent) providerConfig(model string) *copilot.ProviderConfig {
	baseURL := strings.TrimSpace(a.opts.BaseURL)
	if baseURL == "" {
		return nil
	}

	providerType := strings.TrimSpace(a.opts.Extra["type"])
	if providerType == "" {
		providerType = "openai"
	}

	cfg := &copilot.ProviderConfig{
		Type:        providerType,
		BaseURL:     baseURL,
		APIKey:      strings.TrimSpace(a.opts.APIKey),
		BearerToken: strings.TrimSpace(a.opts.BearerToken),
		ModelID:     model,
	}
	if wireAPI := strings.TrimSpace(a.opts.Extra["wire_api"]); wireAPI != "" {
		cfg.WireAPI = wireAPI
	}
	return cfg
}

// resolveModel 解析本次调用使用的模型：请求级 Model 优先，其次为 Provider 配置的 Model。
func (a *copilotAgent) resolveModel(req agent.Request) (string, error) {
	model := firstNonEmpty(strings.TrimSpace(req.Model), strings.TrimSpace(a.opts.Model))
	if model == "" && strings.TrimSpace(a.opts.BaseURL) != "" {
		return "", fmt.Errorf("copilot: model is required when using a custom provider")
	}
	return model, nil
}

// defaultTimeout 是未显式设置超时时的默认值。
//
// SDK 的 SendAndWait 在 ctx 无 deadline 时会强加 60s 硬超时，对推理类模型或耗时较长
// 的调用过短；这里统一用 10 分钟作为默认，req.Timeout > 0 时仍以请求值为准。
const defaultTimeout = 10 * time.Minute

// withTimeout 为 ctx 附加超时：timeout > 0 时使用其值，否则使用 defaultTimeout。
func (a *copilotAgent) withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return context.WithTimeout(ctx, timeout)
}

// buildPrompt 将 Request 中的对话上下文转换为 Copilot 单次 Send 所需的 prompt。
func buildPrompt(req agent.Request) string {
	// 常见场景：单条 user 消息直接透传。
	if len(req.Messages) == 1 {
		return strings.TrimSpace(req.Messages[0].Content)
	}

	var b strings.Builder
	for i, m := range req.Messages {
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		if i > 0 {
			b.WriteString("\n\n")
		}
		switch m.Role {
		case agent.RoleUser:
			b.WriteString(content)
		case agent.RoleAssistant:
			b.WriteString("assistant: ")
			b.WriteString(content)
		default:
			if role := strings.TrimSpace(m.Role); role != "" {
				b.WriteString(role)
				b.WriteString(": ")
			}
			b.WriteString(content)
		}
	}
	return strings.TrimSpace(b.String())
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// toMessageBlock 将 Copilot 的完整 assistant 消息转换为框架内的完整消息块。
func toMessageBlock(data *copilot.AssistantMessageData) agent.MessageBlock {
	mb := agent.MessageBlock{
		ID:      data.MessageID,
		Content: data.Content,
	}
	if data.TurnID != nil {
		mb.TurnID = *data.TurnID
	}
	for _, tr := range data.ToolRequests {
		mb.ToolCalls = append(mb.ToolCalls, agent.ToolCall{
			ID:        tr.ToolCallID,
			Name:      tr.Name,
			Arguments: tr.Arguments,
		})
	}
	return mb
}

// turnBlock 构造一轮边界块；model 可能为 nil。
func turnBlock(turnID string, model *string) agent.TurnBlock {
	tb := agent.TurnBlock{TurnID: turnID}
	if model != nil {
		tb.Model = *model
	}
	return tb
}
