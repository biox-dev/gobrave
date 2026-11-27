package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// fakeSessionTitleUpdater 是最小的 SessionTitleUpdater 实现，用于验证工具行为。
type fakeSessionTitleUpdater struct {
	gotSessionID int64
	gotUserID    int64
	gotTitle     string
}

func (f *fakeSessionTitleUpdater) UpdateSessionTitle(_ context.Context, sessionID, userID int64, title string) (runtimecontext.SessionTitleUpdateResult, error) {
	f.gotSessionID = sessionID
	f.gotUserID = userID
	f.gotTitle = title
	return runtimecontext.SessionTitleUpdateResult{SessionID: sessionID, Title: title}, nil
}

// denySessionTitleRequester 记录授权请求并一律拒绝。
type denySessionTitleRequester struct {
	got tool.PermissionRequest
}

func (d *denySessionTitleRequester) RequestPermission(_ context.Context, req tool.PermissionRequest) bool {
	d.got = req
	return false
}

func TestUpdateSessionTitleUsesFactsAndNormalizesTitle(t *testing.T) {
	u := &fakeSessionTitleUpdater{}
	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{SessionID: 42, UserID: 7})

	out, err := UpdateSessionTitle(u).Execute(ctx,
		json.RawMessage(`{"title":"  \"差异分析  v2\"。\n"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if u.gotSessionID != 42 || u.gotUserID != 7 {
		t.Fatalf("session/user = %d/%d, want 42/7", u.gotSessionID, u.gotUserID)
	}
	if u.gotTitle != "差异分析 v2" {
		t.Fatalf("title = %q, want %q", u.gotTitle, "差异分析 v2")
	}
	result, ok := out.(UpdateSessionTitleOutput)
	if !ok {
		t.Fatalf("output type = %T", out)
	}
	if result.SessionID != 42 || result.Title != "差异分析 v2" {
		t.Fatalf("output = %+v", result)
	}
}

func TestUpdateSessionTitleRequiresSessionID(t *testing.T) {
	u := &fakeSessionTitleUpdater{}
	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{UserID: 7})

	if _, err := UpdateSessionTitle(u).Execute(ctx, json.RawMessage(`{"title":"x"}`)); err == nil {
		t.Fatal("expected error for missing session id")
	}
	if u.gotSessionID != 0 {
		t.Fatalf("updater should not be called, got session %d", u.gotSessionID)
	}
}

func TestUpdateSessionTitleRejectsEmptyTitle(t *testing.T) {
	u := &fakeSessionTitleUpdater{}
	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{SessionID: 42, UserID: 7})

	if _, err := UpdateSessionTitle(u).Execute(ctx, json.RawMessage(`{"title":"  \"\"  "}`)); err == nil {
		t.Fatal("expected error for empty title")
	}
}

func TestUpdateSessionTitleTruncatesLongTitle(t *testing.T) {
	u := &fakeSessionTitleUpdater{}
	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{SessionID: 42, UserID: 7})

	long := strings.Repeat("标", sessionTitleMaxRunes+10)
	if _, err := UpdateSessionTitle(u).Execute(ctx, mustJSON(t, map[string]string{"title": long})); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := len([]rune(u.gotTitle)); got != sessionTitleMaxRunes {
		t.Fatalf("title length = %d, want %d", got, sessionTitleMaxRunes)
	}
}

func TestUpdateSessionTitlePermissionDenied(t *testing.T) {
	u := &fakeSessionTitleUpdater{}
	requester := &denySessionTitleRequester{}
	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{SessionID: 42, UserID: 7})
	ctx = tool.WithPermissionRequester(ctx, requester)

	if _, err := UpdateSessionTitle(u).Execute(ctx, json.RawMessage(`{"title":"new"}`)); err == nil {
		t.Fatal("expected permission denied error")
	}
	if u.gotSessionID != 0 {
		t.Fatalf("updater should not be called on denial, got session %d", u.gotSessionID)
	}
	if requester.got.ToolName != UpdateSessionTitleToolName || requester.got.Action != "write" {
		t.Fatalf("permission request = %+v", requester.got)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return data
}
