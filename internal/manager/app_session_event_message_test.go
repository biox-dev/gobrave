package manager

import (
	"testing"

	"github.com/biox-dev/gobrave/internal/types"
)

// 会话层发布的事件必须是 component.invoke(category=app-session)，前端 app-session
// 面板按 method 同名函数 refetch。
func TestAppSessionEventMessage_ComponentInvoke(t *testing.T) {
	msg, action := appSessionEventMessage(types.AppSessionEvent{
		AppSessionID:        42,
		UserID:              7,
		ContainerTemplateID: 9,
		Status:              "running",
		Method:              "sessionCreated",
		Args:                map[string]any{"persistent": true},
	})
	if action != "component.invoke" {
		t.Fatalf("expected component.invoke, got %s", action)
	}
	if msg["action"] != "component.invoke" {
		t.Fatalf("expected action=component.invoke, got %v", msg["action"])
	}
	payload, ok := msg["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload is not a map: %T", msg["payload"])
	}
	if payload["category"] != "app-session" {
		t.Fatalf("expected category=app-session, got %v", payload["category"])
	}
	if payload["id"] != "42" {
		t.Fatalf("expected id=42, got %v", payload["id"])
	}
	if payload["method"] != "sessionCreated" {
		t.Fatalf("expected method=sessionCreated, got %v", payload["method"])
	}
	args, ok := payload["args"].(map[string]any)
	if !ok {
		t.Fatalf("args is not a map: %T", payload["args"])
	}
	if args["session_id"] != "42" || args["template_id"] != "9" {
		t.Fatalf("unexpected args: %v", args)
	}
	if args["status"] != "running" {
		t.Fatalf("expected status=running, got %v", args["status"])
	}
	if args["persistent"] != true {
		t.Fatalf("expected persistent=true, got %v", args["persistent"])
	}
}

// terminal.exec 事件保持原有 payload 形状：前端据此自动打开终端标签。
func TestAppSessionEventMessage_TerminalExec(t *testing.T) {
	msg, action := appSessionEventMessage(types.AppSessionEvent{
		AppSessionID: 42,
		UserID:       7,
		Action:       "terminal.exec",
		Args:         map[string]any{"phase": "start", "command": "ls"},
	})
	if action != "terminal.exec" || msg["action"] != "terminal.exec" {
		t.Fatalf("expected terminal.exec action, got %v / %v", action, msg["action"])
	}
	payload, ok := msg["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload is not a map: %T", msg["payload"])
	}
	if payload["command"] != "ls" || payload["phase"] != "start" {
		t.Fatalf("unexpected payload: %v", payload)
	}
}

// method 为空时按删除事件兜底，避免前端收到无法匹配的 method。
func TestAppSessionEventMessage_DefaultMethod(t *testing.T) {
	msg, _ := appSessionEventMessage(types.AppSessionEvent{AppSessionID: 1, UserID: 2})
	payload, ok := msg["payload"].(map[string]any)
	if !ok {
		t.Fatalf("payload is not a map: %T", msg["payload"])
	}
	if payload["method"] != "sessionDeleted" {
		t.Fatalf("expected default method sessionDeleted, got %v", payload["method"])
	}
}

// 容器启动侧事件只更新落库状态，不再重复推送（启动通知由会话层发布）。
func TestAppSessionEventHandler_StartSideContainerEventOnlyPersists(t *testing.T) {
	repo := newMockContainerRepo()
	session := &types.AppSession{ID: 1005, UserID: 1, Status: "CREATING"}
	if err := repo.CreateAppSession(t.Context(), session); err != nil {
		t.Fatalf("seed app session failed: %v", err)
	}
	inst := &types.ContainerInstance{ID: 2005, OwnerType: types.ContainerOwnerAppSession, OwnerID: session.ID}
	if err := repo.CreateContainerInstance(t.Context(), inst); err != nil {
		t.Fatalf("seed container instance failed: %v", err)
	}

	h := NewAppSessionEventHandler(repo, nil)
	for _, evt := range []string{"ContainerCreating", "ContainerStarting", "ContainerStarted"} {
		h.Handle(types.ContainerEvent{ContainerInstanceID: inst.ID, Event: evt})
	}

	stored, err := repo.GetAppSessionByID(t.Context(), session.ID)
	if err != nil {
		t.Fatalf("load app session failed: %v", err)
	}
	if stored.Status != "RUNNING" {
		t.Fatalf("expected RUNNING, got %s", stored.Status)
	}
}
