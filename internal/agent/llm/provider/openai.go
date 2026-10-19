package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

// openaiClient 是 OpenAI Chat Completions 协议客户端。
//
// 所有 OpenAI 兼容网关（deepseek / groq / openrouter / xai / 自建网关）共用它，
// 仅 BaseURL 与默认参数不同。
type openaiClient struct {
	opts   Options
	client openai.Client
}

func newOpenAIClient(opts Options) Client {
	requestOptions := []option.RequestOption{}
	if opts.APIKey != "" {
		requestOptions = append(requestOptions, option.WithAPIKey(opts.APIKey))
	}
	if baseURL := resolveBaseURL(opts); baseURL != "" {
		requestOptions = append(requestOptions, option.WithBaseURL(baseURL))
	}
	return &openaiClient{opts: opts, client: openai.NewClient(requestOptions...)}
}

// Stream 实现 Client：把增量文本 / 推理内容逐块发出，结束时发送完整聚合结果。
func (c *openaiClient) Stream(ctx context.Context, messages []Message, tools []Tool) <-chan Event {
	params := c.params(messages, tools)
	events := make(chan Event)

	go func() {
		defer close(events)
		attempts := 0
		for {
			attempts++
			stream := c.client.Chat.Completions.NewStreaming(ctx, params)
			acc := openai.ChatCompletionAccumulator{}
			content := ""

			for stream.Next() {
				chunk := stream.Current()
				acc.AddChunk(chunk)
				for _, choice := range chunk.Choices {
					if reasoning := reasoningDelta(choice.Delta); reasoning != "" {
						events <- Event{Type: EventThinkingDelta, Thinking: reasoning}
					}
					if choice.Delta.Content != "" {
						content += choice.Delta.Content
						events <- Event{Type: EventContentDelta, Content: choice.Delta.Content}
					}
				}
			}

			err := stream.Err()
			if err == nil || errors.Is(err, io.EOF) {
				events <- Event{Type: EventComplete, Response: c.response(acc, content)}
				return
			}

			retry, after, retryErr := shouldRetryOpenAI(attempts, err)
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

func (c *openaiClient) params(messages []Message, tools []Tool) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(c.apiModel()),
		Messages: c.convertMessages(messages),
		Tools:    c.convertTools(tools),
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	}
	if c.opts.Model.CanReason {
		params.MaxCompletionTokens = openai.Int(c.opts.MaxTokens)
		params.ReasoningEffort = openaiReasoningEffort(c.opts.ReasoningEffort)
	} else {
		params.MaxTokens = openai.Int(c.opts.MaxTokens)
	}
	return params
}

func (c *openaiClient) apiModel() string {
	if c.opts.Model.APIModel != "" {
		return c.opts.Model.APIModel
	}
	return string(c.opts.Model.ID)
}

func (c *openaiClient) convertMessages(messages []Message) []openai.ChatCompletionMessageParamUnion {
	out := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages)+1)
	if c.opts.SystemPrompt != "" {
		out = append(out, openai.SystemMessage(c.opts.SystemPrompt))
	}

	for _, msg := range messages {
		switch msg.Role {
		case RoleUser:
			out = append(out, openai.UserMessage(msg.Content))

		case RoleAssistant:
			assistant := openai.ChatCompletionAssistantMessageParam{}
			if msg.Content != "" {
				assistant.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openai.String(msg.Content),
				}
			}
			if len(msg.ToolCalls) > 0 {
				assistant.ToolCalls = make([]openai.ChatCompletionMessageToolCallParam, 0, len(msg.ToolCalls))
				for _, call := range msg.ToolCalls {
					assistant.ToolCalls = append(assistant.ToolCalls, openai.ChatCompletionMessageToolCallParam{
						ID:   call.ID,
						Type: "function",
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      call.Name,
							Arguments: call.Input,
						},
					})
				}
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant})

		case RoleTool:
			for _, result := range msg.ToolResults {
				out = append(out, openai.ToolMessage(result.Content, result.ToolCallID))
			}
		}
	}
	return out
}

func (c *openaiClient) convertTools(tools []Tool) []openai.ChatCompletionToolParam {
	out := make([]openai.ChatCompletionToolParam, 0, len(tools))
	for _, t := range tools {
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, openai.ChatCompletionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name:        t.Name,
				Description: openai.String(t.Description),
				Parameters:  openai.FunctionParameters(schema),
			},
		})
	}
	return out
}

func (c *openaiClient) response(acc openai.ChatCompletionAccumulator, content string) *Response {
	completion := acc.ChatCompletion
	finishReason := FinishUnknown
	toolCalls := make([]ToolCall, 0)
	if len(completion.Choices) > 0 {
		finishReason = openaiFinishReason(string(completion.Choices[0].FinishReason))
		for _, call := range completion.Choices[0].Message.ToolCalls {
			toolCalls = append(toolCalls, ToolCall{
				ID:    call.ID,
				Name:  call.Function.Name,
				Input: call.Function.Arguments,
			})
		}
	}
	if len(toolCalls) > 0 {
		finishReason = FinishToolUse
	}

	cachedTokens := completion.Usage.PromptTokensDetails.CachedTokens
	return &Response{
		Content:      content,
		ToolCalls:    toolCalls,
		FinishReason: finishReason,
		Usage: Usage{
			InputTokens:     completion.Usage.PromptTokens - cachedTokens,
			OutputTokens:    completion.Usage.CompletionTokens,
			CacheReadTokens: cachedTokens,
		},
	}
}

func openaiFinishReason(reason string) FinishReason {
	switch reason {
	case "stop":
		return FinishEndTurn
	case "length":
		return FinishMaxToken
	case "tool_calls":
		return FinishToolUse
	default:
		return FinishUnknown
	}
}

func openaiReasoningEffort(effort string) shared.ReasoningEffort {
	switch effort {
	case "low":
		return shared.ReasoningEffortLow
	case "high":
		return shared.ReasoningEffortHigh
	default:
		return shared.ReasoningEffortMedium
	}
}

// reasoningDelta 从 OpenAI 兼容网关的非标准流式字段中提取推理增量。
//
// DeepSeek 等使用 reasoning_content，部分 OpenAI 模型使用 reasoning；二者都不在
// openai-go 的类型定义里，只能从 chunk 的 ExtraFields 读取。
//
// 注意：非标准字段的 IsPresent() 恒为 false，必须直接读取 Raw() 再反序列化。
func reasoningDelta(delta openai.ChatCompletionChunkChoiceDelta) string {
	for _, key := range []string{"reasoning_content", "reasoning"} {
		field, ok := delta.JSON.ExtraFields[key]
		if !ok {
			continue
		}
		raw := field.Raw()
		if raw == "" || raw == "null" {
			continue
		}
		var value string
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			continue
		}
		return value
	}
	return ""
}

func shouldRetryOpenAI(attempts int, err error) (bool, int64, error) {
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		return false, 0, err
	}
	if apiErr.StatusCode != 429 && apiErr.StatusCode != 500 {
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
