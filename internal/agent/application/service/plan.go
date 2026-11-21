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
// 前端按计划 ID 注册监听，收到后重新拉取该计划详情。
const PlanRealtimeCategory = "agent-plan-progress"

// PlanRealtimeMethod 是「节点状态变化」时推送的方法名。
const PlanRealtimeMethod = "planProgressChanged"

// planService 提供计划（AgentPlan）的增删改查实现。
//
// 计划是项目级的 agent 产物：写入前由调用方（工具）用执行事实里的 ProjectID /
// SessionID 赋值，因此这里不再做项目归属校验（越权由调用方的事实保证）。
//
// 计划头（agent_plans）与节点（agent_plan_items）分表存放，节点一行一个：
// 节点 ID 因此可以作为「执行结果 → 计划节点」关联的引用目标。
type planService struct {
	store PlanStore
	// hub 为 nil 时不推送实时事件。
	hub *realtime.Hub
}

// NewPlanService 创建计划服务。hub 可为 nil（不推送事件）。
func NewPlanService(store PlanStore, hub *realtime.Hub) PlanService {
	return &planService{store: store, hub: hub}
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
	// Store 是节点树的唯一写入口：它负责回填 PlanID / CreatedAt 并保持
	// status 不被结构覆盖，因此读回一次，保证返回值与库内一致。
	return s.store.Get(ctx, plan.ID)
}

func (s *planService) Get(ctx context.Context, id int64) (*AgentPlan, error) {
	if id <= 0 {
		return nil, ErrPlanNotFound
	}
	return s.store.Get(ctx, id)
}

func (s *planService) ListByProject(ctx context.Context, projectID int64) ([]*AgentPlan, error) {
	if projectID <= 0 {
		return nil, nil
	}
	return s.store.ListByProject(ctx, projectID)
}

func (s *planService) Delete(ctx context.Context, id int64) error {
	return s.store.Delete(ctx, id)
}

// UpdateItemStatus 上报节点状态并返回最新计划。
//
// 只更新节点的 status / session_id / note 列，不动计划结构（标题 / 来源 / 排序 /
// 父子），因此与用户编辑计划互不覆盖。节点 ID 不属于该计划时返回 ErrPlanItemNotFound。
func (s *planService) UpdateItemStatus(ctx context.Context, planID int64, sessionID int64, updates []PlanItemStatusUpdate) (*AgentPlan, error) {
	normalized := make([]PlanItemStatusUpdate, 0, len(updates))
	for _, update := range updates {
		if !update.Status.Valid() {
			return nil, fmt.Errorf("invalid status %q", update.Status)
		}
		update.Note = strings.TrimSpace(update.Note)
		normalized = append(normalized, update)
	}
	if len(normalized) > 0 {
		if _, err := s.store.UpdateItemStatus(ctx, planID, sessionID, normalized); err != nil {
			return nil, err
		}
	}
	plan, err := s.store.Get(ctx, planID)
	if err != nil {
		return nil, err
	}
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
	summary := map[string]int{
		string(PlanItemPending):    0,
		string(PlanItemInProgress): 0,
		string(PlanItemCompleted):  0,
		string(PlanItemSkipped):    0,
	}
	for _, item := range items {
		status := item.Status
		if !status.Valid() {
			status = PlanItemPending
		}
		summary[string(status)]++
	}
	return summary
}

// NormalizePlanItems 清洗计划节点树：补齐 ID、去空白、丢弃空标题、校验来源类型、
// 断开悬空 / 自引用的父节点（提升为根）、补默认状态。同级顺序由调用方维护。
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
		if !item.Status.Valid() {
			item.Status = PlanItemPending
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
