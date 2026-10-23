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
	"sync"

	"github.com/biox-dev/gobrave/internal/agent/llm/provider"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/logger"
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
func (c *Config) Run(ctx context.Context, req agenttypes.Request, rt agentinterfaces.Runtime) (*agenttypes.Result, error) {
	if c.Client == nil {
		err := fmt.Errorf("native agent: provider client is required")
		logger.Errorf(ctx, "native agent: %v", err)
		return nil, err
	}
	if rt == nil {
		// Runtime 由调用方（AgentService 的 taskRuntime）保证非 nil：事件、权限、
		// 提问都必须有明确的任务归属，这里不做“无任务”兜底。
		err := fmt.Errorf("native agent: runtime is required")
		logger.Errorf(ctx, "native agent: %v", err)
		return nil, err
	}

	executor := tool.NewExecutor(c.Tools)
	tools := toolDefinitions(c.Tools)

	// 检查点（任务模式下可用）：先尝试从持久化状态恢复未完成的工具往返。
	cw := newCheckpointWriter(rt)
	history, resumed := c.restoreCheckpoint(ctx, executor, rt, cw)
	if !resumed {
		history = requestMessages(req)
	}

	var final string
	for i := 0; i < maxIterations; i++ {
		response, thinking, err := c.streamOnce(ctx, history, tools, rt)
		if err != nil {
			logger.Errorf(ctx, "native agent: iteration %d failed: %v", i+1, err)
			return nil, err
		}

		// 完整思考块 / 消息块：作为时间线锚点持久化（增量已实时下发）。
		if strings.TrimSpace(thinking) != "" {
			_ = rt.Emit(ctx, agenttypes.StreamEvent{
				Type: agenttypes.StreamEventReasoning,
				Data: agenttypes.ReasoningBlock{ID: uuid.NewString(), Content: thinking},
			})
		}
		if strings.TrimSpace(response.Content) != "" {
			_ = rt.Emit(ctx, agenttypes.StreamEvent{
				Type: agenttypes.StreamEventMessage,
				Data: agenttypes.MessageBlock{ID: uuid.NewString(), Content: response.Content},
			})
		}
		final = response.Content

		// 没有工具调用：本轮结束。正常结束必须清理检查点，避免任务被误判为可续跑。
		if len(response.ToolCalls) == 0 {
			history = append(history, provider.Message{
				Role:      provider.RoleAssistant,
				Content:   response.Content,
				ToolCalls: nil,
			})
			cw.clear(ctx)
			_ = rt.Emit(ctx, agenttypes.StreamEvent{Type: agenttypes.StreamEventDone})
			return &agenttypes.Result{
				Content: final,
				Usage:   agenttypes.Usage{InputTokens: response.Usage.InputTokens, OutputTokens: response.Usage.OutputTokens},
			}, nil
		}

		// 在「模型已响应、工具尚未执行」这一安全点落检查点：
		// 若工具在等待权限时进程退出，重启后可据此精确续跑。
		state := newResumeState(req, history, response)
		cw.save(ctx, state)

		// 每完成一个工具就把结果合并进检查点：正常重启不会重复执行已完成的工具。
		results := c.executeTools(ctx, executor, response.ToolCalls, rt, func(_ provider.ToolCall, res provider.ToolResult) {
			state.MarkToolResult(toResumeToolResult(res))
			cw.save(ctx, state)
		})

		history = append(history, provider.Message{
			Role:      provider.RoleAssistant,
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		})
		history = append(history, provider.Message{Role: provider.RoleTool, ToolResults: results})
		state.CompleteTurn()
		cw.save(ctx, state)
	}

	err := fmt.Errorf("native agent: exceeded %d tool iterations", maxIterations)
	logger.Errorf(ctx, "native agent: %v", err)
	return nil, err
}

// restoreCheckpoint 从持久化检查点恢复执行现场。
//
// 返回 ok=false 表示没有可用检查点，调用方应使用请求自带的历史重新开始。
// 有检查点时：
//   - History 还原为已完成的多轮对话；
//   - Pending 中的 assistant 响应重新入栈；
//   - 已完成结果的工具直接复用，未完成的工具重新执行（权限 / 提问会按 tool_call_id 复用决策）。
func (c *Config) restoreCheckpoint(ctx context.Context, executor *tool.Executor, rt agentinterfaces.Runtime, cw *checkpointWriter) ([]provider.Message, bool) {
	if !cw.enabled() {
		return nil, false
	}
	state := cw.load(ctx)
	if state == nil || state.Version != agenttypes.ResumeStateVersion {
		return nil, false
	}
	// 空检查点没有恢复价值：回退到请求自带历史，避免让模型收到空对话。
	if state.Pending == nil && len(state.History) == 0 {
		return nil, false
	}

	history := fromResumeHistory(state.History)
	if state.Pending == nil {
		return history, true
	}

	logger.Infof(ctx, "native agent: resuming from checkpoint (history=%d, pending_tools=%d)",
		len(history), len(state.Pending.Assistant.ToolCalls))

	assistant := fromResumeMessage(state.Pending.Assistant)
	results := make([]provider.ToolResult, 0, len(assistant.ToolCalls))
	for _, call := range assistant.ToolCalls {
		if done, ok := state.CompletedResult(call.ID); ok {
			results = append(results, fromResumeToolResult(done))
			continue
		}
		res := c.executeTool(ctx, executor, call)
		c.emitToolResult(ctx, rt, call, res)
		results = append(results, res)
		state.MarkToolResult(toResumeToolResult(res))
		cw.save(ctx, state)
	}

	history = append(history, assistant)
	history = append(history, provider.Message{Role: provider.RoleTool, ToolResults: results})
	state.CompleteTurn()
	cw.save(ctx, state)
	return history, true
}

// streamOnce 执行一轮模型调用，转发增量事件并返回聚合结果。
func (c *Config) streamOnce(ctx context.Context, history []provider.Message, tools []provider.Tool, rt agentinterfaces.Runtime) (*provider.Response, string, error) {
	stream := c.Client.Stream(ctx, history, tools)
	var thinking strings.Builder

	for event := range stream {
		switch event.Type {
		case provider.EventContentDelta:
			if event.Content != "" {
				_ = rt.Emit(ctx, agenttypes.StreamEvent{Type: agenttypes.StreamEventText, Content: event.Content})
			}
		case provider.EventThinkingDelta:
			if event.Thinking != "" {
				thinking.WriteString(event.Thinking)
				_ = rt.Emit(ctx, agenttypes.StreamEvent{Type: agenttypes.StreamEventReasoningDelta, Content: event.Thinking})
			}
		case provider.EventError:
			if event.Error == nil {
				err := fmt.Errorf("native agent: provider returned empty error")
				logger.Errorf(ctx, "native agent: %v", err)
				return nil, thinking.String(), err
			}
			logger.Errorf(ctx, "native agent: provider stream error: %v", event.Error)
			return nil, thinking.String(), event.Error
		case provider.EventComplete:
			if event.Response == nil {
				err := fmt.Errorf("native agent: provider returned empty response")
				logger.Errorf(ctx, "native agent: %v", err)
				return nil, thinking.String(), err
			}
			return event.Response, thinking.String(), nil
		}
	}
	err := fmt.Errorf("native agent: stream ended without completion")
	logger.Errorf(ctx, "native agent: %v", err)
	return nil, thinking.String(), err
}

// maxConcurrentAgentCalls 限制同一轮内并发执行的子 Agent 调用数。
//
// 只对 agent 工具生效：子 Agent 彼此独立且只读；其它工具仍顺序执行，避免同一文件上的
// 写操作互相覆盖。
const maxConcurrentAgentCalls = 4

// executeTools 执行本轮的多个工具调用。
//
//   - agent 工具调用彼此独立，允许并发执行，缩短多子 Agent 场景的总耗时；
//   - 其它工具保持顺序执行，避免 bash / edit / write 等写操作互相干扰；
//   - 结果按模型给出的调用顺序回填（与 tool_call_id 一一对应），并发完成顺序不影响语义。
//   - onResult 在每个工具完成时回调，用于把结果增量写入持久化检查点（可为 nil）。
func (c *Config) executeTools(ctx context.Context, executor *tool.Executor, calls []provider.ToolCall, rt agentinterfaces.Runtime, onResult func(provider.ToolCall, provider.ToolResult)) []provider.ToolResult {
	results := make([]provider.ToolResult, len(calls))

	// 先按顺序下发全部 tool_call 事件，保证时间线里调用顺序与模型输出一致。
	for _, call := range calls {
		_ = rt.Emit(ctx, agenttypes.StreamEvent{
			Type: agenttypes.StreamEventToolCall,
			Data: agenttypes.ToolCall{ID: call.ID, Name: call.Name, Arguments: call.Input},
		})
	}

	agentCalls := 0
	for _, call := range calls {
		if call.Name == AgentToolName {
			agentCalls++
		}
	}

	// 并发执行 agent 调用；完成一个就通知主协程下发该调用的结果事件。
	done := make(chan int, agentCalls)
	var wg sync.WaitGroup
	sem := make(chan struct{}, maxConcurrentAgentCalls)
	for i, call := range calls {
		if call.Name != AgentToolName {
			continue
		}
		wg.Add(1)
		go func(i int, call provider.ToolCall) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = provider.ToolResult{
					ToolCallID: call.ID,
					Content:    "Tool execution canceled by user",
					IsError:    true,
				}
				done <- i
				return
			}
			results[i] = c.executeTool(ctx, executor, call)
			done <- i
		}(i, call)
	}
	go func() {
		wg.Wait()
		close(done)
	}()

	// 主协程顺序执行其它工具，并即时下发它们的结果事件。
	for i, call := range calls {
		if call.Name == AgentToolName {
			continue
		}
		results[i] = c.executeTool(ctx, executor, call)
		c.emitToolResult(ctx, rt, call, results[i])
		if onResult != nil {
			onResult(call, results[i])
		}
	}
	// 子 Agent 结果按完成顺序下发（tool_call_id 仍能正确对应）。
	for i := range done {
		c.emitToolResult(ctx, rt, calls[i], results[i])
		if onResult != nil {
			onResult(calls[i], results[i])
		}
	}
	return results
}

// executeTool 执行单个工具调用（注入工具执行上下文）。
func (c *Config) executeTool(ctx context.Context, executor *tool.Executor, call provider.ToolCall) provider.ToolResult {
	// 工具调用 ID 贯穿权限 / 提问 / 工具执行全链路：重启恢复时据此复用已落库的决策。
	ctx = agenttypes.WithToolCallID(ctx, call.ID)

	toolCtx := ctx
	if c.PrepareToolContext != nil {
		toolCtx = c.PrepareToolContext(ctx)
	}
	res := executor.Execute(toolCtx, tool.Call{
		ID:        call.ID,
		Name:      call.Name,
		Arguments: json.RawMessage(call.Input),
	})
	if res.IsError {
		logger.Errorf(ctx, "native agent: tool %q failed: %s", call.Name, truncateLog(res.Content, 2000))
	}
	return provider.ToolResult{
		ToolCallID: call.ID,
		Content:    res.Content,
		IsError:    res.IsError,
	}
}

// truncateLog 截断过长的错误文本，避免工具输出把日志刷爆。
func truncateLog(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…(truncated)"
}

// emitToolResult 下发一次完整的工具结果事件。
func (c *Config) emitToolResult(ctx context.Context, rt agentinterfaces.Runtime, call provider.ToolCall, res provider.ToolResult) {
	_ = rt.Emit(ctx, agenttypes.StreamEvent{
		Type: agenttypes.StreamEventToolResult,
		Data: map[string]any{
			"tool_call_id": call.ID,
			"name":         call.Name,
			"content":      res.Content,
			"is_error":     res.IsError,
		},
	})
}

// requestMessages 把请求历史转换为模型消息：只保留 user / assistant 文本。
func requestMessages(req agenttypes.Request) []provider.Message {
	out := make([]provider.Message, 0, len(req.Messages))
	for _, msg := range req.Messages {
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case agenttypes.RoleUser:
			out = append(out, provider.Message{Role: provider.RoleUser, Content: msg.Content})
		case agenttypes.RoleAssistant:
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
