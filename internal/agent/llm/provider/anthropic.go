package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// anthropicClient 是 Anthropic Messages 协议客户端（Claude）。
type anthropicClient struct {
	opts   Options
	client anthropic.Client
}

func newAnthropicClient(opts Options) Client {
	requestOptions := []option.RequestOption{}
	if opts.APIKey != "" {
		requestOptions = append(requestOptions, option.WithAPIKey(opts.APIKey))
	}
	return &anthropicClient{opts: opts, client: anthropic.NewClient(requestOptions...)}
}

// Stream 实现 Client。
func (c *anthropicClient) Stream(ctx context.Context, messages []Message, tools []Tool) <-chan Event {
	params := c.params(messages, tools)
	events := make(chan Event)

	go func() {
		defer close(events)
		attempts := 0
		for {
			attempts++
			stream := c.client.Messages.NewStreaming(ctx, params)
			acc := anthropic.Message{}

			for stream.Next() {
				event := stream.Current()
				if err := acc.Accumulate(event); err != nil {
					continue
				}
				switch e := event.AsAny().(type) {
				case anthropic.ContentBlockDeltaEvent:
					switch e.Delta.Type {
					case "thinking_delta":
						if e.Delta.Thinking != "" {
							events <- Event{Type: EventThinkingDelta, Thinking: e.Delta.Thinking}
						}
					case "text_delta":
						if e.Delta.Text != "" {
							events <- Event{Type: EventContentDelta, Content: e.Delta.Text}
						}
					}
				case anthropic.MessageStopEvent:
					events <- Event{Type: EventComplete, Response: c.response(acc)}
				}
			}

			err := stream.Err()
			if err == nil || errors.Is(err, io.EOF) {
				return
			}

			retry, after, retryErr := shouldRetryAnthropic(attempts, err)
			if retryErr != nil {
				events <- Event{Type: EventError, Error: retryErr}
				return
			}
			if retry {
				select {
				case <-ctx.Done():
					events <- Event{Type: EventError, Error: ctx.Err()}
					return
				case <-time.After(time.Duration(after) * time.Millisecond):
					continue
				}
			}
			events <- Event{Type: EventError, Error: retryErr}
			return
		}
	}()

	return events
}

func (c *anthropicClient) params(messages []Message, tools []Tool) anthropic.MessageNewParams {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(c.apiModel()),
		MaxTokens: c.opts.MaxTokens,
		Messages:  c.convertMessages(messages),
		Tools:     c.convertTools(tools),
	}
	if c.opts.SystemPrompt != "" {
		params.System = []anthropic.TextBlockParam{{
			Text:         c.opts.SystemPrompt,
			CacheControl: anthropic.CacheControlEphemeralParam{Type: "ephemeral"},
		}}
	}
	return params
}

func (c *anthropicClient) apiModel() string {
	if c.opts.Model.APIModel != "" {
		return c.opts.Model.APIModel
	}
	return string(c.opts.Model.ID)
}

func (c *anthropicClient) convertMessages(messages []Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case RoleUser:
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(msg.Content)))

		case RoleAssistant:
			blocks := make([]anthropic.ContentBlockParamUnion, 0, len(msg.ToolCalls)+1)
			if msg.Content != "" {
				blocks = append(blocks, anthropic.NewTextBlock(msg.Content))
			}
			for _, call := range msg.ToolCalls {
				var input map[string]any
				if err := json.Unmarshal([]byte(call.Input), &input); err != nil {
					continue
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(call.ID, input, call.Name))
			}
			if len(blocks) > 0 {
				out = append(out, anthropic.NewAssistantMessage(blocks...))
			}

		case RoleTool:
			results := make([]anthropic.ContentBlockParamUnion, 0, len(msg.ToolResults))
			for _, result := range msg.ToolResults {
				results = append(results, anthropic.NewToolResultBlock(result.ToolCallID, result.Content, result.IsError))
			}
			if len(results) > 0 {
				out = append(out, anthropic.NewUserMessage(results...))
			}
		}
	}
	return out
}

func (c *anthropicClient) convertTools(tools []Tool) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for i, t := range tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		properties, _ := schema["properties"].(map[string]any)
		toolParam := anthropic.ToolParam{
			Name:        t.Name,
			Description: anthropic.String(t.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: properties,
				Required:   stringSlice(schema["required"]),
			},
		}
		// 缓存最后一个工具的 schema，减少多轮调用时的重复计费。
		if i == len(tools)-1 {
			toolParam.CacheControl = anthropic.CacheControlEphemeralParam{Type: "ephemeral"}
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &toolParam})
	}
	return out
}

func (c *anthropicClient) response(msg anthropic.Message) *Response {
	content := ""
	for _, block := range msg.Content {
		if text, ok := block.AsAny().(anthropic.TextBlock); ok {
			content += text.Text
		}
	}
	toolCalls := make([]ToolCall, 0)
	for _, block := range msg.Content {
		if use, ok := block.AsAny().(anthropic.ToolUseBlock); ok {
			toolCalls = append(toolCalls, ToolCall{
				ID:    use.ID,
				Name:  use.Name,
				Input: string(use.Input),
			})
		}
	}
	finishReason := FinishUnknown
	switch string(msg.StopReason) {
	case "end_turn", "stop_sequence":
		finishReason = FinishEndTurn
	case "max_tokens":
		finishReason = FinishMaxToken
	case "tool_use":
		finishReason = FinishToolUse
	}
	if len(toolCalls) > 0 {
		finishReason = FinishToolUse
	}

	return &Response{
		Content:      content,
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
		Usage: Usage{
			InputTokens:         msg.Usage.InputTokens,
			OutputTokens:        msg.Usage.OutputTokens,
			CacheCreationTokens: msg.Usage.CacheCreationInputTokens,
			CacheReadTokens:     msg.Usage.CacheReadInputTokens,
		},
	}
}

func shouldRetryAnthropic(attempts int, err error) (bool, int64, error) {
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		return false, 0, err
	}
	if apiErr.StatusCode != 429 && apiErr.StatusCode != 529 {
		return false, 0, err
	}
	if attempts > maxRetries {
		return false, 0, fmt.Errorf("maximum retry attempts reached: %d retries", maxRetries)
	}

	retryMs := 2000 * (1 << (attempts - 1))
	retryMs += retryMs / 5
	if values := apiErr.Response.Header.Values("Retry-After"); len(values) > 0 {
		var seconds int
		if _, scanErr := fmt.Sscanf(values[0], "%d", &seconds); scanErr == nil {
			retryMs = seconds * 1000
		}
	}
	return true, int64(retryMs), nil
}
