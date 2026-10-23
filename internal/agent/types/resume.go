package types

import (
	"context"
	"time"
)

// 本文件定义「任务可恢复执行」的持久化检查点（Checkpoint）。
//
// 设计原则：
//   - 数据库才是状态源：Agent 循环在每一个安全点把「已完成的对话历史 + 待执行的工具调用」
//     写入 agent_tasks.resume_state，进程重启后据此续跑，而不是依赖内存 goroutine / channel；
//   - 检查点只描述执行事实（消息 / 工具调用 / 工具结果），不包含任何进程级服务对象，
//     因此可以安全地 JSON 序列化进数据库；
//   - Provider 通过实现 CheckpointRuntime 主动写入检查点。当前只有原生 Agent 循环
//     （native.Config.Run）实现；外部 CLI Provider（copilot / claude_code / codex）
//     若未写入检查点，则重启后不可续跑（ResumeTask 会返回 ErrTaskNotResumable）。
//
// 恢复语义（at-least-once）：
//   - 工具结果每完成一个就更新检查点，因此正常重启不会重复执行已完成的工具；
//   - 若进程在「工具已执行、结果尚未落库」的窗口内崩溃，恢复时该工具会被再次执行。
//     对写文件 / 执行命令等非幂等操作，调用方需自行保证幂等（这是持久化循环的通用取舍）。

// ResumeStateVersion 是检查点结构版本。结构不兼容变更时递增，旧检查点会被忽略。
const ResumeStateVersion = 1

// ResumeToolCall 是检查点里的工具调用（与 provider.ToolCall 一一对应）。
type ResumeToolCall struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input,omitempty"` // JSON 参数
}

// ResumeToolResult 是检查点里的工具执行结果（与 provider.ToolResult 一一对应）。
type ResumeToolResult struct {
	ToolCallID string `json:"tool_call_id"`
	Content    string `json:"content,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
}

// ResumeMessage 是检查点里的一条对话消息。
//
// Role 取值与 LLM Provider 对齐：user / assistant / tool。
type ResumeMessage struct {
	Role        string             `json:"role"`
	Content     string             `json:"content,omitempty"`
	ToolCalls   []ResumeToolCall   `json:"tool_calls,omitempty"`
	ToolResults []ResumeToolResult `json:"tool_results,omitempty"`
}

// ResumeTurn 是一次「模型已响应、工具尚未全部执行完」的中间态。
//
// Assistant 保存模型这一轮的完整响应（文本 + 工具调用）；
// Completed 保存已经执行完成的工具结果。二者结合即可精确算出「还有哪些调用需要执行」。
type ResumeTurn struct {
	Assistant ResumeMessage      `json:"assistant"`
	Completed []ResumeToolResult `json:"completed,omitempty"`
}

// TaskResumeState 是任务的可恢复执行检查点，持久化在 agent_tasks.resume_state。
type TaskResumeState struct {
	Version  int             `json:"version"`
	Provider string          `json:"provider,omitempty"`
	Model    string          `json:"model,omitempty"`
	History  []ResumeMessage `json:"history,omitempty"`
	// Pending 非空表示上一轮模型响应已落库、但工具调用尚未全部完成。
	Pending *ResumeTurn `json:"pending,omitempty"`
	// UpdatedAt 便于排查恢复时的检查点新旧。
	UpdatedAt time.Time `json:"updated_at"`
}

// CompletedResult 查询某个工具调用是否已有执行结果。
func (s *TaskResumeState) CompletedResult(callID string) (ResumeToolResult, bool) {
	if s == nil || s.Pending == nil || callID == "" {
		return ResumeToolResult{}, false
	}
	for _, r := range s.Pending.Completed {
		if r.ToolCallID == callID {
			return r, true
		}
	}
	return ResumeToolResult{}, false
}

// MarkToolResult 记录一次工具执行结果（按 tool_call_id 去重）。
func (s *TaskResumeState) MarkToolResult(result ResumeToolResult) {
	if s == nil || s.Pending == nil || result.ToolCallID == "" {
		return
	}
	for i := range s.Pending.Completed {
		if s.Pending.Completed[i].ToolCallID == result.ToolCallID {
			s.Pending.Completed[i] = result
			s.UpdatedAt = time.Now()
			return
		}
	}
	s.Pending.Completed = append(s.Pending.Completed, result)
	s.UpdatedAt = time.Now()
}

// CompleteTurn 把当前 Pending 轮次归档进 History，表示这一轮工具往返已经全部完成。
func (s *TaskResumeState) CompleteTurn() {
	if s == nil || s.Pending == nil {
		return
	}
	assistant := s.Pending.Assistant
	assistant.Role = "assistant"
	s.History = append(s.History, assistant)

	results := make([]ResumeToolResult, 0, len(assistant.ToolCalls))
	for _, call := range assistant.ToolCalls {
		if result, ok := s.CompletedResult(call.ID); ok {
			results = append(results, result)
		}
	}
	if len(results) > 0 {
		s.History = append(s.History, ResumeMessage{Role: "tool", ToolResults: results})
	}
	s.Pending = nil
	s.UpdatedAt = time.Now()
}

// 工具调用 ID 的上下文键。
//
// 它把「当前执行的是哪个 tool call」传给权限 / 提问子系统：
//   - 权限请求写入 Operation.Metadata，重启后据此把已批准的权限与待执行工具精确配对；
//   - 提问请求写入 QuestionRequest.ToolCallID，重启后可复用已作答的结果。
type toolCallIDContextKey struct{}

// WithToolCallID 把工具调用 ID 注入 ctx；id 为空时原样返回。
func WithToolCallID(ctx context.Context, id string) context.Context {
	if id == "" || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, toolCallIDContextKey{}, id)
}

// ToolCallIDFromContext 读取当前工具调用 ID；不存在时返回空串。
func ToolCallIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(toolCallIDContextKey{}).(string)
	return id
}

// OperationMetadataToolCallID 是 Operation.Metadata 中记录工具调用 ID 的键。
const OperationMetadataToolCallID = "tool_call_id"
