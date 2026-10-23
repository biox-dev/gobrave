package service_test

import (
	"context"
	"errors"
	"testing"
	"time"

	// 使用点导入以复用 agent 包中大量领域标识符（与合并前的包内测试保持一致）。
	agentrepository "github.com/biox-dev/gobrave/internal/agent/application/repository"
	. "github.com/biox-dev/gobrave/internal/agent/application/service"
	. "github.com/biox-dev/gobrave/internal/agent/runtime"
	. "github.com/biox-dev/gobrave/internal/agent/types"
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

// TestQuestionManagerPartialAnswer 覆盖：多问题逐题作答时，未答完全部必答题保持
// pending 并保留暂存进度，答完后才唤醒等待方且答案完整。
func TestQuestionManagerPartialAnswer(t *testing.T) {
	ctx := context.Background()
	mgr := newTestQuestionManager(t)

	q, err := mgr.Create(ctx, 9, "", AskUserInput{
		Title: "两个问题",
		Questions: []Question{
			{ID: "analysis_type", Type: QuestionTypeSingleChoice, Title: "类型", Required: true,
				Options: []QuestionOption{{Label: "a", Value: "a"}, {Label: "b", Value: "b"}}},
			{ID: "group_column", Type: QuestionTypeText, Title: "分组列", Required: true},
			{ID: "note", Type: QuestionTypeText, Title: "备注"},
		},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// 先作答第一题：仍为 pending，进度已持久化。
	got, err := mgr.Answer(ctx, q.ID, AskUserAnswer{
		Answers:     []QuestionAnswer{{ID: "analysis_type", Value: "a"}},
		AnsweredIDs: []string{"analysis_type"},
	}, 1)
	if err != nil {
		t.Fatalf("Answer#1: %v", err)
	}
	if got.Status != QuestionPending {
		t.Fatalf("status = %s, want pending", got.Status)
	}
	if got.Answer == nil || len(got.Answer.Answers) != 1 {
		t.Fatalf("progress = %+v, want 1 answer", got.Answer)
	}
	if got.ResolvedAt != nil {
		t.Fatalf("ResolvedAt should be nil while pending")
	}

	// 等待方在答完全部必答题前不应被唤醒。
	waited := make(chan AskUserAnswer, 1)
	go func() {
		ans, err := mgr.Wait(ctx, q.ID)
		if err != nil {
			waited <- AskUserAnswer{}
			return
		}
		waited <- ans
	}()
	select {
	case <-waited:
		t.Fatalf("Wait returned before all required questions were answered")
	case <-time.After(150 * time.Millisecond):
	}

	// 作答第二题（必答）：迁移到 answered 并唤醒等待方，答案包含两题。
	got, err = mgr.Answer(ctx, q.ID, AskUserAnswer{
		Answers:     []QuestionAnswer{{ID: "group_column", Value: "group"}},
		AnsweredIDs: []string{"group_column"},
	}, 2)
	if err != nil {
		t.Fatalf("Answer#2: %v", err)
	}
	if got.Status != QuestionAnswered {
		t.Fatalf("status = %s, want answered", got.Status)
	}
	if got.Answer == nil || len(got.Answer.Answers) != 2 {
		t.Fatalf("merged answers = %+v, want 2", got.Answer)
	}

	select {
	case ans := <-waited:
		if len(ans.Answers) != 2 {
			t.Fatalf("waited answers = %+v, want 2", ans)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("Wait not woken after all required answered")
	}

	// 已完结的提问不能再作答。
	if _, err := mgr.Answer(ctx, q.ID, AskUserAnswer{Answers: []QuestionAnswer{{ID: "analysis_type", Value: "b"}}}, 3); !errors.Is(err, ErrQuestionNotPending) {
		t.Fatalf("err = %v, want ErrQuestionNotPending", err)
	}
}

// TestQuestionManagerUnknownQuestionID 覆盖：提交不存在的问题 ID 被拒绝且状态不变。
func TestQuestionManagerUnknownQuestionID(t *testing.T) {
	ctx := context.Background()
	mgr := newTestQuestionManager(t)

	q, err := mgr.Create(ctx, 10, "", demoAskInput())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	_, err = mgr.Answer(ctx, q.ID, AskUserAnswer{
		Answers: []QuestionAnswer{{ID: "not_exists", Value: "x"}},
	}, 1)
	if !errors.Is(err, ErrUnknownQuestionID) {
		t.Fatalf("err = %v, want ErrUnknownQuestionID", err)
	}
	got, err := mgr.Get(ctx, q.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != QuestionPending {
		t.Fatalf("status = %s, want pending", got.Status)
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

// TestAskUserInputNormalize 覆盖：模型漏填 / 重复 option.value 时补齐并去重。
//
// 前端 Radio / Checkbox 以 value 作为选中标识，value 缺失或重复会让所有选项共享
// 同一个值，表现为「点一个选项，全部选项都被选中」。
func TestAskUserInputNormalize(t *testing.T) {
	q := NewQuestionRequest(1, "", AskUserInput{
		Title: "Roary 泛基因组分析参数确认",
		Questions: []Question{
			{
				ID:    "samples",
				Type:  QuestionTypeSingleChoice,
				Title: "使用哪些 GFF 样本？",
				Options: []QuestionOption{
					{Label: "全部 17 个 GFF"},
					{Label: "16 个，排除 OCF-1.gff"},
					{Label: "16 个，排除 OCF-1.gff"},
					{Value: "v"},
					{Label: "", Value: ""},
				},
			},
			{
				ID:      "note",
				Type:    QuestionTypeText,
				Title:   "补充说明",
				Options: []QuestionOption{{Label: "x", Value: "x"}},
			},
		},
	})

	opts := q.Questions[0].Options
	want := []string{"全部 17 个 GFF", "16 个，排除 OCF-1.gff", "16 个，排除 OCF-1.gff#2", "v"}
	if len(opts) != len(want) {
		t.Fatalf("options = %+v", opts)
	}
	for i, opt := range opts {
		if opt.Value != want[i] {
			t.Fatalf("options[%d].Value = %q, want %q", i, opt.Value, want[i])
		}
		if opt.Label == "" {
			t.Fatalf("options[%d].Label empty", i)
		}
	}
	if q.Questions[1].Options != nil {
		t.Fatalf("text question options = %+v, want nil", q.Questions[1].Options)
	}
}
