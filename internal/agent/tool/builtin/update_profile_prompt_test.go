package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// fakeProfilePromptWriter 是最小的 ProfilePromptWriter 实现，用于验证工具行为。
type fakeProfilePromptWriter struct {
	gotProfileID int64
	gotOld       string
	gotNew       string
	notice       string
	planErr      error
	applied      *runtimecontext.ProfilePromptPlan
}

func (f *fakeProfilePromptWriter) PlanEdit(_ context.Context, profileID int64, oldString, newString string) (runtimecontext.ProfilePromptPlan, error) {
	f.gotProfileID = profileID
	f.gotOld = oldString
	f.gotNew = newString
	if f.planErr != nil {
		return runtimecontext.ProfilePromptPlan{}, f.planErr
	}
	return runtimecontext.ProfilePromptPlan{
		ProfileID: profileID,
		Name:      "通用助手",
		FilePath:  "agent_profiles/-1/system_prompt.md",
		Before:    "line1\n" + oldString + "\nline3\n",
		After:     "line1\n" + newString + "\nline3\n",
		Notice:    f.notice,
	}, nil
}

func (f *fakeProfilePromptWriter) Apply(_ context.Context, plan runtimecontext.ProfilePromptPlan) (runtimecontext.ProfilePromptInfo, error) {
	f.applied = &plan
	return runtimecontext.ProfilePromptInfo{ProfileID: plan.ProfileID, Name: plan.Name, Notice: plan.Notice}, nil
}

// denyProfileRequester 记录授权请求并一律拒绝。
type denyProfileRequester struct {
	got tool.PermissionRequest
}

func (d *denyProfileRequester) RequestPermission(_ context.Context, req tool.PermissionRequest) bool {
	d.got = req
	return false
}

func TestUpdateProfilePromptUsesProfileIDFromFacts(t *testing.T) {
	w := &fakeProfilePromptWriter{}
	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{ProfileID: agenttypes.BuiltinDefaultProfileID})

	_, err := UpdateProfilePrompt(w).Execute(ctx,
		json.RawMessage(`{"old_string":"old line","new_string":"new line"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if w.gotProfileID != agenttypes.BuiltinDefaultProfileID {
		t.Fatalf("profile id = %d, want %d", w.gotProfileID, agenttypes.BuiltinDefaultProfileID)
	}
	if w.gotOld != "old line" || w.gotNew != "new line" {
		t.Fatalf("old/new = %q/%q", w.gotOld, w.gotNew)
	}
}

// 未注入权限请求器时，RequirePermission 视为放行，工具应成功写入。
func TestUpdateProfilePromptAppliesAndReturnsNotice(t *testing.T) {
	w := &fakeProfilePromptWriter{notice: "builtin profile: will be overwritten on restart"}
	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{ProfileID: agenttypes.BuiltinDefaultProfileID})

	out, err := UpdateProfilePrompt(w).Execute(ctx,
		json.RawMessage(`{"old_string":"old line","new_string":"new line"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := out.(UpdateProfilePromptOutput)
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if res.Notice != w.notice {
		t.Fatalf("notice = %q, want %q", res.Notice, w.notice)
	}
	if w.applied == nil {
		t.Fatalf("Apply was not called")
	}
}

func TestUpdateProfilePromptPermissionDenied(t *testing.T) {
	w := &fakeProfilePromptWriter{}
	requester := &denyProfileRequester{}
	ctx := tool.WithPermissionRequester(context.Background(), requester)
	ctx = tool.WithTaskFacts(ctx, agenttypes.TaskFacts{ProfileID: agenttypes.BuiltinDefaultProfileID})

	_, err := UpdateProfilePrompt(w).Execute(ctx,
		json.RawMessage(`{"old_string":"old line","new_string":"new line"}`))
	if !errors.Is(err, tool.ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	if w.applied != nil {
		t.Fatalf("Apply should not be called when denied")
	}
	if requester.got.Action != "write" {
		t.Fatalf("permission action = %q, want write", requester.got.Action)
	}
	// 授权请求必须带上与 coder edit / write 同键的 diff 预览。
	preview, ok := requester.got.Params.(tool.PermissionPreview)
	if !ok || preview.FilePath == "" || !strings.Contains(preview.Diff, "new line") {
		t.Fatalf("permission params = %#v, want file_path + diff preview", requester.got.Params)
	}
}

func TestUpdateProfilePromptPlanErrorIsReported(t *testing.T) {
	w := &fakeProfilePromptWriter{planErr: errors.New("old_string not found in system prompt")}
	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{ProfileID: agenttypes.BuiltinDefaultProfileID})

	out, err := UpdateProfilePrompt(w).Execute(ctx,
		json.RawMessage(`{"old_string":"missing","new_string":"x"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	res := out.(UpdateProfilePromptOutput)
	if !strings.Contains(res.Error, "old_string not found") {
		t.Fatalf("error = %q", res.Error)
	}
	if w.applied != nil {
		t.Fatalf("Apply should not be called on plan error")
	}
}
