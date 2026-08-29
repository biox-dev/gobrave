package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/biox-dev/gobrave/internal/agent"
	agentproviders "github.com/biox-dev/gobrave/internal/agent/providers"
)

// newTestService 构建一个使用 mock Provider 的 AgentService。
func newTestService() *agent.AgentService {
	registry := agent.NewRegistry(agentproviders.All()...)
	client := agent.NewClient(registry, agent.ProviderMock, agent.Options{Model: "demo"})
	return agent.NewService(agent.ServiceConfig{Client: client})
}

// TestServiceFullPermissionFlow 演示完整链路：
// 创建任务 → 流式输出 → 请求权限（write → ask）→ 批准 → 恢复 → 完成。
func TestServiceFullPermissionFlow(t *testing.T) {
	svc := newTestService()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events := make(chan agent.AgentEvent, 128)
	unsub := svc.Subscribe("", func(_ context.Context, ev agent.AgentEvent) error {
		events <- ev
		return nil
	})
	defer unsub()

	req := agent.Request{
		Provider:   agent.ProviderMock,
		Stream:     true,
		WorkingDir: "/tmp",
		Env:        map[string]string{"MOCK_PERMISSION_DEMO": "true"},
		Messages:   []agent.Message{{Role: agent.RoleUser, Content: "hello"}},
	}

	task, err := svc.RunTask(ctx, req, nil)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	// 等待 permission.created 事件。
	var permID string
	for {
		ev := readEvent(t, ctx, events)
		if ev.Type == agent.EventPermissionCreated {
			perm, ok := ev.Payload.(*agent.PermissionRequest)
			if !ok {
				t.Fatalf("unexpected payload type %T", ev.Payload)
			}
			permID = perm.ID
			break
		}
		if ev.Type == agent.EventTaskFailed || ev.Type == agent.EventTaskCompleted {
			t.Fatalf("task finished before permission: %s", ev.Type)
		}
	}

	// 校验任务进入 waiting_permission，且权限可被查询。
	if got, _ := svc.GetTask(ctx, task.ID); got.Status != agent.TaskWaitingPermission {
		t.Fatalf("task status = %s, want waiting_permission", got.Status)
	}
	pending, err := svc.GetPendingPermissions(ctx, task.ID)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending permissions = %d (err=%v), want 1", len(pending), err)
	}

	// 批准权限。
	if err := svc.ApprovePermission(ctx, permID, "tester"); err != nil {
		t.Fatalf("ApprovePermission: %v", err)
	}

	// 等待任务完成。
	for {
		ev := readEvent(t, ctx, events)
		switch ev.Type {
		case agent.EventTaskCompleted:
			goto done
		case agent.EventTaskFailed:
			t.Fatalf("task failed: %v", ev.Payload)
		}
	}
done:

	// 终态校验。
	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != agent.TaskCompleted {
		t.Fatalf("task status = %s, want completed", got.Status)
	}

	// 事件序列校验：sequence 单调递增，且包含权限创建 / 决策 / 流式 / 完成。
	evs, err := svc.GetEvents(ctx, task.ID, 0)
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	seen := map[agent.AgentEventType]bool{}
	for i, e := range evs {
		if i > 0 && e.Sequence <= evs[i-1].Sequence {
			t.Fatalf("sequence not increasing: %d then %d", evs[i-1].Sequence, e.Sequence)
		}
		seen[e.Type] = true
	}
	for _, want := range []agent.AgentEventType{
		agent.EventTaskCreated,
		agent.EventTaskStarted,
		agent.EventTaskWaiting,
		agent.EventPermissionCreated,
		agent.EventPermissionResolved,
		agent.EventStream,
		agent.EventTaskCompleted,
	} {
		if !seen[want] {
			t.Errorf("missing event type %s (seen=%v)", want, seen)
		}
	}
}

// TestServiceDenyPermissionFlow 演示拒绝路径：拒绝后任务失败。
func TestServiceDenyPermissionFlow(t *testing.T) {
	svc := newTestService()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	events := make(chan agent.AgentEvent, 128)
	unsub := svc.Subscribe("", func(_ context.Context, ev agent.AgentEvent) error {
		events <- ev
		return nil
	})
	defer unsub()

	req := agent.Request{
		Provider:   agent.ProviderMock,
		Stream:     true,
		WorkingDir: "/tmp",
		Env:        map[string]string{"MOCK_PERMISSION_DEMO": "true"},
		Messages:   []agent.Message{{Role: agent.RoleUser, Content: "hello"}},
	}

	task, err := svc.RunTask(ctx, req, nil)
	if err != nil {
		t.Fatalf("RunTask: %v", err)
	}

	var permID string
	for {
		ev := readEvent(t, ctx, events)
		if ev.Type == agent.EventPermissionCreated {
			permID = ev.Payload.(*agent.PermissionRequest).ID
			break
		}
	}

	if err := svc.DenyPermission(ctx, permID, "tester"); err != nil {
		t.Fatalf("DenyPermission: %v", err)
	}

	for {
		ev := readEvent(t, ctx, events)
		if ev.Type == agent.EventTaskFailed {
			break
		}
	}

	got, _ := svc.GetTask(ctx, task.ID)
	if got.Status != agent.TaskFailed {
		t.Fatalf("task status = %s, want failed", got.Status)
	}
	if got.Error == "" {
		t.Fatalf("expected error recorded on task, got empty error")
	}
}

// TestStandaloneInvoke 验证 Client.Invoke 的一次性调用语义（AI 摘要等场景）不受影响。
// func TestStandaloneInvoke(t *testing.T) {
// 	registry := agent.NewRegistry(agentproviders.All()...)
// 	client := agent.NewClient(registry, agent.ProviderMock, agent.Options{Model: "demo"})

// 	result, err := client.Invoke(context.Background(), agent.Request{
// 		Messages: []agent.Message{{Role: agent.RoleUser, Content: "hi"}},
// 	})
// 	if err != nil {
// 		t.Fatalf("Invoke: %v", err)
// 	}
// 	if result == nil || result.Content == "" {
// 		t.Fatalf("empty result: %+v", result)
// 	}
// }

// TestRecoverInterruptedTask 验证后端重启后，无活跃 goroutine 的 running 任务被标记为失败。
func TestRecoverInterruptedTask(t *testing.T) {
	ctx := context.Background()

	tasks := agent.NewMemoryTaskRepository()
	task := agent.NewTask(agent.Request{Provider: agent.ProviderMock})
	task.Status = agent.TaskRunning
	if err := tasks.Update(ctx, task); err != nil {
		t.Fatalf("seed task: %v", err)
	}

	svc := agent.NewService(agent.ServiceConfig{Tasks: tasks})

	if err := svc.Recover(ctx); err != nil {
		t.Fatalf("Recover: %v", err)
	}

	got, err := svc.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != agent.TaskFailed {
		t.Fatalf("task status = %s, want failed after recover", got.Status)
	}
	if got.Error == "" {
		t.Fatalf("expected error recorded on recovered task")
	}
}

// ---- helpers ----

func readEvent(t *testing.T, ctx context.Context, ch <-chan agent.AgentEvent) agent.AgentEvent {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-ctx.Done():
		t.Fatalf("timed out waiting for event")
		return agent.AgentEvent{}
	}
}
