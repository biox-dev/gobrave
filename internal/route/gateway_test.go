package route

import (
	"context"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
)

func newTestGateway(t *testing.T) *Gateway {
	t.Helper()

	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(gormlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	r, err := NewGateway(db)
	if err != nil {
		t.Fatalf("create gateway: %v", err)
	}

	return r
}

func newTestGatewayWithDB(t *testing.T, dsn string) *Gateway {
	t.Helper()

	db, err := gorm.Open(gormlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	r, err := NewGateway(db)
	if err != nil {
		t.Fatalf("create gateway: %v", err)
	}

	return r
}

func TestGatewayPersistAndReload(t *testing.T) {
	t.Parallel()

	dsn := "file:" + t.TempDir() + "/gateway_route.db?_journal_mode=WAL"
	r := newTestGatewayWithDB(t, dsn)

	err := r.UpsertRoute(context.Background(), Registration{
		RouteKey:   "app-session-1",
		PathPrefix: "/apps/1",
		Backend: Backend{
			Host: "172.18.0.2",
			Port: 8888,
		},
	})
	if err != nil {
		t.Fatalf("upsert route: %v", err)
	}

	r2 := newTestGatewayWithDB(t, dsn)

	got, prefix, ok := r2.ResolveByPath("/apps/1/lab")
	if !ok {
		t.Fatalf("route not found after reload")
	}
	if prefix != "/apps/1" {
		t.Fatalf("unexpected matched prefix: got=%s", prefix)
	}
	if got.Backend.Host != "172.18.0.2" || got.Backend.Port != 8888 {
		t.Fatalf("unexpected backend: %+v", got.Backend)
	}
}

func TestGatewayLongestPrefixMatch(t *testing.T) {
	t.Parallel()

	r := newTestGateway(t)

	if err := r.UpsertRoute(context.Background(), Registration{
		RouteKey:   "apps-root",
		PathPrefix: "/apps",
		Backend:    Backend{Host: "172.18.0.3", Port: 80},
	}); err != nil {
		t.Fatalf("upsert root route: %v", err)
	}

	if err := r.UpsertRoute(context.Background(), Registration{
		RouteKey:   "app-session-2",
		PathPrefix: "/apps/2",
		Backend:    Backend{Host: "172.18.0.4", Port: 7860},
	}); err != nil {
		t.Fatalf("upsert specific route: %v", err)
	}

	got, _, ok := r.ResolveByPath("/apps/2/workspace")
	if !ok {
		t.Fatalf("route not found")
	}
	if got.RouteKey != "app-session-2" {
		t.Fatalf("longest prefix not selected, got route key=%s", got.RouteKey)
	}
}
