package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// fakeUIActionPusher 记录工具推送的 action / payload，避免依赖真实 WS 连接。
type fakeUIActionPusher struct {
	userID  int64
	action  string
	payload map[string]any
	calls   int
}

func (f *fakeUIActionPusher) PushUIAction(_ context.Context, userID int64, action string, payload map[string]any) error {
	f.calls++
	f.userID, f.action, f.payload = userID, action, payload
	return nil
}

func executeUIAction(t *testing.T, pusher *fakeUIActionPusher, userID int64, in UIActionInput) tool.Result {
	t.Helper()
	exec := tool.NewExecutor(tool.NewRegistryWith(UIAction(pusher)))
	args, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{UserID: userID})
	return exec.Execute(ctx, tool.Call{Name: UIActionToolName, Arguments: args})
}

func TestUIActionRouteGo(t *testing.T) {
	pusher := &fakeUIActionPusher{}
	res := executeUIAction(t, pusher, 42, UIActionInput{Action: UIActionRouteGo, Path: "agent-skill"})
	if res.IsError {
		t.Fatalf("unexpected error result: %s", res.Content)
	}
	if pusher.calls != 1 {
		t.Fatalf("push calls = %d, want 1", pusher.calls)
	}
	if pusher.userID != 42 {
		t.Fatalf("userID = %d, want 42 (from task facts)", pusher.userID)
	}
	if pusher.action != UIActionRouteGo {
		t.Fatalf("action = %q, want %q", pusher.action, UIActionRouteGo)
	}
	// 缺前导 / 时自动补齐。
	if got := pusher.payload["path"]; got != "/agent-skill" {
		t.Fatalf("payload path = %v, want /agent-skill", got)
	}
}

func TestUIActionRejectsUnknownAction(t *testing.T) {
	pusher := &fakeUIActionPusher{}
	res := executeUIAction(t, pusher, 42, UIActionInput{Action: "ui.drop_database"})
	if pusher.calls != 0 {
		t.Fatalf("push calls = %d, want 0 for unknown action", pusher.calls)
	}
	var out UIActionOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal output: %v (content=%q)", err, res.Content)
	}
	if out.Error == "" {
		t.Fatalf("expected error in output, got %+v", out)
	}
}

func TestUIActionRequiresPath(t *testing.T) {
	pusher := &fakeUIActionPusher{}
	res := executeUIAction(t, pusher, 42, UIActionInput{Action: UIActionRouteGo})
	if pusher.calls != 0 {
		t.Fatalf("push calls = %d, want 0 when path missing", pusher.calls)
	}
	var out UIActionOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if out.Error == "" {
		t.Fatalf("expected error in output, got %+v", out)
	}
}

func TestUIActionRequiresUser(t *testing.T) {
	pusher := &fakeUIActionPusher{}
	res := executeUIAction(t, pusher, 0, UIActionInput{Action: UIActionRouteGo, Path: "/x"})
	if pusher.calls != 0 {
		t.Fatalf("push calls = %d, want 0 without user", pusher.calls)
	}
	var out UIActionOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	if out.Error == "" {
		t.Fatalf("expected error in output, got %+v", out)
	}
}
