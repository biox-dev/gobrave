package service

import (
	"strings"
	"testing"

	. "github.com/biox-dev/gobrave/internal/agent/types"
)

func TestCompactionOptionsWithDefaults(t *testing.T) {
	got := (CompactionOptions{Enabled: true}).withDefaults()
	if got.TriggerRatio != defaultCompactionTriggerRatio {
		t.Fatalf("TriggerRatio = %v", got.TriggerRatio)
	}
	if got.PreCompactRatio != defaultCompactionPreRatio {
		t.Fatalf("PreCompactRatio = %v", got.PreCompactRatio)
	}
	if got.KeepTurns != defaultCompactionKeepTurns {
		t.Fatalf("KeepTurns = %d", got.KeepTurns)
	}
	if got.MaxSummaryTokens != defaultCompactionMaxSummary {
		t.Fatalf("MaxSummaryTokens = %d", got.MaxSummaryTokens)
	}
	if got.SummaryTimeout != defaultCompactionTimeout {
		t.Fatalf("SummaryTimeout = %v", got.SummaryTimeout)
	}

	// 预压缩阈值必须低于同步阈值，否则每轮都会同步压缩。
	bad := (CompactionOptions{Enabled: true, TriggerRatio: 0.5, PreCompactRatio: 0.9}).withDefaults()
	if bad.PreCompactRatio >= bad.TriggerRatio {
		t.Fatalf("PreCompactRatio = %v, want < TriggerRatio %v", bad.PreCompactRatio, bad.TriggerRatio)
	}
}

func TestCompactionCutSeq(t *testing.T) {
	msgs := make([]Message, 0, 16)
	seq := 0
	for taskID := int64(1); taskID <= 5; taskID++ {
		for _, role := range []string{RoleUser, RoleAssistant} {
			msgs = append(msgs, Message{Seq: seq, TaskID: taskID, Role: role})
			seq++
		}
	}

	// 保留最近 2 轮（任务 4、5）：切点是任务 4 的第一条消息。
	if got := compactionCutSeq(msgs, 2); got != 6 {
		t.Fatalf("cut = %d, want 6", got)
	}
	// 轮数不超过保留数：无需压缩。
	if got := compactionCutSeq(msgs, 5); got != 0 {
		t.Fatalf("cut = %d, want 0", got)
	}
	// keepTurns 非法时至少保留 1 轮。
	if got := compactionCutSeq(msgs, 0); got != 8 {
		t.Fatalf("cut = %d, want 8", got)
	}
}

func TestMessagesInSeqRange(t *testing.T) {
	msgs := []Message{{Seq: 0}, {Seq: 1}, {Seq: 2}, {Seq: 3}}
	got := messagesInSeqRange(msgs, noSummarySeq, 3)
	if len(got) != 3 || got[0].Seq != 0 || got[2].Seq != 2 {
		t.Fatalf("range = %+v, want seq 0..2", got)
	}
	if got := messagesInSeqRange(msgs, 1, 3); len(got) != 1 || got[0].Seq != 2 {
		t.Fatalf("range = %+v, want seq 2", got)
	}
}

func TestRenderMessagesForSummary(t *testing.T) {
	msgs := []Message{
		{Role: RoleUser, Kind: KindUser, Content: "分析差异"},
		{Role: RoleAssistant, Kind: KindReasoning, Data: ReasoningBlock{Content: "思考内容"}},
		{Role: RoleAssistant, Kind: KindToolCall, ToolCallID: "c1",
			Data: ToolCall{ID: "c1", Name: "read", Arguments: map[string]any{"path": "/a"}}},
		{Role: RoleAssistant, Kind: KindToolResult, ToolCallID: "c1",
			Data: ToolResultBlock{ToolCallID: "c1", Name: "read", Content: strings.Repeat("x", summaryToolResultMaxRunes+100)}},
		{Role: RoleAssistant, Kind: KindError, Content: "boom"},
		{Role: RoleAssistant, Kind: KindAssistant, Content: "结论"},
	}

	got := renderMessagesForSummary(msgs, summaryInputMaxRunes)
	for _, want := range []string{"用户: 分析差异", "工具调用 read(", "工具结果 read:", "助手: 结论"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered summary missing %q:\n%s", want, got)
		}
	}
	for _, banned := range []string{"思考内容", "boom"} {
		if strings.Contains(got, banned) {
			t.Fatalf("rendered summary contains %q:\n%s", banned, got)
		}
	}
	// 单条工具输出被截断，不会把整段原文塞进摘要输入。
	if strings.Contains(got, strings.Repeat("x", summaryToolResultMaxRunes+1)) {
		t.Fatal("tool result was not truncated")
	}
}

func TestHistoryPromptInjectsSummary(t *testing.T) {
	svc := &AgentService{}
	msgs := []Message{
		{Seq: 0, TaskID: 1, Role: RoleUser, Kind: KindUser, Content: "旧问题"},
		{Seq: 1, TaskID: 1, Role: RoleAssistant, Kind: KindAssistant, Content: "旧回答"},
		{Seq: 2, TaskID: 2, Role: RoleUser, Kind: KindUser, Content: "新问题"},
		{Seq: 3, TaskID: 2, Role: RoleAssistant, Kind: KindAssistant, Content: "本轮已产生，应由回放补齐"},
	}
	summary := &SessionSummary{SessionID: 1, UpToSeq: 1, Content: "用户想解决旧问题"}

	out := svc.historyPrompt(msgs, summary, &Task{ID: 2})
	if len(out) != 2 {
		t.Fatalf("len = %d, want summary + new user: %+v", len(out), out)
	}
	if out[0].Role != RoleUser || !strings.Contains(out[0].Content, "用户想解决旧问题") {
		t.Fatalf("summary message = %+v", out[0])
	}
	if out[1].Content != "新问题" {
		t.Fatalf("kept message = %+v", out[1])
	}
	for _, m := range out {
		if strings.Contains(m.Content, "旧回答") || strings.Contains(m.Content, "本轮已产生") {
			t.Fatalf("unexpected message kept: %+v", m)
		}
	}
}

func TestEstimateTextTokens(t *testing.T) {
	if got := estimateTextTokens(""); got != 0 {
		t.Fatalf("empty = %d", got)
	}
	if got := estimateTextTokens("abcdefgh"); got != 2 {
		t.Fatalf("ascii = %d, want 2", got)
	}
	if got := estimateTextTokens("你好世界"); got != 6 {
		t.Fatalf("cjk = %d, want 6", got)
	}
	if got := estimateTokens([]Message{{Role: RoleUser, Content: "你好"}}); got < 10 {
		t.Fatalf("estimateTokens = %d, want structural overhead included", got)
	}
}
