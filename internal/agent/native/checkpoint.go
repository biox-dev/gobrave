package native

import (
	"context"
	"time"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/agent/llm/provider"
	"github.com/biox-dev/gobrave/internal/agent/logging"
)

// 本文件负责原生 Agent 循环与「持久化检查点」之间的桥接：
//
//	native 循环（内存历史 / 工具往返） ↔ agent.TaskResumeState（可 JSON 落库）
//
// 只有 Runtime 实现了 agent.CheckpointRuntime（任务模式）时才启用检查点；
// 独立调用（NewStandaloneRuntime）下 writer 为空，行为与之前完全一致。

// checkpointWriter 把检查点写入 Runtime 背后的任务行。
type checkpointWriter struct {
	rt agent.CheckpointRuntime
}

// newCheckpointWriter 在 Runtime 支持检查点时返回 writer，否则返回 nil。
func newCheckpointWriter(rt agent.Runtime) *checkpointWriter {
	if rt == nil {
		return nil
	}
	cr, ok := rt.(agent.CheckpointRuntime)
	if !ok {
		return nil
	}
	return &checkpointWriter{rt: cr}
}

func (w *checkpointWriter) enabled() bool { return w != nil && w.rt != nil }

func (w *checkpointWriter) load(ctx context.Context) *agent.TaskResumeState {
	if !w.enabled() {
		return nil
	}
	return w.rt.LoadCheckpoint(ctx)
}

func (w *checkpointWriter) clear(ctx context.Context) {
	if !w.enabled() {
		return
	}
	if err := w.rt.ClearCheckpoint(ctx); err != nil {
		logging.Warn("native agent: clear checkpoint failed", "err", err)
	}
}

func (w *checkpointWriter) save(ctx context.Context, state *agent.TaskResumeState) {
	if !w.enabled() || state == nil {
		return
	}
	state.UpdatedAt = time.Now()
	if err := w.rt.SaveCheckpoint(ctx, state); err != nil {
		// 检查点写入是尽力而为：失败只记日志，不打断 Agent 主流程。
		logging.Warn("native agent: save checkpoint failed", "err", err)
	}
}

// newResumeState 构造「模型已响应、工具待执行」这一安全点的检查点。
//
// history 必须是不含本轮 assistant 消息的历史（本轮响应放在 Pending.Assistant 中），
// 恢复时先把 Pending 补全，避免同一轮在历史里出现两次。
func newResumeState(req agent.Request, history []provider.Message, response *provider.Response) *agent.TaskResumeState {
	return &agent.TaskResumeState{
		Version:  agent.ResumeStateVersion,
		Provider: req.Provider,
		Model:    req.ModelID,
		History:  toResumeHistory(history),
		Pending: &agent.ResumeTurn{
			Assistant: responseToResumeMessage(response),
		},
		UpdatedAt: time.Now(),
	}
}

// toResumeHistory 把内存对话历史转换为可序列化结构。
func toResumeHistory(history []provider.Message) []agent.ResumeMessage {
	out := make([]agent.ResumeMessage, 0, len(history))
	for _, m := range history {
		out = append(out, toResumeMessage(m))
	}
	return out
}

func toResumeMessage(m provider.Message) agent.ResumeMessage {
	msg := agent.ResumeMessage{
		Role:    string(m.Role),
		Content: m.Content,
	}
	if len(m.ToolCalls) > 0 {
		msg.ToolCalls = make([]agent.ResumeToolCall, 0, len(m.ToolCalls))
		for _, call := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, toResumeToolCall(call))
		}
	}
	if len(m.ToolResults) > 0 {
		msg.ToolResults = make([]agent.ResumeToolResult, 0, len(m.ToolResults))
		for _, res := range m.ToolResults {
			msg.ToolResults = append(msg.ToolResults, toResumeToolResult(res))
		}
	}
	return msg
}

func toResumeToolCall(call provider.ToolCall) agent.ResumeToolCall {
	return agent.ResumeToolCall{ID: call.ID, Name: call.Name, Input: call.Input}
}

func toResumeToolResult(res provider.ToolResult) agent.ResumeToolResult {
	return agent.ResumeToolResult{
		ToolCallID: res.ToolCallID,
		Content:    res.Content,
		IsError:    res.IsError,
	}
}

// fromResumeHistory 把持久化历史还原为 Provider 对话消息。
func fromResumeHistory(history []agent.ResumeMessage) []provider.Message {
	out := make([]provider.Message, 0, len(history))
	for _, m := range history {
		out = append(out, fromResumeMessage(m))
	}
	return out
}

func fromResumeMessage(m agent.ResumeMessage) provider.Message {
	msg := provider.Message{
		Role:    provider.Role(m.Role),
		Content: m.Content,
	}
	if len(m.ToolCalls) > 0 {
		msg.ToolCalls = make([]provider.ToolCall, 0, len(m.ToolCalls))
		for _, call := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, provider.ToolCall{ID: call.ID, Name: call.Name, Input: call.Input})
		}
	}
	if len(m.ToolResults) > 0 {
		msg.ToolResults = make([]provider.ToolResult, 0, len(m.ToolResults))
		for _, res := range m.ToolResults {
			msg.ToolResults = append(msg.ToolResults, provider.ToolResult{
				ToolCallID: res.ToolCallID,
				Content:    res.Content,
				IsError:    res.IsError,
			})
		}
	}
	return msg
}

func fromResumeToolResult(res agent.ResumeToolResult) provider.ToolResult {
	return provider.ToolResult{
		ToolCallID: res.ToolCallID,
		Content:    res.Content,
		IsError:    res.IsError,
	}
}

// responseToResumeMessage 把一轮模型响应转换为 assistant 消息（文本 + 工具调用）。
func responseToResumeMessage(resp *provider.Response) agent.ResumeMessage {
	if resp == nil {
		return agent.ResumeMessage{Role: string(provider.RoleAssistant)}
	}
	msg := agent.ResumeMessage{
		Role:    string(provider.RoleAssistant),
		Content: resp.Content,
	}
	if len(resp.ToolCalls) > 0 {
		msg.ToolCalls = make([]agent.ResumeToolCall, 0, len(resp.ToolCalls))
		for _, call := range resp.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, toResumeToolCall(call))
		}
	}
	return msg
}
