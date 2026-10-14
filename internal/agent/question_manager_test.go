package agent_test

import (
	"context"
	"errors"
	"testing"
	"time"

	// 使用点导入以复用 agent 包中大量领域标识符（与合并前的包内测试保持一致）。
	. "github.com/biox-dev/gobrave/internal/agent"
	agentrepository "github.com/biox-dev/gobrave/internal/agent/repository"
)

// newTestQuestionManager 构建一个使用 GORM 提问仓库（内存 sqlite）的提问管理器。
func newTestQuestionManager(t *testing.T) *QuestionManager {
	t.Helper()
	return NewQuestionManager(agentrepository.NewGormQuestionRepository(newTestDB(t)))
}

func demoAskInput() AskUserInput {
	return AskUserInput{
		RequestID:   "ask_001",
		Title:       "选择分析类型",
		Description: "请确认你希望进行哪种差异分析。",
		Questions: []Question{
			{
				ID:       "analysis_type",
				Type:     QuestionTypeSingleChoice,
				Title:    "你要分析什么？",
				Required: true,
				Options: []QuestionOption{
					{Label: "差异表达分析", Value: "differential_expression"},
					{Label: "差异丰度分析", Value: "differential_abundance"},
				},
				AllowCustom: true,
			},
		},
	}
}

// TestQuestionManagerAnswerFlow 覆盖：创建 pending → 查询 → 阻塞等待 → 作答唤醒 → 持久化 → 重复作答报错。
func TestQuestionManagerAnswerFlow(t *testing.T) {
	ctx := context.Background()
	mgr := newTestQuestionManager(t)

	q, err := mgr.Create(ctx, 123, "sess-1", demoAskInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if q.Status != QuestionPending {
		t.Fatalf("status = %s, want pending", q.Status)
	}

	pending, err := mgr.GetPending(ctx, 123)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending = %d (err=%v), want 1", len(pending), err)
	}

	// 模拟 Agent 侧阻塞等待。
	waited := make(chan AskUserAnswer, 1)
	waitErr := make(chan error, 1)
	go func() {
		ans, err := mgr.Wait(ctx, q.ID)
		if err != nil {
			waitErr <- err
			return
		}
		waited <- ans
	}()

	// UI 作答。
	answer := AskUserAnswer{
		RequestID: "ask_001",
		Answers:   []QuestionAnswer{{ID: "analysis_type", Value: "differential_expression"}},
	}
	resolved, err := mgr.Answer(ctx, q.ID, answer, 1)
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if resolved.Status != QuestionAnswered {
		t.Fatalf("status = %s, want answered", resolved.Status)
	}

	select {
	case ans := <-waited:
		if len(ans.Answers) != 1 || ans.Answers[0].Value != "differential_expression" {
			t.Fatalf("waited answer = %+v", ans)
		}
	case err := <-waitErr:
		t.Fatalf("Wait: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatalf("Wait did not return after answer")
	}

	// 作答后不再 pending，且答案已持久化（可跨刷新恢复）。
	pending, _ = mgr.GetPending(ctx, 123)
	if len(pending) != 0 {
		t.Fatalf("pending after answer = %d, want 0", len(pending))
	}
	got, err := mgr.Get(ctx, q.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Answer == nil || len(got.Answer.Answers) != 1 || got.Answer.Answers[0].Value != "differential_expression" {
		t.Fatalf("persisted answer = %+v", got.Answer)
	}

	// 重复作答应报 ErrQuestionNotPending。
	if _, err := mgr.Answer(ctx, q.ID, answer, 1); !errors.Is(err, ErrQuestionNotPending) {
		t.Fatalf("second Answer err = %v, want ErrQuestionNotPending", err)
	}
}

// TestQuestionManagerCancel 覆盖：取消未作答的提问会唤醒等待方并返回 ErrQuestionCanceled。
func TestQuestionManagerCancel(t *testing.T) {
	ctx := context.Background()
	mgr := newTestQuestionManager(t)

	q, err := mgr.Create(ctx, 7, "", AskUserInput{
		Title:     "t",
		Questions: []Question{{ID: "a", Type: QuestionTypeText, Title: "a"}},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := mgr.Wait(ctx, q.ID)
		errCh <- err
	}()

	if _, err := mgr.Cancel(ctx, q.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, ErrQuestionCanceled) {
			t.Fatalf("Wait err = %v, want ErrQuestionCanceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Wait did not return after cancel")
	}
}

// TestAskUserToolDefinition 校验 ask_user 工具定义的非空关键字段。
func TestAskUserToolDefinition(t *testing.T) {
	def := AskUserToolDefinition()
	if AskUserToolName != "ask_user" {
		t.Fatalf("AskUserToolName = %q, want ask_user", AskUserToolName)
	}
	if def.Name != AskUserToolName {
		t.Fatalf("name = %q", def.Name)
	}
	if def.Description == "" {
		t.Fatalf("empty description")
	}
	if def.InputSchema["type"] != "object" {
		t.Fatalf("schema type = %v", def.InputSchema["type"])
	}
	props, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("missing properties")
	}
	for _, key := range []string{"title", "description", "questions", "request_id"} {
		if _, ok := props[key]; !ok {
			t.Fatalf("missing property %q", key)
		}
	}
}
