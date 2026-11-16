package service_test

import (
	"context"
	"strings"
	"testing"

	agentrepository "github.com/biox-dev/gobrave/internal/agent/application/repository"
	. "github.com/biox-dev/gobrave/internal/agent/application/service"
	. "github.com/biox-dev/gobrave/internal/agent/types"
)

// newTestProfilePromptWriter 创建一个已同步内置 Profile 的提示词写入器。
func newTestProfilePromptWriter(t *testing.T, overwrite bool) (*ProfilePromptWriter, *ProfileManager) {
	t.Helper()
	repo := agentrepository.NewGormProfileRepository(newTestDB(t))
	if err := EnsureBuiltinProfiles(context.Background(), repo, true); err != nil {
		t.Fatalf("seed builtin profiles: %v", err)
	}
	m := NewProfileManager(repo)
	return NewProfilePromptWriter(m, overwrite), m
}

func TestProfilePromptWriterPlanEditUniqueReplacement(t *testing.T) {
	ctx := context.Background()
	w, m := newTestProfilePromptWriter(t, true)

	profile, err := m.Resolve(ctx, BuiltinDefaultProfileID)
	if err != nil {
		t.Fatalf("resolve default: %v", err)
	}
	if !strings.Contains(profile.SystemPrompt, "工作方式") {
		t.Fatalf("builtin prompt missing expected heading: %q", profile.SystemPrompt)
	}

	plan, err := w.PlanEdit(ctx, BuiltinDefaultProfileID, "工作方式", "工作方式（已调整）")
	if err != nil {
		t.Fatalf("PlanEdit: %v", err)
	}
	if plan.ProfileID != BuiltinDefaultProfileID {
		t.Fatalf("plan profile id = %d, want %d", plan.ProfileID, BuiltinDefaultProfileID)
	}
	if !strings.Contains(plan.After, "工作方式（已调整）") {
		t.Fatalf("plan.After missing replacement: %q", plan.After)
	}
	if plan.FilePath == "" {
		t.Fatalf("plan.FilePath is empty")
	}
	// 内置 Profile + overwrite=true：应提醒重启后会被覆盖。
	if plan.Notice == "" {
		t.Fatalf("expected overwrite notice for builtin profile")
	}

	info, err := w.Apply(ctx, plan)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if info.ProfileID != BuiltinDefaultProfileID {
		t.Fatalf("info profile id = %d", info.ProfileID)
	}

	saved, err := m.Get(ctx, BuiltinDefaultProfileID)
	if err != nil {
		t.Fatalf("get saved: %v", err)
	}
	if !strings.Contains(saved.SystemPrompt, "工作方式（已调整）") {
		t.Fatalf("saved prompt missing replacement")
	}
}

func TestProfilePromptWriterRejectsNonUniqueAndMissing(t *testing.T) {
	ctx := context.Background()
	w, _ := newTestProfilePromptWriter(t, false)

	if _, err := w.PlanEdit(ctx, BuiltinDefaultProfileID, "不存在的文本 XYZ", "x"); err == nil {
		t.Fatalf("missing old_string should fail")
	}
	if _, err := w.PlanEdit(ctx, BuiltinDefaultProfileID, "", "x"); err == nil {
		t.Fatalf("empty old_string should fail")
	}
}

func TestProfilePromptWriterNoNoticeWhenOverwriteDisabled(t *testing.T) {
	ctx := context.Background()
	w, _ := newTestProfilePromptWriter(t, false)

	plan, err := w.PlanEdit(ctx, BuiltinDefaultProfileID, "工作方式", "工作方式2")
	if err != nil {
		t.Fatalf("PlanEdit: %v", err)
	}
	if plan.Notice != "" {
		t.Fatalf("notice = %q, want empty when overwrite disabled", plan.Notice)
	}
}
