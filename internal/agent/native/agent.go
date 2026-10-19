// Package native 是 gobrave 的原生 Agent 执行循环。
//
// 迁移自 opencode internal/llm/agent/agent.go，但做了两点简化：
//   - 不再持有 session / message 服务：会话持久化由上层 AgentService 负责，
//     本包只消费 Request.Messages 并在内存中维护工具调用往返；
//   - 工具统一走 gobrave 的 tool.Registry / tool.Executor，权限、技能、文件历史等
//     服务能力由调用方通过 PrepareToolContext 注入。
package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/agent/llm/provider"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/google/uuid"
)

// maxIterations 限制单次请求内的「模型 → 工具 → 模型」往返轮数，防止死循环。
const maxIterations = 64

// Config 是一次原生 Agent 执行所需的依赖。
type Config struct {
	Client provider.Client
	Tools  *tool.Registry
	// PrepareToolContext 在每次工具执行前装饰 ctx（注入权限、运行时上下文、技能等）。
	PrepareToolContext func(context.Context) context.Context
}

// Run 执行一次完整请求：模型流式输出 → 执行工具 → 回填结果 → 继续，直至模型不再调用工具。
func (c *Config) Run(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	if c.Client == nil {
		return nil, fmt.Errorf("native agent: provider client is required")
	}
	if rt == nil {
		rt = agent.NewStandaloneRuntime(nil)
	}

	executor := tool.NewExecutor(c.Tools)
	history := requestMessages(req)
	tools := toolDefinitions(c.Tools)

	var final string
	for i := 0; i < maxIterations; i++ {
		response, thinking, err := c.streamOnce(ctx, history, tools, rt)
		if err != nil {
			return nil, err
		}

		// 完整思考块 / 消息块：作为时间线锚点持久化（增量已实时下发）。
		if strings.TrimSpace(thinking) != "" {
			_ = rt.Emit(ctx, agent.StreamEvent{
				Type: agent.StreamEventReasoning,
				Data: agent.ReasoningBlock{ID: uuid.NewString(), Content: thinking},
			})
		}
		if strings.TrimSpace(response.Content) != "" {
			_ = rt.Emit(ctx, agent.StreamEvent{
				Type: agent.StreamEventMessage,
				Data: agent.MessageBlock{ID: uuid.NewString(), Content: response.Content},
			})
		}
		final = response.Content

		history = append(history, provider.Message{
			Role:      provider.RoleAssistant,
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		})

		// 没有工具调用：本轮结束。
		if len(response.ToolCalls) == 0 {
			_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventDone})
			return &agent.Result{
				Content: final,
				Usage:   agent.Usage{InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens},
			}, nil
		}

		results := c.executeTools(ctx, executor, response.ToolCalls, rt)
		history = append(history, provider.Message{Role: provider.RoleTool, ToolResults: results})
	}

	return nil, fmt.Errorf("native agent: exceeded %d tool iterations", maxIterations)
}

// streamOnce 执行一轮模型调用，转发增量事件并返回聚合结果。
func (c *Config) streamOnce(ctx context.Context, history []provider.Message, tools []provider.Tool, rt agent.Runtime) (*provider.Response, string, error) {
	stream := c.Client.Stream(ctx, history, tools)
	var thinking strings.Builder

	for event := range stream {
		switch event.Type {
		case provider.EventContentDelta:
			if event.Content != "" {
				_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventText, Content: event.Content})
			}
		case provider.EventThinkingDelta:
			if event.Thinking != "" {
				thinking.WriteString(event.Thinking)
				_ = rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventReasoningDelta, Content: event.Thinking})
			}
		case provider.EventError:
			if event.Error == nil {
				return nil, thinking.String(), fmt.Errorf("native agent: provider returned empty error")
			}
			return nil, thinking.String(), event.Error
		case provider.EventComplete:
			if event.Response == nil {
				return nil, thinking.String(), fmt.Errorf("native agent: provider returned empty response")
			}
			return event.Response, thinking.String(), nil
		}
	}
	return nil, thinking.String(), fmt.Errorf("native agent: stream ended without completion")
}

// executeTools 逐个执行工具调用，并把调用 / 结果作为完整块事件下发。
func (c *Config) executeTools(ctx context.Context, executor *tool.Executor, calls []provider.ToolCall, rt agent.Runtime) []provider.ToolResult {
	results := make([]provider.ToolResult, 0, len(calls))
	for _, call := range calls {
		_ = rt.Emit(ctx, agent.StreamEvent{
			Type: agent.StreamEventToolCall,
			Data: agent.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Input},
		})

		toolCtx := ctx
		if c.PrepareToolContext != nil {
			toolCtx = c.PrepareToolContext(ctx)
		}
		res := executor.Execute(toolCtx, tool.Call{
			ID:        call.ID,
			Name:      call.Name,
			Arguments: json.RawMessage(call.Input),
		})

		_ = rt.Emit(ctx, agent.StreamEvent{
			Type: agent.StreamEventToolResult,
			Data: map[string]any{
				"tool_call_id": call.ID,
				"name":         call.Name,
				"content":      res.Content,
				"is_error":     res.IsError,
			},
		})
		results = append(results, provider.ToolResult{
			ToolCallID: call.ID,
			Content:    res.Content,
			IsError:    res.IsError,
		})
	}
	return results
}

// requestMessages 把请求历史转换为模型消息：只保留 user / assistant 文本。
func requestMessages(req agent.Request) []provider.Message {
	out := make([]provider.Message, 0, len(req.Messages))
	for _, msg := range req.Messages {
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case agent.RoleUser:
			out = append(out, provider.Message{Role: provider.RoleUser, Content: msg.Content})
		case agent.RoleAssistant:
			out = append(out, provider.Message{Role: provider.RoleAssistant, Content: msg.Content})
		}
	}
	return out
}

// toolDefinitions 把 gobrave 工具定义转换为 Provider 侧的工具定义。
func toolDefinitions(registry *tool.Registry) []provider.Tool {
	if registry == nil {
		return nil
	}
	defs := registry.List()
	out := make([]provider.Tool, 0, len(defs))
	for _, def := range defs {
		out = append(out, provider.Tool{
			Name:        def.Name,
			Description: def.Description,
			InputSchema: def.InputSchema,
		})
	}
	return out
}
