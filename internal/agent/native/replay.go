package native

import (
	"context"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/llm/provider"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/logger"
)

// 本文件负责「任务中断后重建执行现场」。
//
// 恢复的事实只有一个来源：agent_messages。同一次模型响应产生的 assistant 文本 /
// tool_call / tool_result 共享 turn_id，按落库顺序归为若干轮：
//   - 除最后一轮外的轮次都已经完整走完，直接拼接到请求历史之后；
//   - 最后一轮没有工具调用，说明模型已给出最终回答（进程在收尾前退出），
//     直接复用该回答，避免重复生成；
//   - 否则重放该轮：已有结果的工具直接复用，缺少结果的重新执行。
//
// 这与「先落检查点再执行」等价，但不再有第二份存储：既省掉每完成一个工具就整段
// 重写历史写放大的开销，也因为与时间线共用一份事实而不会出现两份数据不一致。

// restore 从已落库的消息重建执行现场。
//
// 返回 ok=false 表示没有可恢复的事实（Provider 不支持消息回放，或本任务尚未产生
// assistant / 工具消息），调用方应使用请求自带的历史重新开始。
// final 非空表示模型已经产出最终回答，调用方应直接结束而不必再调用模型。
func (c *Config) restore(ctx context.Context, executor *tool.Executor, rt agentinterfaces.Runtime, req agenttypes.Request) (history []provider.Message, final string, ok bool) {
	replayer, ok := rt.(agentinterfaces.MessageReplay)
	if !ok {
		return nil, "", false
	}
	msgs, err := replayer.TaskMessages(ctx)
	if err != nil {
		logger.Warnf(ctx, "native agent: load task messages failed: %v", err)
		return nil, "", false
	}
	rounds := groupRounds(msgs)
	if len(rounds) == 0 {
		return nil, "", false
	}

	history = requestMessages(req)
	last := rounds[len(rounds)-1]
	if len(last.Calls) == 0 {
		for _, r := range rounds {
			history = append(history, completedMessages(r)...)
		}
		return history, strings.TrimSpace(last.Content), true
	}

	for _, r := range rounds[:len(rounds)-1] {
		history = append(history, completedMessages(r)...)
	}
	history = append(history, c.replayPending(ctx, executor, rt, last)...)
	return history, "", true
}

// groupRounds 把本任务的消息按 turn_id 归为若干轮（保持落库顺序）。
//
// user 消息不参与分组：它已经由 req.Messages 承载。error 不是模型对话的一部分，
// 直接忽略；reasoning 作为该轮的思考内容挂在轮上（思考模式要求随 assistant 回传）。
func groupRounds(msgs []agenttypes.Message) []agenttypes.ToolRound {
	rounds := make([]agenttypes.ToolRound, 0, 4)
	index := make(map[string]int, 4)

	// round 返回指定轮次的可写引用；首次出现时按顺序追加。
	round := func(turnID string) *agenttypes.ToolRound {
		if i, ok := index[turnID]; ok {
			return &rounds[i]
		}
		rounds = append(rounds, agenttypes.ToolRound{TurnID: turnID})
		index[turnID] = len(rounds) - 1
		return &rounds[len(rounds)-1]
	}

	for _, m := range msgs {
		switch m.Kind {
		case agenttypes.KindAssistant:
			r := round(m.TurnID)
			if r.Content != "" {
				r.Content += "\n"
			}
			r.Content += m.Content
		case agenttypes.KindReasoning:
			// 思考内容必须与 assistant 消息一起回传，否则思考模式下的续跑请求会被拒绝。
			if content, ok := agenttypes.ReasoningBlockContent(m.Data); ok && strings.TrimSpace(content) != "" {
				r := round(m.TurnID)
				r.Reasoning = content
			}
		case agenttypes.KindToolCall:
			if call, ok := agenttypes.ToolCallFromData(m.Data); ok {
				r := round(m.TurnID)
				r.Calls = append(r.Calls, call)
			}
		case agenttypes.KindToolResult:
			if res, ok := agenttypes.ToolResultFromData(m.Data); ok {
				r := round(m.TurnID)
				r.Results = append(r.Results, res)
			}
		}
	}
	return rounds
}

// completedMessages 把一轮已完成的工具往返转换为 Provider 消息。
func completedMessages(r agenttypes.ToolRound) []provider.Message {
	if len(r.Calls) == 0 {
		if r.Content == "" {
			return nil
		}
		return []provider.Message{{Role: provider.RoleAssistant, Content: r.Content}}
	}

	calls := make([]provider.ToolCall, 0, len(r.Calls))
	results := make([]provider.ToolResult, 0, len(r.Calls))
	for _, call := range r.Calls {
		calls = append(calls, toProviderToolCall(call))
		res, ok := r.Result(call.ID)
		if !ok {
			// 结果缺失（落库失败 / 数据被清理）：补一条错误结果，保持
			// assistant(tool_calls) 与 tool 消息一一对应，避免 Provider 拒绝请求。
			res = agenttypes.ToolResultBlock{
				ToolCallID: call.ID,
				Content:    "tool result was not persisted",
				IsError:    true,
			}
		}
		results = append(results, toProviderToolResult(res))
	}
	return []provider.Message{
		{Role: provider.RoleAssistant, Content: r.Content, Reasoning: r.Reasoning, ToolCalls: calls},
		{Role: provider.RoleTool, ToolResults: results},
	}
}

// replayPending 重放中断轮次：已有结果的工具复用，缺少结果的重新执行。
func (c *Config) replayPending(ctx context.Context, executor *tool.Executor, rt agentinterfaces.Runtime, r agenttypes.ToolRound) []provider.Message {
	logger.Infof(ctx, "native agent: resuming turn %s (calls=%d, done=%d)",
		r.TurnID, len(r.Calls), len(r.Results))

	calls := make([]provider.ToolCall, 0, len(r.Calls))
	results := make([]provider.ToolResult, 0, len(r.Calls))
	for _, call := range r.Calls {
		pc := toProviderToolCall(call)
		calls = append(calls, pc)
		if res, ok := r.Result(call.ID); ok {
			results = append(results, toProviderToolResult(res))
			continue
		}
		res := c.executeTool(ctx, executor, pc)
		c.emitToolResult(ctx, rt, pc, r.TurnID, res)
		results = append(results, res)
	}
	return []provider.Message{
		{Role: provider.RoleAssistant, Content: r.Content, Reasoning: r.Reasoning, ToolCalls: calls},
		{Role: provider.RoleTool, ToolResults: results},
	}
}

func toProviderToolCall(call agenttypes.ToolCall) provider.ToolCall {
	return provider.ToolCall{ID: call.ID, Name: call.Name, Input: agenttypes.ToolCallArguments(call.Arguments)}
}

func toProviderToolResult(res agenttypes.ToolResultBlock) provider.ToolResult {
	return provider.ToolResult{ToolCallID: res.ToolCallID, Content: res.Content, IsError: res.IsError}
}
