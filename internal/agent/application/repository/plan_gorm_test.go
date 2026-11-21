package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/ncruces/go-sqlite3/gormlite"
	"gorm.io/gorm"
)

// newGormPlanTestDB 打开共享内存 sqlite 并迁移计划相关的两张表。
func newGormPlanTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	_ = utils.InitSnowflake(1)
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := gorm.Open(gormlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&agenttypes.AgentPlan{}, &agenttypes.PlanItem{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	return db
}

// TestGormPlanItemsRoundTrip 验证计划与节点真的落在 agent_plan_items 两行上，
// 且状态更新只改状态列、不覆盖结构。
func TestGormPlanItemsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newGormPlanTestDB(t)
	store := NewGormPlanStore(db)

	plan := &agenttypes.AgentPlan{
		ProjectID: 99,
		Title:     "计划",
		Items: []agenttypes.PlanItem{
			{ID: utils.GenerateID(), Title: "A", SortOrder: 0},
			{ID: utils.GenerateID(), Title: "B", SortOrder: 1},
		},
	}
	if err := store.Save(ctx, plan); err != nil {
		t.Fatalf("save: %v", err)
	}

	// 节点确实是一行一条。
	got, err := store.Get(ctx, plan.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(got.Items))
	}
	for _, item := range got.Items {
		if item.PlanID != plan.ID {
			t.Fatalf("item %q plan_id = %d, want %d", item.Title, item.PlanID, plan.ID)
		}
		if item.Status != agenttypes.PlanItemPending {
			t.Fatalf("item %q status = %q, want pending", item.Title, item.Status)
		}
	}

	itemA := got.Items[0].ID
	if _, err := store.UpdateItemStatus(ctx, plan.ID, 7, []agenttypes.PlanItemStatusUpdate{
		{ItemID: itemA, Status: agenttypes.PlanItemCompleted, Note: "done"},
	}); err != nil {
		t.Fatalf("update status: %v", err)
	}

	// 未知节点必须是 ErrPlanItemNotFound（而不是静默成功）。
	if _, err := store.UpdateItemStatus(ctx, plan.ID, 7, []agenttypes.PlanItemStatusUpdate{
		{ItemID: 1, Status: agenttypes.PlanItemCompleted},
	}); !errors.Is(err, agenttypes.ErrPlanItemNotFound) {
		t.Fatalf("unknown item err = %v, want ErrPlanItemNotFound", err)
	}

	// 整体覆盖结构（改标题、保留 itemA、移除 itemB）：状态必须存活。
	plan.Items = []agenttypes.PlanItem{
		{ID: itemA, Title: "A 改名", SortOrder: 0},
	}
	if err := store.Save(ctx, plan); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	got, err = store.Get(ctx, plan.ID)
	if err != nil {
		t.Fatalf("get after re-save: %v", err)
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %d, want 1 (itemB removed)", len(got.Items))
	}
	if got.Items[0].ID != itemA || got.Items[0].Title != "A 改名" {
		t.Fatalf("item = %+v, want itemA renamed", got.Items[0])
	}
	if got.Items[0].Status != agenttypes.PlanItemCompleted {
		t.Fatalf("status = %q, want completed (structure edit must not reset progress)", got.Items[0].Status)
	}
	if got.Items[0].SessionID != 7 || got.Items[0].Note != "done" {
		t.Fatalf("item = %+v, want session 7 / note done", got.Items[0])
	}

	// 删除计划级联清理节点。
	if err := store.Delete(ctx, plan.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var left int64
	if err := db.Model(&agenttypes.PlanItem{}).Where("plan_id = ?", plan.ID).Count(&left).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 0 {
		t.Fatalf("items left = %d, want 0", left)
	}
	if _, err := store.Get(ctx, plan.ID); !errors.Is(err, agenttypes.ErrPlanNotFound) {
		t.Fatalf("get after delete err = %v, want ErrPlanNotFound", err)
	}
}
