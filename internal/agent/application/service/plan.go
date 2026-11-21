package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	agenttool "github.com/biox-dev/gobrave/internal/agent/tool"
	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/realtime"
	"github.com/biox-dev/gobrave/internal/utils"
)

// PlanRealtimeCategory 是「计划进度」实时推送的组件类目。
//
// 与前端 brave-ui/src/event-bus/stores/components.ts 注册的 category 一致：
// 后端推 component.invoke(category, id=<plan_id>, method=planProgressChanged)，
// 前端按计划 ID 注册监听，收到后重新拉取该计划详情（状态已合并）。
const PlanRealtimeCategory = "agent-plan-progress"

// PlanRealtimeMethod 是「节点状态变化」时推送的方法名。
const PlanRealtimeMethod = "planProgressChanged"

// PlanService 提供计划（AgentPlan）的增删改查实现。
//
// 计划是项目级的 agent 产物：写入前由调用方（工具）用执行事实里的 ProjectID /
// SessionID 赋值，因此这里不再做项目归属校验（越权由调用方的事实保证）。
//
// 计划结构（Items）与运行态（agent_plan_item_states）分开存放：结构可被用户整体
// 覆盖，运行态是执行事实，读计划时合并到 PlanItem.Status。
type planService struct {
	store PlanStore
	// states 为 nil 时计划不带进度（仅结构），便于只需要结构的调用方降级使用。
	states PlanStateStore
	// hub 为 nil 时不推送实时事件。
	hub *realtime.Hub
}

// NewPlanService 创建计划服务。states / hub 可为 nil（不持久化进度 / 不推送事件）。
func NewPlanService(store PlanStore, states PlanStateStore, hub *realtime.Hub) PlanService {
	return &planService{store: store, states: states, hub: hub}
}

func (s *planService) Save(ctx context.Context, plan *AgentPlan) (*AgentPlan, error) {
	if plan == nil {
		return nil, errors.New("plan is nil")
	}
	if plan.ProjectID <= 0 {
		return nil, errors.New("project_id is required")
	}
	plan.Title = strings.TrimSpace(plan.Title)
	if plan.Title == "" {
		return nil, errors.New("title is required")
	}
	plan.Items = NormalizePlanItems(plan.Items)

	now := time.Now()
	plan.UpdatedAt = now
	if plan.ID == 0 {
		plan.CreatedAt = now
	} else if plan.CreatedAt.IsZero() {
		if stored, err := s.store.Get(ctx, plan.ID); err == nil {
			plan.CreatedAt = stored.CreatedAt
		}
	}
	if err := s.store.Save(ctx, plan); err != nil {
		return nil, err
	}
	// 结构更新后合并运行态：已存在的节点保留原进度，新增节点默认 pending。
	s.mergeItemStatus(ctx, plan)
	return plan, nil
}

func (s *planService) Get(ctx context.Context, id int64) (*AgentPlan, error) {
	if id <= 0 {
		return nil, ErrPlanNotFound
	}
	plan, err := s.store.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	s.mergeItemStatus(ctx, plan)
	return plan, nil
}

func (s *planService) ListByProject(ctx context.Context, projectID int64) ([]*AgentPlan, error) {
	if projectID <= 0 {
		return nil, nil
	}
	plans, err := s.store.ListByProject(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for _, plan := range plans {
		s.mergeItemStatus(ctx, plan)
	}
	return plans, nil
}

func (s *planService) Delete(ctx context.Context, id int64) error {
	if err := s.store.Delete(ctx, id); err != nil {
		return err
	}
	if s.states != nil {
		// 运行态是计划的附属数据：计划删除后不应残留（列表 / 详情都不再引用它）。
		_ = s.states.DeleteByPlan(ctx, id)
	}
	return nil
}

// UpdateItemStatus 上报节点状态并返回合并状态后的计划。
//
// 只写运行态，不动计划结构；同一节点的重复上报覆盖为最新状态。
func (s *planService) UpdateItemStatus(ctx context.Context, planID int64, sessionID int64, updates []PlanItemStatusUpdate) (*AgentPlan, error) {
	if s.states == nil {
		return nil, errors.New("plan state store is not configured")
	}
	plan, err := s.store.Get(ctx, planID)
	if err != nil {
		return nil, err
	}

	known := make(map[int64]struct{}, len(plan.Items))
	for _, item := range plan.Items {
		known[item.ID] = struct{}{}
	}

	now := time.Now()
	states := make([]*AgentPlanItemState, 0, len(updates))
	for _, update := range updates {
		if _, ok := known[update.ItemID]; !ok {
			return nil, fmt.Errorf("plan item %d does not exist in plan %d", update.ItemID, planID)
		}
		if !update.Status.Valid() {
			return nil, fmt.Errorf("invalid status %q", update.Status)
		}
		states = append(states, &AgentPlanItemState{
			PlanID:    planID,
			ItemID:    update.ItemID,
			Status:    update.Status,
			SessionID: sessionID,
			Note:      strings.TrimSpace(update.Note),
			UpdatedAt: now,
		})
	}
	if len(states) > 0 {
		if err := s.states.Upsert(ctx, states); err != nil {
			return nil, err
		}
	}
	s.mergeItemStatus(ctx, plan)
	s.notifyProgress(ctx, plan)
	return plan, nil
}

// notifyProgress 把「节点状态已变化」推给该用户在线的前端。
//
// 推送失败不影响业务：它只是让前端的计划面板刷新，与 SessionResourceService
// 对 no_client_for_user 的处理保持一致。前端收到后按 plan_id 重新拉详情。
func (s *planService) notifyProgress(ctx context.Context, plan *AgentPlan) {
	if s.hub == nil || plan == nil {
		return
	}
	userID := agenttool.UserIDFrom(ctx)
	if userID <= 0 {
		return
	}
	planKey := strconv.FormatInt(plan.ID, 10)
	_ = s.hub.PushMessage(userID, map[string]any{
		"action": "component.invoke",
		"payload": map[string]any{
			"category": PlanRealtimeCategory,
			"id":       planKey,
			"method":   PlanRealtimeMethod,
			"args": map[string]any{
				"plan_id":  planKey,
				"items":    plan.Items,
				"progress": planProgressSummary(plan.Items),
			},
		},
	})
}

// planProgressSummary 统计各状态节点数，便于前端标题栏直接展示「3/5 完成」。
func planProgressSummary(items []PlanItem) map[string]int {
	summary := map[string]int{string(PlanItemPending): 0, string(PlanItemInProgress): 0, string(PlanItemCompleted): 0, string(PlanItemSkipped): 0}
	for _, item := range items {
		status := item.Status
		if !status.Valid() {
			status = PlanItemPending
		}
		summary[string(status)]++
	}
	return summary
}

// mergeItemStatus 把运行态合并进计划节点的 Status（无记录的节点保持 pending）。
//
// 运行态查询失败不影响结构读取：进度是附加信息，缺进度时前端按 pending 展示。
func (s *planService) mergeItemStatus(ctx context.Context, plan *AgentPlan) {
	if plan == nil {
		return
	}
	statuses := map[int64]PlanItemStatus{}
	if s.states != nil {
		if states, err := s.states.ListByPlan(ctx, plan.ID); err == nil {
			for _, state := range states {
				if state != nil {
					statuses[state.ItemID] = state.Status
				}
			}
		}
	}
	for i := range plan.Items {
		status, ok := statuses[plan.Items[i].ID]
		if !ok || !status.Valid() {
			status = PlanItemPending
		}
		plan.Items[i].Status = status
	}
}

// NormalizePlanItems 清洗计划节点树：补齐 ID、去空白、丢弃空标题、校验来源类型、
// 断开悬空 / 自引用的父节点（提升为根）。同级顺序由调用方按 SortOrder 维护。
func NormalizePlanItems(items []PlanItem) []PlanItem {
	if len(items) == 0 {
		return nil
	}

	existing := make(map[int64]struct{}, len(items))
	for i := range items {
		if items[i].ID <= 0 {
			items[i].ID = utils.GenerateID()
		}
		existing[items[i].ID] = struct{}{}
	}

	out := make([]PlanItem, 0, len(items))
	for _, item := range items {
		item.Title = strings.TrimSpace(item.Title)
		if item.Title == "" {
			continue
		}
		item.Summary = strings.TrimSpace(item.Summary)
		if !item.Source.Valid() {
			item.Source = PlanSourceCustom
		}
		if item.Source == PlanSourceCustom {
			item.SourceID = 0
		}
		if _, ok := existing[item.ParentID]; !ok || item.ParentID == item.ID {
			item.ParentID = 0
		}
		if item.SortOrder < 0 {
			item.SortOrder = 0
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
