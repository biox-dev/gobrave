package service_test

import (
	"context"
	"testing"

	agentrepository "github.com/biox-dev/gobrave/internal/agent/application/repository"
	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// TestConversationHistoryRebuiltFromMessages 验证会话轮次的历史在执行时从
// agent_messages 重建，而不是来自 Request 快照：
//   - 历史工具往返被还原成 assistant(tool_calls) + tool(results)；
//   - 本轮（当前任务）已产生的 Agent 侧消息被排除（由 Provider 回放补齐）；
//   - 任务行不持久化消息，因此不会随对话增长膨胀。
func TestConversationHistoryRebuiltFromMessages(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newServiceOnDB(t, db)
	sessions := agentservice.NewSessionService(agentrepository.NewGormSessionStore(db))
	messages := agentservice.NewMessageService(agentrepository.NewGormMessageStore(db))

	sess, err := sessions.Create(ctx, agenttypes.SessionCreateParams{UserID: 1, ProjectID: 1, Title: "t"})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	const (
		prevTaskID    = int64(1001)
		currentTaskID = int64(1002)
	)
	seed := func(p agenttypes.MessageCreateParams) {
		t.Helper()
		p.SessionID = sess.ID
		if _, err := messages.Create(ctx, p); err != nil {
			t.Fatalf("seed message: %v", err)
		}
	}

	// 上一轮：user → 思考 + 工具调用 + 结果 → 最终回答。
	seed(agenttypes.MessageCreateParams{Role: agenttypes.RoleUser, Kind: agenttypes.KindUser, TaskID: prevTaskID, UserID: 1, Content: "第一问"})
	seed(agenttypes.MessageCreateParams{
		Role: agenttypes.RoleAssistant, Kind: agenttypes.KindReasoning, TaskID: prevTaskID, TurnID: "t1",
		Data: agenttypes.ReasoningBlock{ID: "r1", TurnID: "t1", Content: "先读文件"},
	})
	seed(agenttypes.MessageCreateParams{
		Role: agenttypes.RoleAssistant, Kind: agenttypes.KindToolCall, TaskID: prevTaskID, TurnID: "t1", ToolCallID: "c1",
		Data: agenttypes.ToolCall{ID: "c1", TurnID: "t1", Name: "read", Arguments: map[string]any{"path": "/tmp/a"}},
	})
	seed(agenttypes.MessageCreateParams{
		Role: agenttypes.RoleAssistant, Kind: agenttypes.KindToolResult, TaskID: prevTaskID, TurnID: "t1", ToolCallID: "c1",
		Data: agenttypes.ToolResultBlock{ToolCallID: "c1", TurnID: "t1", Content: "A"},
	})
	seed(agenttypes.MessageCreateParams{
		Role: agenttypes.RoleAssistant, Kind: agenttypes.KindAssistant, TaskID: prevTaskID, TurnID: "t2", Content: "第一答",
		Data: agenttypes.MessageBlock{ID: "m1", TurnID: "t2", Content: "第一答"},
	})

	// 本轮：user 已写入，Agent 侧消息（同一个 ToolCall 的发起）不应再进历史。
	seed(agenttypes.MessageCreateParams{Role: agenttypes.RoleUser, Kind: agenttypes.KindUser, TaskID: currentTaskID, UserID: 1, Content: "第二问"})
	seed(agenttypes.MessageCreateParams{
		Role: agenttypes.RoleAssistant, Kind: agenttypes.KindToolCall, TaskID: currentTaskID, TurnID: "t3", ToolCallID: "c2",
		Data: agenttypes.ToolCall{ID: "c2", TurnID: "t3", Name: "read", Arguments: map[string]any{"path": "/tmp/b"}},
	})

	task := &agenttypes.Task{ID: currentTaskID, SessionID: sess.ID}
	out := svc.ConversationHistoryForTest(ctx, task)

	wantRoles := []string{agenttypes.RoleUser, agenttypes.RoleAssistant, agenttypes.RoleTool, agenttypes.RoleAssistant, agenttypes.RoleUser}
	if len(out) != len(wantRoles) {
		t.Fatalf("len = %d, want %d: %+v", len(out), len(wantRoles), out)
	}
	for i, want := range wantRoles {
		if out[i].Role != want {
			t.Fatalf("out[%d].role = %s, want %s (out=%+v)", i, out[i].Role, want, out)
		}
	}
	if out[0].Content != "第一问" || out[4].Content != "第二问" {
		t.Fatalf("user messages = %q / %q", out[0].Content, out[4].Content)
	}
	// 历史工具往返是结构化的，且思考内容挂在带 tool_calls 的 assistant 上。
	if calls := out[1].ToolCalls; len(calls) != 1 || calls[0].ID != "c1" {
		t.Fatalf("assistant tool calls = %+v, want c1", calls)
	}
	if out[1].Reasoning != "先读文件" {
		t.Fatalf("assistant reasoning = %q, want 先读文件", out[1].Reasoning)
	}
	if results := out[2].ToolResults; len(results) != 1 || results[0].Content != "A" {
		t.Fatalf("tool results = %+v, want A", results)
	}
	// 最终回答是纯文本，不携带思考内容。
	if out[3].Content != "第一答" || out[3].Reasoning != "" || len(out[3].ToolCalls) != 0 {
		t.Fatalf("final answer = %+v, want plain text", out[3])
	}
}

// TestNewTaskDropsMessagesForSession 验证会话任务不再把历史快照进任务请求。
func TestNewTaskDropsMessagesForSession(t *testing.T) {
	req := agenttypes.Request{Messages: []agenttypes.Message{{Role: agenttypes.RoleUser, Content: "hi"}}}

	sessionTask := agenttypes.NewTask(req, agenttypes.TaskFacts{SessionID: 1})
	if len(sessionTask.Request.Messages) != 0 {
		t.Fatalf("session task messages = %+v, want dropped", sessionTask.Request.Messages)
	}

	// 无会话的一次性任务没有别处可存输入，必须保留。
	oneShot := agenttypes.NewTask(req, agenttypes.TaskFacts{})
	if len(oneShot.Request.Messages) != 1 {
		t.Fatalf("one-shot task messages = %+v, want preserved", oneShot.Request.Messages)
	}
}
