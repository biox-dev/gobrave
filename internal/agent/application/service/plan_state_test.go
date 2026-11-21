package service

import (
	"context"
	"testing"
	"time"

	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/utils"
)

// fakePlanStore 是内存版 PlanStore：计划头 + 节点各自一份 map，语义与 GORM 实现对齐。
type fakePlanStore struct {
	plan  *agenttypes.AgentPlan
	items map[int64]*agenttypes.PlanItem
}

func newFakePlanStore() *fakePlanStore {
	return &fakePlanStore{items: map[int64]*agenttypes.PlanItem{}}
}

func (f *fakePlanStore) Save(_ context.Context, plan *agenttypes.AgentPlan) error {
	if plan.ID == 0 {
		plan.ID = utils.GenerateID()
	}
	f.plan = plan

	keep := make(map[int64]struct{}, len(plan.Items))
	for i := range plan.Items {
		// 与 GORM 实现一致：节点归属由计划头回填。
		plan.Items[i].PlanID = plan.ID
		item := plan.Items[i]
		keep[item.ID] = struct{}{}
		existing, ok := f.items[item.ID]
		if !ok {
			copied := item
			if copied.Status == "" {
				copied.Status = agenttypes.PlanItemPending
			}
			copied.CreatedAt = time.Now()
			f.items[item.ID] = &copied
			continue
		}
		// 与 GORM 实现一致：只覆盖结构列，保留 status / session_id / note。
		existing.PlanID = item.PlanID
		existing.ParentID = item.ParentID
		existing.Title = item.Title
		existing.Summary = item.Summary
		existing.Source = item.Source
		existing.SourceID = item.SourceID
		existing.SortOrder = item.SortOrder
	}
	for id := range f.items {
		if _, ok := keep[id]; !ok {
			delete(f.items, id)
		}
	}
	return nil
}

func (f *fakePlanStore) collect(planID int64) []agenttypes.PlanItem {
	out := make([]agenttypes.PlanItem, 0, len(f.items))
	for _, item := range f.items {
		if item.PlanID == planID {
			out = append(out, *item)
		}
	}
	return out
}

func (f *fakePlanStore) Get(_ context.Context, id int64) (*agenttypes.AgentPlan, error) {
	if f.plan == nil || f.plan.ID != id {
		return nil, agenttypes.ErrPlanNotFound
	}
	copied := *f.plan
	copied.Items = f.collect(id)
	return &copied, nil
}

func (f *fakePlanStore) ListByProject(_ context.Context, _ int64) ([]*agenttypes.AgentPlan, error) {
	if f.plan == nil {
		return nil, nil
	}
	one, err := f.Get(context.Background(), f.plan.ID)
	if err != nil {
		return nil, err
	}
	return []*agenttypes.AgentPlan{one}, nil
}

func (f *fakePlanStore) Delete(_ context.Context, id int64) error {
	if f.plan != nil && f.plan.ID == id {
		f.plan = nil
	}
	for itemID, item := range f.items {
		if item.PlanID == id {
			delete(f.items, itemID)
		}
	}
	return nil
}

func (f *fakePlanStore) UpdateItemStatus(_ context.Context, planID, sessionID int64, updates []agenttypes.PlanItemStatusUpdate) (int64, error) {
	var affected int64
	for _, update := range updates {
		item, ok := f.items[update.ItemID]
		if !ok || item.PlanID != planID {
			return affected, agenttypes.ErrPlanItemNotFound
		}
		item.Status = update.Status
		item.SessionID = sessionID
		item.Note = update.Note
		affected++
	}
	return affected, nil
}

// TestUpdateItemStatus 覆盖节点状态上报：
// 只写状态列、未上报节点保持 pending、不存在 / 非法状态返回错误、重复上报覆盖。
func TestUpdateItemStatus(t *testing.T) {
	_ = utils.InitSnowflake(1)
	ctx := context.Background()

	store := newFakePlanStore()
	svc := NewPlanService(store, nil)

	plan, err := svc.Save(ctx, &agenttypes.AgentPlan{
		ProjectID: 99,
		Title:     "差异分析计划",
		Items: []agenttypes.PlanItem{
			{Title: "准备数据"},
			{Title: "差异分析"},
		},
	})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	first, second := plan.Items[0].ID, plan.Items[1].ID

	if _, err := svc.UpdateItemStatus(ctx, plan.ID, 7, []agenttypes.PlanItemStatusUpdate{
		{ItemID: first, Status: agenttypes.PlanItemInProgress},
	}); err != nil {
		t.Fatalf("update status: %v", err)
	}

	got, err := svc.Get(ctx, plan.ID)
	if err != nil {
		t.Fatalf("get plan: %v", err)
	}
	if got.Items[0].Status != agenttypes.PlanItemInProgress {
		t.Fatalf("item[0] status = %q, want in_progress", got.Items[0].Status)
	}
	if got.Items[1].Status != agenttypes.PlanItemPending {
		t.Fatalf("item[1] status = %q, want pending", got.Items[1].Status)
	}

	// 重复上报覆盖为最新状态，并记录会话 ID。
	if _, err := svc.UpdateItemStatus(ctx, plan.ID, 7, []agenttypes.PlanItemStatusUpdate{
		{ItemID: first, Status: agenttypes.PlanItemCompleted, Note: "ok"},
	}); err != nil {
		t.Fatalf("update status again: %v", err)
	}
	got, _ = svc.Get(ctx, plan.ID)
	if got.Items[0].Status != agenttypes.PlanItemCompleted {
		t.Fatalf("item[0] status = %q, want completed", got.Items[0].Status)
	}
	if row := store.items[first]; row == nil || row.SessionID != 7 || row.Note != "ok" {
		t.Fatalf("item row = %+v, want session 7 / note ok", row)
	}

	// 未知节点、非法状态必须报错，避免 agent 写错目标。
	if _, err := svc.UpdateItemStatus(ctx, plan.ID, 7, []agenttypes.PlanItemStatusUpdate{
		{ItemID: 1, Status: agenttypes.PlanItemCompleted},
	}); err == nil {
		t.Fatal("expected error for unknown item id")
	}
	if _, err := svc.UpdateItemStatus(ctx, plan.ID, 7, []agenttypes.PlanItemStatusUpdate{
		{ItemID: second, Status: "done"},
	}); err == nil {
		t.Fatal("expected error for invalid status")
	}
}

// TestUpdatePlanKeepsItemStatus 覆盖「用户编辑计划不会清掉进度」：
// 整体覆盖结构后，已有节点的 status 必须保留，新增节点为 pending。
func TestUpdatePlanKeepsItemStatus(t *testing.T) {
	_ = utils.InitSnowflake(1)
	ctx := context.Background()

	store := newFakePlanStore()
	svc := NewPlanService(store, nil)

	plan, err := svc.Save(ctx, &agenttypes.AgentPlan{
		ProjectID: 99,
		Title:     "计划",
		Items:     []agenttypes.PlanItem{{Title: "A"}, {Title: "B"}},
	})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	itemA := plan.Items[0].ID

	if _, err := svc.UpdateItemStatus(ctx, plan.ID, 7, []agenttypes.PlanItemStatusUpdate{
		{ItemID: itemA, Status: agenttypes.PlanItemCompleted},
	}); err != nil {
		t.Fatalf("update status: %v", err)
	}

	// 前端整体覆盖：保留 A（改标题）、移除 B、新增 C。
	updated, err := svc.Save(ctx, &agenttypes.AgentPlan{
		ID:        plan.ID,
		ProjectID: 99,
		Title:     "计划",
		Items: []agenttypes.PlanItem{
			{ID: itemA, Title: "A 改名"},
			{Title: "C"},
		},
	})
	if err != nil {
		t.Fatalf("re-save plan: %v", err)
	}
	if len(updated.Items) != 2 {
		t.Fatalf("item count = %d, want 2", len(updated.Items))
	}
	if updated.Items[0].Title != "A 改名" {
		t.Fatalf("item[0] title = %q, want renamed", updated.Items[0].Title)
	}
	if updated.Items[0].Status != agenttypes.PlanItemCompleted {
		t.Fatalf("item[0] status = %q, want completed (progress must survive)", updated.Items[0].Status)
	}
	if updated.Items[1].Status != agenttypes.PlanItemPending {
		t.Fatalf("item[1] status = %q, want pending", updated.Items[1].Status)
	}
}

// TestDeletePlanRemovesItems 覆盖删除计划时级联清理节点。
func TestDeletePlanRemovesItems(t *testing.T) {
	_ = utils.InitSnowflake(1)
	ctx := context.Background()

	store := newFakePlanStore()
	svc := NewPlanService(store, nil)

	plan, err := svc.Save(ctx, &agenttypes.AgentPlan{
		ProjectID: 99,
		Title:     "计划",
		Items:     []agenttypes.PlanItem{{Title: "A"}},
	})
	if err != nil {
		t.Fatalf("save plan: %v", err)
	}
	if err := svc.Delete(ctx, plan.ID); err != nil {
		t.Fatalf("delete plan: %v", err)
	}
	if len(store.items) != 0 {
		t.Fatalf("items left after delete = %d, want 0", len(store.items))
	}
}
