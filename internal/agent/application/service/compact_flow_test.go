package service_test

import (
	"context"
	"strings"
	"testing"
	"time"

	agentrepository "github.com/biox-dev/gobrave/internal/agent/application/repository"
	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	agentclient "github.com/biox-dev/gobrave/internal/agent/client"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

const fixedSummaryProviderName = "fixed-summary"

type fixedSummaryProvider struct{}

func (fixedSummaryProvider) Name() string { return fixedSummaryProviderName }

func (fixedSummaryProvider) New(agentclient.Options) (agentinterfaces.Agent, error) {
	return fixedSummaryAgent{}, nil
}

type fixedSummaryAgent struct{}

func (fixedSummaryAgent) Name() string { return fixedSummaryProviderName }

func (fixedSummaryAgent) Invoke(context.Context, agenttypes.Request, agentinterfaces.Runtime) (*agenttypes.Result, error) {
	return &agenttypes.Result{Content: "压缩后的摘要：用户此前在处理旧任务。"}, nil
}

func (fixedSummaryAgent) Stream(ctx context.Context, req agenttypes.Request, rt agentinterfaces.Runtime) (*agenttypes.Result, error) {
	return fixedSummaryAgent{}.Invoke(ctx, req, rt)
}

// TestConversationHistoryCompactsWhenOverBudget 验证同步压缩闭环：
// 历史超过预算 → 调用摘要模型 → 写入水位 → 本轮 Prompt 只保留摘要 + 最近轮原文。
func TestConversationHistoryCompactsWhenOverBudget(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	registry := agentclient.NewRegistry(fixedSummaryProvider{})
	client := agentclient.NewClient(registry, agentclient.Options{
		ProviderSource: func() map[string]agentclient.ModelProviderConfig {
			return map[string]agentclient.ModelProviderConfig{
				"test-model": {ModelID: "test-model", ContextWindow: 100_000, DefaultMaxTokens: 1024},
			}
		},
	})
	messages := agentservice.NewMessageService(agentrepository.NewGormMessageStore(db))
	summaries := agentservice.NewSessionSummaryService(agentrepository.NewGormSessionSummaryStore(db))
	svc := agentservice.NewService(agentservice.ServiceConfig{
		Client:    client,
		Tasks:     agentrepository.NewGormTaskRepository(db),
		Messages:  messages,
		Summaries: summaries,
		Perms:     agentservice.NewPermissionManager(agentrepository.NewGormPermissionRepository(db)),
		Questions: agentservice.NewQuestionManager(agentrepository.NewGormQuestionRepository(db)),
		Profiles:  agentservice.NewProfileManager(agentrepository.NewGormProfileRepository(db)),
		Compaction: agentservice.CompactionOptions{
			Enabled:          true,
			TriggerRatio:     0.01,
			PreCompactRatio:  0.005,
			KeepTurns:        1,
			MaxSummaryTokens: 64,
			SummaryTimeout:   5 * time.Second,
		},
	})

	sessions := agentservice.NewSessionService(agentrepository.NewGormSessionStore(db))
	sess, err := sessions.Create(ctx, agenttypes.SessionCreateParams{UserID: 1, ProjectID: 1, Title: "t"})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}

	const (
		oldTaskID = int64(3001)
		curTaskID = int64(3002)
	)
	oldContent := strings.Repeat("旧的历史内容 old history content ", 300)
	seed := func(p agenttypes.MessageCreateParams) {
		t.Helper()
		p.SessionID = sess.ID
		if _, err := messages.Create(ctx, p); err != nil {
			t.Fatalf("seed message: %v", err)
		}
	}
	seed(agenttypes.MessageCreateParams{Role: agenttypes.RoleUser, Kind: agenttypes.KindUser, TaskID: oldTaskID, UserID: 1, Content: oldContent})
	seed(agenttypes.MessageCreateParams{Role: agenttypes.RoleAssistant, Kind: agenttypes.KindAssistant, TaskID: oldTaskID, TurnID: "t1", Content: "旧回答"})
	seed(agenttypes.MessageCreateParams{Role: agenttypes.RoleUser, Kind: agenttypes.KindUser, TaskID: curTaskID, UserID: 1, Content: "当前问题"})

	out := svc.ConversationHistoryWithRequestForTest(ctx, &agenttypes.Task{ID: curTaskID, SessionID: sess.ID}, agenttypes.Request{
		Provider: fixedSummaryProviderName,
		ModelID:  "test-model",
	})

	summary, err := summaries.Get(ctx, sess.ID)
	if err != nil {
		t.Fatalf("get summary: %v", err)
	}
	if summary == nil || strings.TrimSpace(summary.Content) == "" {
		t.Fatal("summary was not created")
	}
	if summary.UpToSeq <= 0 {
		t.Fatalf("summary watermark = %d, want > 0", summary.UpToSeq)
	}

	if len(out) != 2 {
		t.Fatalf("len = %d, want summary + current user: %+v", len(out), out)
	}
	if !strings.Contains(out[0].Content, summary.Content) {
		t.Fatalf("first message is not the summary: %q", out[0].Content)
	}
	if out[1].Content != "当前问题" {
		t.Fatalf("kept message = %q", out[1].Content)
	}
	for _, m := range out {
		if strings.Contains(m.Content, "旧的历史内容") {
			t.Fatal("old history was not replaced by the summary")
		}
	}
}

// TestScheduleCompactionWritesSummaryInBackground 验证轮末异步预压缩：
// 不阻塞调用方，后台推进摘要水位后下一轮直接命中。
func TestScheduleCompactionWritesSummaryInBackground(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)

	registry := agentclient.NewRegistry(fixedSummaryProvider{})
	client := agentclient.NewClient(registry, agentclient.Options{
		ProviderSource: func() map[string]agentclient.ModelProviderConfig {
			return map[string]agentclient.ModelProviderConfig{
				"test-model": {ModelID: "test-model", ContextWindow: 100_000, DefaultMaxTokens: 1024},
			}
		},
	})
	messages := agentservice.NewMessageService(agentrepository.NewGormMessageStore(db))
	summaries := agentservice.NewSessionSummaryService(agentrepository.NewGormSessionSummaryStore(db))
	svc := agentservice.NewService(agentservice.ServiceConfig{
		Client:    client,
		Tasks:     agentrepository.NewGormTaskRepository(db),
		Messages:  messages,
		Summaries: summaries,
		Perms:     agentservice.NewPermissionManager(agentrepository.NewGormPermissionRepository(db)),
		Questions: agentservice.NewQuestionManager(agentrepository.NewGormQuestionRepository(db)),
		Profiles:  agentservice.NewProfileManager(agentrepository.NewGormProfileRepository(db)),
		Compaction: agentservice.CompactionOptions{
			Enabled:          true,
			TriggerRatio:     0.9,
			PreCompactRatio:  0.005,
			KeepTurns:        1,
			MaxSummaryTokens: 64,
			SummaryTimeout:   5 * time.Second,
		},
	})

	sessions := agentservice.NewSessionService(agentrepository.NewGormSessionStore(db))
	sess, err := sessions.Create(ctx, agenttypes.SessionCreateParams{UserID: 1, ProjectID: 1, Title: "t"})
	if err != nil {
		t.Fatalf("Create session: %v", err)
	}
	seed := func(p agenttypes.MessageCreateParams) {
		t.Helper()
		p.SessionID = sess.ID
		if _, err := messages.Create(ctx, p); err != nil {
			t.Fatalf("seed message: %v", err)
		}
	}
	seed(agenttypes.MessageCreateParams{Role: agenttypes.RoleUser, Kind: agenttypes.KindUser, TaskID: 4001, UserID: 1,
		Content: strings.Repeat("旧的历史内容 ", 400)})
	seed(agenttypes.MessageCreateParams{Role: agenttypes.RoleUser, Kind: agenttypes.KindUser, TaskID: 4002, UserID: 1, Content: "当前问题"})

	task := &agenttypes.Task{ID: 4002, SessionID: sess.ID, Status: agenttypes.TaskCompleted}
	svc.ScheduleCompactionForTest(ctx, task, agenttypes.Request{Provider: fixedSummaryProviderName, ModelID: "test-model"})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		summary, err := summaries.Get(ctx, sess.ID)
		if err != nil {
			t.Fatalf("get summary: %v", err)
		}
		if summary != nil && summary.Content != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("background compaction did not write a summary in time")
}
