package service

import (
	"context"
	"errors"
	"strings"
	"time"

	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/utils"
)

// PlanService 提供计划（AgentPlan）的增删改查实现。
//
// 计划是项目级的 agent 产物：写入前由调用方（工具）用执行事实里的 ProjectID /
// SessionID 赋值，因此这里不再做项目归属校验（越权由调用方的事实保证）。
type planService struct {
	store PlanStore
}

// NewPlanService 创建计划服务。
func NewPlanService(store PlanStore) PlanService {
	return &planService{store: store}
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
	return plan, nil
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
