package service

import (
	"testing"

	. "github.com/biox-dev/gobrave/internal/agent/types"
)

// agentMsg 构造一条 Agent 侧时间线消息（assistant 文本 / 思考 / 工具调用 / 结果）。
func agentMsg(kind, turnID, callID string, data any) Message {
	return Message{Role: RoleAssistant, Kind: kind, TurnID: turnID, ToolCallID: callID, Data: data}
}

// userMsg 构造一条用户消息。
func userMsg(content string) Message {
	return Message{Role: RoleUser, Kind: KindUser, Content: content}
}

// TestPromptMessagesRebuildsToolRounds 验证历史工具往返被还原成
// assistant(tool_calls) + tool(results)，而不是两条 assistant 纯文本 JSON 摘要。
func TestPromptMessagesRebuildsToolRounds(t *testing.T) {
	history := []Message{
		userMsg("分析一下"),
		agentMsg(KindReasoning, "t1", "", ReasoningBlock{ID: "r1", TurnID: "t1", Content: "先读文件"}),
		agentMsg(KindToolCall, "t1", "c1", ToolCall{ID: "c1", TurnID: "t1", Name: "read", Arguments: map[string]any{"path": "/tmp/a"}}),
		agentMsg(KindToolCall, "t1", "c2", ToolCall{ID: "c2", TurnID: "t1", Name: "read", Arguments: map[string]any{"path": "/tmp/b"}}),
		agentMsg(KindToolResult, "t1", "c1", ToolResultBlock{ToolCallID: "c1", TurnID: "t1", Content: "A"}),
		agentMsg(KindToolResult, "t1", "c2", ToolResultBlock{ToolCallID: "c2", TurnID: "t1", Content: "B"}),
		{Role: RoleAssistant, Kind: KindAssistant, TurnID: "t2", Content: "结论",
			Data: MessageBlock{ID: "m1", TurnID: "t2", Content: "结论"}},
	}

	out := promptMessages(history)
	if len(out) != 4 {
		t.Fatalf("len = %d, want user + assistant(calls) + tool + assistant; got %+v", len(out), out)
	}

	if out[0].Role != RoleUser || out[0].Content != "分析一下" {
		t.Fatalf("out[0] = %+v, want user", out[0])
	}

	assistant := out[1]
	if assistant.Role != RoleAssistant || len(assistant.ToolCalls) != 2 {
		t.Fatalf("out[1] = %+v, want assistant with 2 tool calls", assistant)
	}
	if assistant.ToolCalls[0].ID != "c1" || assistant.ToolCalls[1].ID != "c2" {
		t.Fatalf("tool call order = %+v, want c1, c2", assistant.ToolCalls)
	}
	if assistant.Reasoning != "先读文件" {
		t.Fatalf("reasoning = %q, want 先读文件", assistant.Reasoning)
	}

	toolMsg := out[2]
	if toolMsg.Role != RoleTool || len(toolMsg.ToolResults) != 2 {
		t.Fatalf("out[2] = %+v, want tool with 2 results", toolMsg)
	}
	if toolMsg.ToolResults[0].Content != "A" || toolMsg.ToolResults[1].Content != "B" {
		t.Fatalf("results = %+v, want A, B", toolMsg.ToolResults)
	}

	// 最终回答是一条纯文本 assistant，且不带思考内容（跨轮 CoT 不参与上下文）。
	final := out[3]
	if final.Role != RoleAssistant || final.Content != "结论" || final.Reasoning != "" {
		t.Fatalf("out[3] = %+v, want plain assistant answer", final)
	}
}

// TestPromptMessagesKeepsTurnBoundaries 验证不同轮次（被 user 消息隔开）不会被合并。
func TestPromptMessagesKeepsTurnBoundaries(t *testing.T) {
	history := []Message{
		userMsg("第一轮"),
		agentMsg(KindToolCall, "t1", "c1", ToolCall{ID: "c1", TurnID: "t1", Name: "echo"}),
		agentMsg(KindToolResult, "t1", "c1", ToolResultBlock{ToolCallID: "c1", TurnID: "t1", Content: "1"}),
		userMsg("第二轮"),
		agentMsg(KindToolCall, "t2", "c2", ToolCall{ID: "c2", TurnID: "t2", Name: "echo"}),
		agentMsg(KindToolResult, "t2", "c2", ToolResultBlock{ToolCallID: "c2", TurnID: "t2", Content: "2"}),
	}

	out := promptMessages(history)
	if len(out) != 6 {
		t.Fatalf("len = %d, want 6 (2 x user + assistant + tool)", len(out))
	}
	for i, want := range []string{RoleUser, RoleAssistant, RoleTool, RoleUser, RoleAssistant, RoleTool} {
		if out[i].Role != want {
			t.Fatalf("out[%d].role = %s, want %s", i, out[i].Role, want)
		}
	}
	if out[1].ToolCalls[0].ID != "c1" || out[4].ToolCalls[0].ID != "c2" {
		t.Fatalf("rounds not separated: %+v vs %+v", out[1], out[4])
	}
}

// TestPromptMessagesFillsMissingResults 验证结果缺失时补一条错误结果，
// 保证 assistant(tool_calls) 与 tool 消息仍然一一对应（否则 Provider 会拒绝请求）。
func TestPromptMessagesFillsMissingResults(t *testing.T) {
	history := []Message{
		userMsg("hi"),
		agentMsg(KindToolCall, "t1", "c1", ToolCall{ID: "c1", TurnID: "t1", Name: "echo"}),
	}

	out := promptMessages(history)
	if len(out) != 3 {
		t.Fatalf("len = %d, want user + assistant + tool", len(out))
	}
	results := out[2].ToolResults
	if len(results) != 1 || results[0].ToolCallID != "c1" || !results[0].IsError {
		t.Fatalf("results = %+v, want one error placeholder for c1", results)
	}
}

// TestPromptMessagesSkipsEmptyAndErrorMessages 验证空正文的 assistant 与 error 消息
// 不进入 prompt，避免产生空气泡。
func TestPromptMessagesSkipsEmptyAndErrorMessages(t *testing.T) {
	history := []Message{
		userMsg("hi"),
		agentMsg(KindError, "", "", nil),
		{Role: RoleAssistant, Kind: KindAssistant, Content: "   "},
	}

	out := promptMessages(history)
	if len(out) != 1 || out[0].Role != RoleUser {
		t.Fatalf("out = %+v, want only the user message", out)
	}
}
