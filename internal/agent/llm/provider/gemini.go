package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"google.golang.org/genai"
)

// geminiClient 是 Google Gemini 协议客户端。
type geminiClient struct {
	opts   Options
	client *genai.Client
}

func newGeminiClient(opts Options) (Client, error) {
	cfg := &genai.ClientConfig{APIKey: opts.APIKey, Backend: genai.BackendGeminiAPI}
	if baseURL := strings.TrimSpace(opts.BaseURL); baseURL != "" {
		cfg.HTTPOptions = genai.HTTPOptions{BaseURL: baseURL}
	}
	client, err := genai.NewClient(context.Background(), cfg)
	if err != nil {
		return nil, fmt.Errorf("provider: init gemini client: %w", err)
	}
	return &geminiClient{opts: opts, client: client}, nil
}

// Stream 实现 Client。
func (g *geminiClient) Stream(ctx context.Context, messages []Message, tools []Tool) <-chan Event {
	events := make(chan Event)
	go func() {
		defer close(events)

		history := g.convertMessages(messages)
		if len(history) == 0 {
			events <- Event{Type: EventError, Error: errors.New("provider: gemini requires at least one message")}
			return
		}
		last := history[len(history)-1]
		chat, err := g.client.Chats.Create(ctx, g.apiModel(), g.config(tools), history[:len(history)-1])
		if err != nil {
			events <- Event{Type: EventError, Error: err}
			return
		}

		attempts := 0
		for {
			attempts++
			content := ""
			toolCalls := make([]ToolCall, 0)
			var finalResp *genai.GenerateContentResponse
			var streamErr error

			parts := make([]genai.Part, 0, len(last.Parts))
			for _, part := range last.Parts {
				parts = append(parts, *part)
			}
			for resp, sendErr := range chat.SendMessageStream(ctx, parts...) {
				if sendErr != nil {
					streamErr = sendErr
					break
				}
				finalResp = resp
				for _, part := range candidateParts(resp) {
					if part.Text != "" {
						content += part.Text
						events <- Event{Type: EventContentDelta, Content: part.Text}
					}
					if part.FunctionCall != nil {
						call := genaiToolCall(part)
						if !containsToolCall(toolCalls, call) {
							toolCalls = append(toolCalls, call)
						}
					}
				}
			}

			if streamErr == nil {
				if finalResp == nil {
					events <- Event{Type: EventError, Error: errors.New("provider: gemini returned no response")}
					return
				}
				events <- Event{Type: EventComplete, Response: g.response(finalResp, content, toolCalls)}
				return
			}

			retry, after, retryErr := shouldRetryGemini(attempts, streamErr)
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

func (g *geminiClient) apiModel() string {
	if g.opts.Model.APIModel != "" {
		return g.opts.Model.APIModel
	}
	return string(g.opts.Model.ID)
}

func (g *geminiClient) config(tools []Tool) *genai.GenerateContentConfig {
	cfg := &genai.GenerateContentConfig{MaxOutputTokens: int32(g.opts.MaxTokens)}
	if g.opts.SystemPrompt != "" {
		cfg.SystemInstruction = &genai.Content{Parts: []*genai.Part{{Text: g.opts.SystemPrompt}}}
	}
	if len(tools) > 0 {
		declarations := make([]*genai.FunctionDeclaration, 0, len(tools))
		for _, t := range tools {
			schema := t.InputSchema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			properties, _ := schema["properties"].(map[string]any)
			required := stringSlice(schema["required"])
			declarations = append(declarations, &genai.FunctionDeclaration{
				Name:        t.Name,
				Description: t.Description,
				Parameters: &genai.Schema{
					Type:       genai.TypeObject,
					Properties: convertSchemaProperties(properties),
					Required:   required,
				},
			})
		}
		cfg.Tools = []*genai.Tool{{FunctionDeclarations: declarations}}
	}
	return cfg
}

func (g *geminiClient) convertMessages(messages []Message) []*genai.Content {
	out := make([]*genai.Content, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case RoleUser:
			out = append(out, &genai.Content{Role: "user", Parts: []*genai.Part{{Text: msg.Content}}})

		case RoleAssistant:
			parts := make([]*genai.Part, 0, len(msg.ToolCalls)+1)
			if msg.Content != "" {
				parts = append(parts, &genai.Part{Text: msg.Content})
			}
			for _, call := range msg.ToolCalls {
				args, _ := parseJSONMap(call.Input)
				parts = append(parts, &genai.Part{FunctionCall: &genai.FunctionCall{Name: call.Name, Args: args}})
			}
			if len(parts) > 0 {
				out = append(out, &genai.Content{Role: "model", Parts: parts})
			}

		case RoleTool:
			for _, result := range msg.ToolResults {
				response, err := parseJSONMap(result.Content)
				if err != nil {
					response = map[string]any{"result": result.Content}
				}
				out = append(out, &genai.Content{
					Role: "function",
					Parts: []*genai.Part{{
						FunctionResponse: &genai.FunctionResponse{
							Name:     toolCallName(messages, result.ToolCallID),
							Response: response,
						},
					}},
				})
			}
		}
	}
	return out
}

func (g *geminiClient) response(resp *genai.GenerateContentResponse, content string, toolCalls []ToolCall) *Response {
	finishReason := FinishEndTurn
	if len(resp.Candidates) > 0 {
		switch resp.Candidates[0].FinishReason {
		case genai.FinishReasonMaxTokens:
			finishReason = FinishMaxToken
		}
	}
	if len(toolCalls) > 0 {
		finishReason = FinishToolUse
	}

	usage := Usage{}
	if resp.UsageMetadata != nil {
		usage = Usage{
			InputTokens:     int64(resp.UsageMetadata.PromptTokenCount),
			OutputTokens:    int64(resp.UsageMetadata.CandidatesTokenCount),
			CacheReadTokens: int64(resp.UsageMetadata.CachedContentTokenCount),
		}
	}
	return &Response{Content: content, ToolCalls: toolCalls, FinishReason: finishReason, Usage: usage}
}

func candidateParts(resp *genai.GenerateContentResponse) []*genai.Part {
	if resp == nil || len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return nil
	}
	return resp.Candidates[0].Content.Parts
}

func genaiToolCall(part *genai.Part) ToolCall {
	args, _ := json.Marshal(part.FunctionCall.Args)
	return ToolCall{ID: "call_" + uuid.NewString(), Name: part.FunctionCall.Name, Input: string(args)}
}

func containsToolCall(calls []ToolCall, call ToolCall) bool {
	for _, existing := range calls {
		if existing.Name == call.Name && existing.Input == call.Input {
			return true
		}
	}
	return false
}

// toolCallName 在历史消息中反查某个 tool_call_id 对应的工具名（Gemini 的
// FunctionResponse 只接受名称，不使用 ID）。
func toolCallName(messages []Message, toolCallID string) string {
	for _, msg := range messages {
		if msg.Role != RoleAssistant {
			continue
		}
		for _, call := range msg.ToolCalls {
			if call.ID == toolCallID {
				return call.Name
			}
		}
	}
	return ""
}

func shouldRetryGemini(attempts int, err error) (bool, int64, error) {
	if attempts > maxRetries {
		return false, 0, fmt.Errorf("maximum retry attempts reached: %d retries", maxRetries)
	}
	if errors.Is(err, io.EOF) {
		return false, 0, err
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "rate limit") && !strings.Contains(msg, "quota exceeded") && !strings.Contains(msg, "too many requests") {
		return false, 0, err
	}
	retryMs := 2000 * (1 << (attempts - 1))
	retryMs += retryMs / 5
	return true, int64(retryMs), nil
}

func parseJSONMap(raw string) (map[string]any, error) {
	var result map[string]any
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	err := json.Unmarshal([]byte(raw), &result)
	return result, err
}

func stringSlice(value any) []string {
	switch v := value.(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func convertSchemaProperties(parameters map[string]any) map[string]*genai.Schema {
	properties := make(map[string]*genai.Schema, len(parameters))
	for name, param := range parameters {
		properties[name] = convertToSchema(param)
	}
	return properties
}

func convertToSchema(param any) *genai.Schema {
	schema := &genai.Schema{Type: genai.TypeString}
	paramMap, ok := param.(map[string]any)
	if !ok {
		return schema
	}
	if description, ok := paramMap["description"].(string); ok {
		schema.Description = description
	}
	typeStr, ok := paramMap["type"].(string)
	if !ok {
		return schema
	}
	schema.Type = mapJSONTypeToGenAI(typeStr)
	switch typeStr {
	case "array":
		if items, ok := paramMap["items"].(map[string]any); ok {
			schema.Items = convertToSchema(items)
		}
	case "object":
		if props, ok := paramMap["properties"].(map[string]any); ok {
			schema.Properties = convertSchemaProperties(props)
		}
	}
	return schema
}

func mapJSONTypeToGenAI(jsonType string) genai.Type {
	switch jsonType {
	case "number":
		return genai.TypeNumber
	case "integer":
		return genai.TypeInteger
	case "boolean":
		return genai.TypeBoolean
	case "array":
		return genai.TypeArray
	case "object":
		return genai.TypeObject
	default:
		return genai.TypeString
	}
}
