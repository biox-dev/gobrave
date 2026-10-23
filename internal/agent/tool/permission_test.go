package tool

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

type permissionRequesterFunc func(context.Context, PermissionRequest) bool

func (f permissionRequesterFunc) RequestPermission(ctx context.Context, req PermissionRequest) bool {
	return f(ctx, req)
}

type permissionDemoInput struct {
	FilePath string `json:"file_path"`
}

func TestFuncSelfRequestsPermission(t *testing.T) {
	var captured PermissionRequest
	ctx := WithTaskFacts(context.Background(), agenttypes.TaskFacts{SessionID: 42})
	ctx = WithPermissionRequester(ctx, permissionRequesterFunc(func(_ context.Context, req PermissionRequest) bool {
		captured = req
		return true
	}))

	tool := NewFunc("write_demo", "demo", Schema("", map[string]any{
		"file_path": StringProperty("path"),
	}, "file_path"), func(_ context.Context, _ permissionDemoInput) (string, error) {
		return "ok", nil
	}).WithPermission("write", "file_path", "write a file")

	got, err := tool.Execute(ctx, json.RawMessage(`{"file_path":"/tmp/a.txt"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got != "ok" {
		t.Fatalf("result = %v, want ok", got)
	}
	if captured.SessionID != "42" {
		t.Errorf("SessionID = %q, want 42", captured.SessionID)
	}
	if captured.ToolName != "write_demo" || captured.Action != "write" {
		t.Errorf("request = %+v", captured)
	}
	if captured.Path != "/tmp/a.txt" {
		t.Errorf("Path = %q, want /tmp/a.txt", captured.Path)
	}
}

func TestFuncPermissionDenied(t *testing.T) {
	called := false
	ctx := WithPermissionRequester(context.Background(), permissionRequesterFunc(func(context.Context, PermissionRequest) bool {
		return false
	}))

	tool := NewFunc("write_demo", "demo", Schema("", map[string]any{}), func(_ context.Context, _ struct{}) (string, error) {
		called = true
		return "ok", nil
	}).WithPermission("write", "", "write a file")

	if _, err := tool.Execute(ctx, nil); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("err = %v, want ErrPermissionDenied", err)
	}
	if called {
		t.Fatal("tool function must not run after permission denial")
	}
}
