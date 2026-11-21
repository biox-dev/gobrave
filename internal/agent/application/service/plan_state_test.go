package service

import (
	"context"
	"errors"
	"testing"

	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/utils"
)

// fakePlanStore 是本文件中用到的内存 PlanStore。
type fakePlanStore struct {
	plan *agenttypes.AgentPlan
}

func (f *fakePlanStore) Save(_ context.Context, plan *agenttypes.AgentPlan) error {
	// GORM Store 的 BeforeCreate 会生成雪花 ID，内存 fake 在这里补上。
	if plan.ID == 0 {
		plan.ID = utils.GenerateID()
	}
	f.plan = plan
	return nil
}

func (f *fakePlanStore) Get(_ context.Context, id int64) (*agenttypes.AgentPlan, error) {
	if f.plan == nil || f.plan.ID != id {
		return nil, agenttypes.ErrPlanNotFound
	}
	return f.plan, nil
}

func (f *fakePlanStore) ListByProject(_ context.Context, _ int64) ([]*agenttypes.AgentPlan, error) {
	return []*agenttypes.AgentPlan{f.plan}, nil
}

func (f *fakePlanStore) Delete(_ context.Context, _ int64) error {
	f.plan = nil
	return nil
}

// fakePlanStateStore 是内存 PlanStateStore，按 (plan_id, item_id) 覆盖写入。
type fakePlanStateStore struct {
	rows map[[2]int64]*agenttypes.AgentPlanItemState
}

func newFakePlanStateStore() *fakePlanStateStore {
	return &fakePlanStateStore{rows: map[[2]int64]*agenttypes.AgentPlanItemState{}}
}

func (f *fakePlanStateStore) Upsert(_ context.Context, states []*agenttypes.AgentPlanItemState) error {
	for _, state := range states {
		f.rows[[2]int64{state.PlanID, state.ItemID}] = state
	}
	return nil
}

func (f *fakePlanStateStore) ListByPlan(_ context.Context, planID int64) ([]*agenttypes.AgentPlanItemState, error) {
	var out []*agenttypes.AgentPlanItemState
	for key, state := range f.rows {
		if key[0] == planID {
			out = append(out, state)
		}
	}
	return out, nil
}

func (f *fakePlanStateStore) DeleteByPlan(_ context.Context, planID int64) error {
	for key := range f.rows {
		if key[0] == planID {
			delete(f.rows, key)
		}
	}
	return nil
}

// TestUpdateItemStatus 覆盖节点状态上报：
// 只写运行态、未上报的节点为 pending、不存在 / 非法状态返回错误、重复上报覆盖。
func TestUpdateItemStatus(t *testing.T) {
	_ = utils.InitSnowflake(1)
	ctx := context.Background()

	store := &fakePlanStore{}
	states := newFakePlanStateStore()
	svc := NewPlanService(store, states, nil)

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
	if row := states.rows[[2]int64{plan.ID, first}]; row == nil || row.SessionID != 7 || row.Note != "ok" {
		t.Fatalf("state row = %+v, want session 7 / note ok", row)
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

	// 删除计划时级联清理运行态。
	if err := svc.Delete(ctx, plan.ID); err != nil {
		t.Fatalf("delete plan: %v", err)
	}
	if _, err := states.ListByPlan(ctx, plan.ID); err != nil && !errors.Is(err, agenttypes.ErrPlanNotFound) {
		t.Fatalf("list state after delete: %v", err)
	}
	if len(states.rows) != 0 {
		t.Fatalf("state rows left after delete = %d, want 0", len(states.rows))
	}
}
