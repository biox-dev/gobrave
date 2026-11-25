package service_test

import (
	"context"
	"strings"
	"testing"

	agentrepository "github.com/biox-dev/gobrave/internal/agent/application/repository"
	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// TestConversationHistoryUsesSummary 验证跨轮压缩的拼装语义：
// 水位之前的历史被摘要替代，水位之后的原文照常保留；本轮 Agent 侧消息仍由 Provider 回放。
func TestConversationHistoryUsesSummary(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	svc := newServiceOnDB(t, db)
	sessions := agentservice.NewSessionService(agentrepository.NewGormSessionStore(db))
	messages := agentservice.NewMessageService(agentrepository.NewGormMessageStore(db))
	summaries := agentservice.NewSessionSummaryService(agentrepository.NewGormSessionSummaryStore(db))

	sess, err := sessions.Create(ctx, agenttypes.SessionCreateParams{UserID: 1, ProjectID: 1, Title: "t"})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	const (
		oldTaskID = int64(2001)
		newTaskID = int64(2002)
	)
	seed := func(p agenttypes.MessageCreateParams) {
		t.Helper()
		p.SessionID = sess.ID
		if _, err := messages.Create(ctx, p); err != nil {
			t.Fatalf("seed message: %v", err)
		}
	}

	seed(agenttypes.MessageCreateParams{Role: agenttypes.RoleUser, Kind: agenttypes.KindUser, TaskID: oldTaskID, UserID: 1, Content: "很久以前的问题"})
	seed(agenttypes.MessageCreateParams{Role: agenttypes.RoleAssistant, Kind: agenttypes.KindAssistant, TaskID: oldTaskID, TurnID: "t1", Content: "很久以前的回答"})

	oldMsgs, err := messages.List(ctx, sess.ID)
	if err != nil {
		t.Fatalf("list old messages: %v", err)
	}
	watermark := oldMsgs[len(oldMsgs)-1].Seq
	if err := summaries.Upsert(ctx, &agenttypes.SessionSummary{
		SessionID: sess.ID,
		UpToSeq:   watermark,
		Content:   "用户此前询问了旧问题。",
		Model:     "test-model",
	}); err != nil {
		t.Fatalf("upsert summary: %v", err)
	}

	seed(agenttypes.MessageCreateParams{Role: agenttypes.RoleUser, Kind: agenttypes.KindUser, TaskID: newTaskID, UserID: 1, Content: "现在的问题"})
	seed(agenttypes.MessageCreateParams{Role: agenttypes.RoleAssistant, Kind: agenttypes.KindAssistant, TaskID: newTaskID, TurnID: "t2", Content: "本轮已落库的中间回答"})

	out := svc.ConversationHistoryForTest(ctx, &agenttypes.Task{ID: newTaskID, SessionID: sess.ID})
	if len(out) != 2 {
		t.Fatalf("len = %d, want summary + current user: %+v", len(out), out)
	}
	if !strings.Contains(out[0].Content, "用户此前询问了旧问题。") {
		t.Fatalf("summary message = %+v", out[0])
	}
	if out[1].Content != "现在的问题" {
		t.Fatalf("kept message = %+v", out[1])
	}
	for _, m := range out {
		if strings.Contains(m.Content, "很久以前") || strings.Contains(m.Content, "中间回答") {
			t.Fatalf("unexpected message in prompt: %+v", m)
		}
	}

	// 水位只增不减：较旧的摘要不会覆盖较新的结果。
	if err := summaries.Upsert(ctx, &agenttypes.SessionSummary{
		SessionID: sess.ID,
		UpToSeq:   watermark - 1,
		Content:   "过期的摘要",
	}); err != nil {
		t.Fatalf("upsert stale summary: %v", err)
	}
	got, err := summaries.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get summary: %v", err)
	}
	if got == nil || got.Content != "用户此前询问了旧问题。" || got.UpToSeq != watermark {
		t.Fatalf("summary = %+v, want original", got)
	}
}
