package providers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/agent"
	copilot "github.com/github/copilot-sdk/go"
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
func (a *copilotAgent) Invoke(ctx context.Context, req agent.Request, _ agent.Runtime) (*agent.Result, error) {
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

	session, err := client.CreateSession(ctx, a.sessionConfig(req, model))
	if err != nil {
		return nil, fmt.Errorf("copilot: create session: %w", err)
	}
	defer func() { _ = session.Disconnect() }()

	event, err := session.SendAndWait(ctx, copilot.MessageOptions{Prompt: buildPrompt(req)})
	if err != nil {
		return nil, fmt.Errorf("copilot: send and wait: %w", err)
	}

	content := ""
	if event != nil {
		if data, ok := event.Data.(*copilot.AssistantMessageData); ok {
			content = data.Content
		}
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

	session, err := client.CreateSession(ctx, a.sessionConfig(req, model))
	if err != nil {
		return nil, fmt.Errorf("copilot: create session: %w", err)
	}
	defer func() { _ = session.Disconnect() }()

	resultCh := make(chan copilotStreamResult, 1)
	var final strings.Builder

	unsubscribe := session.On(func(event copilot.SessionEvent) {
		switch data := event.Data.(type) {
		case *copilot.AssistantMessageDeltaData:
			if data.DeltaContent != "" {
				final.WriteString(data.DeltaContent)
				if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventText, Content: data.DeltaContent}); err != nil {
					sendStreamResult(resultCh, copilotStreamResult{content: final.String(), err: err})
				}
			}
		case *copilot.AssistantMessageData:
			// 非流式兜底：若没有收到任何 delta，则用最终消息补齐内容。
			if final.Len() == 0 && data.Content != "" {
				final.WriteString(data.Content)
			}
		case *copilot.SessionErrorData:
			sendStreamResult(resultCh, copilotStreamResult{content: final.String(), err: fmt.Errorf("copilot: session error: %s", data.Message)})
		case *copilot.SessionIdleData:
			sendStreamResult(resultCh, copilotStreamResult{content: final.String()})
		case *copilot.AssistantReasoningDeltaData:
			if data.DeltaContent != "" {
				if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventReasoning, Content: data.DeltaContent}); err != nil {
					sendStreamResult(resultCh, copilotStreamResult{content: final.String(), err: err})
				}
			}
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
func (a *copilotAgent) sessionConfig(req agent.Request, model string) *copilot.SessionConfig {
	cfg := &copilot.SessionConfig{
		Model:               model,
		WorkingDirectory:    firstNonEmpty(strings.TrimSpace(req.WorkingDir), strings.TrimSpace(a.opts.WorkingDir)),
		Streaming:           copilot.Bool(true),
		OnPermissionRequest: copilot.PermissionHandler.ApproveAll,
	}

	if system := strings.TrimSpace(req.SystemPrompt); system != "" {
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

// withTimeout 在 req.Timeout > 0 时为 ctx 附加超时。
func (a *copilotAgent) withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return ctx, func() {}
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
