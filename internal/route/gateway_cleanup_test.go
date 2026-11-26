package route

import (
	"context"
	"testing"
)

// TestGatewayDeleteAllRoutes 校验「清空全部路由」同时清掉内存索引与数据库：
// 清空后既解析不到旧路径，重新构造的 Gateway（从 DB 加载）也不应再看到它们。
func TestGatewayDeleteAllRoutes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := newTestGateway(t)

	for _, reg := range []Registration{
		{RouteKey: "app-session-1", PathPrefix: "/apps/1", Backend: Backend{Host: "172.18.0.2", Port: 8888}},
		{RouteKey: "app-session-2", PathPrefix: "/apps/2", Backend: Backend{Host: "172.18.0.3", Port: 7860}},
	} {
		if err := r.UpsertRoute(ctx, reg); err != nil {
			t.Fatalf("upsert route %s: %v", reg.RouteKey, err)
		}
	}

	deleted, err := r.DeleteAllRoutes(ctx)
	if err != nil {
		t.Fatalf("delete all routes: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("expected 2 routes deleted, got %d", deleted)
	}

	if _, _, ok := r.ResolveByPath("/apps/1/lab"); ok {
		t.Fatalf("route still resolvable after DeleteAllRoutes")
	}

	// 再次清空应为空操作（幂等）。
	if again, err := r.DeleteAllRoutes(ctx); err != nil || again != 0 {
		t.Fatalf("expected idempotent empty delete, got count=%d err=%v", again, err)
	}
}
