package manager

import (
	"context"
	"sort"
	"testing"

	"github.com/biox-dev/gobrave/internal/types"
)

// TestContainerManager_DeleteAllInstances_CleansRunningAndStopped 覆盖「清理全部容器」：
//   - 运行中 / 已停止 / 卡在 create_pending 的实例都要被删除（最后一种走强制删除兜底，
//     因为 FSM 里 create_pending 没有 delete_pending 的出边）；
//   - 每个实例都调用一次运行时 Delete（docker / k8s 容器确实被回收）；
//   - 实例行全部从库里消失。
func TestContainerManager_DeleteAllInstances_CleansRunningAndStopped(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	rt := &dockerMockRuntime{}
	mgr := newTestManager(repo, rt)

	seeds := []*types.ContainerInstance{
		{OwnerType: types.ContainerOwnerAppSession, OwnerID: 1, RuntimeName: "rt-running", RuntimeID: "docker-running", Status: types.ContainerRunning, Name: "running"},
		{OwnerType: types.ContainerOwnerAppSession, OwnerID: 2, RuntimeName: "rt-stopped", RuntimeID: "docker-stopped", Status: types.ContainerStopped, Name: "stopped"},
		{OwnerType: types.ContainerOwnerAppSession, OwnerID: 3, RuntimeName: "rt-stuck", RuntimeID: "docker-stuck", Status: types.ContainerCreatePending, Name: "stuck"},
	}
	for _, inst := range seeds {
		if err := repo.CreateContainerInstance(ctx, inst); err != nil {
			t.Fatalf("seed instance failed: %v", err)
		}
	}

	result := mgr.DeleteAllInstances(ctx)
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.ContainersTotal != len(seeds) {
		t.Fatalf("expected total %d, got %d", len(seeds), result.ContainersTotal)
	}
	if result.ContainersDeleted != len(seeds) {
		t.Fatalf("expected deleted %d, got %d (errors=%v)", len(seeds), result.ContainersDeleted, result.Errors)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("expected no errors, got %v", result.Errors)
	}

	remaining, err := repo.ListContainerInstance(ctx)
	if err != nil {
		t.Fatalf("list instances failed: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected all instances removed, %d remain", len(remaining))
	}

	deleted := append([]string(nil), rt.deleted...)
	sort.Strings(deleted)
	expected := []string{"docker-running", "docker-stopped", "docker-stuck"}
	if len(deleted) != len(expected) {
		t.Fatalf("expected runtime deletes %v, got %v", expected, deleted)
	}
	for i := range expected {
		if deleted[i] != expected[i] {
			t.Fatalf("expected runtime deletes %v, got %v", expected, deleted)
		}
	}
}

// TestContainerManager_DeleteAllInstances_Empty 校验空系统下清理是安全的空操作。
func TestContainerManager_DeleteAllInstances_Empty(t *testing.T) {
	ctx := context.Background()
	repo := newMockContainerRepo()
	mgr := newTestManager(repo, &dockerMockRuntime{})

	result := mgr.DeleteAllInstances(ctx)
	if result.ContainersTotal != 0 || result.ContainersDeleted != 0 {
		t.Fatalf("expected zero counts, got total=%d deleted=%d", result.ContainersTotal, result.ContainersDeleted)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("expected no errors, got %v", result.Errors)
	}
}
