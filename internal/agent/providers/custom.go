package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent"
	opencodesdk "github.com/biox-dev/opencode/sdk"
)

// NewCustom creates a provider that bridges gobrave agent requests to OpenCode SDK.
//
// This integration uses direct Go calls and does not execute the opencode binary.
func NewCustom() agent.Provider {
	return customProvider{}
}

type customProvider struct{}

func (customProvider) Name() string { return agent.ProviderCustom }

func (customProvider) New(opts agent.Options) (agent.Agent, error) {
	return &customAgent{opts: opts}, nil
}

type customAgent struct {
	opts agent.Options
}

func (a *customAgent) Name() string { return agent.ProviderCustom }

func (a *customAgent) Invoke(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	return a.run(ctx, req, rt)
}

func (a *customAgent) Stream(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	return a.run(ctx, req, rt)
}

func (a *customAgent) run(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	if rt == nil {
		rt = agent.NewStandaloneRuntime(nil)
	}

	sdkClient, err := opencodesdk.NewClient(ctx, opencodesdk.Options{
		WorkingDir:  firstNonEmpty(strings.TrimSpace(req.WorkingDir), strings.TrimSpace(a.opts.WorkingDir)),
		Debug:       strings.EqualFold(strings.TrimSpace(a.opts.Extra["opencode_debug"]), "true"),
		AutoApprove: true,
	})
	if err != nil {
		return nil, fmt.Errorf("custom provider: init opencode sdk: %w", err)
	}
	defer func() { _ = sdkClient.Close() }()

	prompt := buildOpenCodePrompt(req)
	if strings.TrimSpace(prompt) == "" {
		return nil, fmt.Errorf("custom provider: empty prompt")
	}

	seenToolCalls := map[string]map[string]bool{}
	seenToolResults := map[string]map[string]bool{}

	result, err := sdkClient.SendMessageStream(ctx, opencodesdk.RunRequest{
		SessionTitle: firstNonEmpty(strings.TrimSpace(req.SessionID), "gobrave-opencode"),
		Prompt:       prompt,
		AutoApprove:  true,
	}, func(event opencodesdk.Event) {
		if event.Message != nil {
			ensureSeenMaps(seenToolCalls, seenToolResults, event.Message.ID)
			emitToolEvents(ctx, rt, event.Message, seenToolCalls, seenToolResults)
			if event.Message.Role == opencodesdk.RoleAssistant && event.Message.Finished {
				_ = rt.Emit(ctx, agent.StreamEvent{
					Type: agent.StreamEventMessage,
					Data: toAssistantMessageBlock(*event.Message),
				})
			}
		}

		switch event.Type {
		case opencodesdk.EventAssistantTextDelta:
			if event.Delta != "" {
				_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventText, Content: event.Delta})
			}
		case opencodesdk.EventAssistantThinkDelta:
			if event.Delta != "" {
				_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventReasoningDelta, Content: event.Delta})
			}
		case opencodesdk.EventAgentCompleted:
			_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventDone})
		case opencodesdk.EventAgentCanceled:
			_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventError, Err: event.Error})
		case opencodesdk.EventAgentError:
			_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventError, Err: event.Error})
		}
	})
	if err != nil {
		return nil, fmt.Errorf("custom provider: opencode run failed: %w", err)
	}

	return &agent.Result{Content: result.Message.Text}, nil
}

func buildOpenCodePrompt(req agent.Request) string {
	parts := make([]string, 0, len(req.Messages)+2)
	if strings.TrimSpace(req.SystemPrompt) != "" {
		parts = append(parts, "[system]\n"+strings.TrimSpace(req.SystemPrompt))
	}
	for _, msg := range req.Messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			role = "user"
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("[%s]\n%s", role, content))
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func ensureSeenMaps(
	seenToolCalls map[string]map[string]bool,
	seenToolResults map[string]map[string]bool,
	messageID string,
) {
	if _, ok := seenToolCalls[messageID]; !ok {
		seenToolCalls[messageID] = map[string]bool{}
	}
	if _, ok := seenToolResults[messageID]; !ok {
		seenToolResults[messageID] = map[string]bool{}
	}
}

func emitToolEvents(
	ctx context.Context,
	rt agent.Runtime,
	msg *opencodesdk.MessageSnapshot,
	seenToolCalls map[string]map[string]bool,
	seenToolResults map[string]map[string]bool,
) {
	for _, call := range msg.ToolCalls {
		if !seenToolCalls[msg.ID][call.ID] {
			_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventToolCall, Data: agent.ToolCall{
				ID:        call.ID,
				Name:      call.Name,
				Arguments: call.Input,
			}})
			seenToolCalls[msg.ID][call.ID] = true
		}
	}
	for _, result := range msg.ToolResults {
		if !seenToolResults[msg.ID][result.ToolCallID] {
			_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventToolResult, Data: map[string]any{
				"tool_call_id": result.ToolCallID,
				"name":         result.Name,
				"content":      result.Content,
				"metadata":     result.Metadata,
				"is_error":     result.IsError,
			}})
			seenToolResults[msg.ID][result.ToolCallID] = true
		}
	}
}

func toAssistantMessageBlock(msg opencodesdk.MessageSnapshot) agent.MessageBlock {
	toolCalls := make([]agent.ToolCall, 0, len(msg.ToolCalls))
	for _, call := range msg.ToolCalls {
		toolCalls = append(toolCalls, agent.ToolCall{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: call.Input,
		})
	}
	return agent.MessageBlock{
		ID:        msg.ID,
		Content:   msg.Text,
		ToolCalls: toolCalls,
	}
}
