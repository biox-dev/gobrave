package repository

import (
	"context"
	"errors"
	"time"

	agent "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"gorm.io/gorm"
)

// NewGormPlanStore 创建基于数据库的计划 Store。
func NewGormPlanStore(db *gorm.DB) agentinterfaces.PlanStore {
	return &gormPlanStore{db: db}
}

// gormPlanStore 同时管理计划头（agent_plans）与节点（agent_plan_items）两张表。
//
// 计划头只存标题 / 目标；节点一行一个，因此节点 ID 可以作为
// 「执行结果 → 计划节点」关联的外键目标。对外仍然是 *AgentPlan（带 Items 投影）。
type gormPlanStore struct {
	db *gorm.DB
}

// Save 写入计划头并把节点树同步到 agent_plan_items。
//
// 同步语义是「以本次 Items 为准」：已有节点按 id 更新**结构列**（不碰 status），
// 新增节点插入，未出现在本次入参的节点删除。进度列因此不会被用户编辑计划冲掉。
func (r *gormPlanStore) Save(ctx context.Context, plan *agent.AgentPlan) error {
	if plan == nil {
		return errors.New("plan is nil")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Omit("Items").Save(plan).Error; err != nil {
			return err
		}
		return syncPlanItems(tx, plan)
	})
}

// syncPlanItems 把 plan.Items 同步为 agent_plan_items 的当前状态。
func syncPlanItems(tx *gorm.DB, plan *agent.AgentPlan) error {
	var existing []int64
	if err := tx.Model(&agent.PlanItem{}).
		Where("plan_id = ?", plan.ID).
		Pluck("id", &existing).Error; err != nil {
		return err
	}

	now := time.Now()
	keep := make(map[int64]struct{}, len(plan.Items))
	for i := range plan.Items {
		item := &plan.Items[i]
		item.PlanID = plan.ID
		keep[item.ID] = struct{}{}
		item.UpdatedAt = now
		if item.Status == "" {
			item.Status = agent.PlanItemPending
		}

		// 结构列显式列出：status / session_id / note 不在其中，
		// 因此用户编辑计划不会覆盖 agent 上报的执行进度。
		res := tx.Model(&agent.PlanItem{}).
			Where("id = ?", item.ID).
			Updates(map[string]any{
				"plan_id":    item.PlanID,
				"parent_id":  item.ParentID,
				"title":      item.Title,
				"summary":    item.Summary,
				"source":     item.Source,
				"source_id":  item.SourceID,
				"sort_order": item.SortOrder,
				"updated_at": item.UpdatedAt,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected > 0 {
			continue
		}
		if item.CreatedAt.IsZero() {
			item.CreatedAt = now
		}
		if err := tx.Create(item).Error; err != nil {
			return err
		}
	}

	// 删除本次未包含的节点（计划整体覆盖语义）。
	stale := make([]int64, 0, len(existing))
	for _, id := range existing {
		if _, ok := keep[id]; !ok {
			stale = append(stale, id)
		}
	}
	if len(stale) == 0 {
		return nil
	}
	return tx.Where("id IN ?", stale).Delete(&agent.PlanItem{}).Error
}

func (r *gormPlanStore) Get(ctx context.Context, id int64) (*agent.AgentPlan, error) {
	var plan agent.AgentPlan
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(&plan).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agent.ErrPlanNotFound
		}
		return nil, err
	}
	if err := r.loadItems(ctx, &plan); err != nil {
		return nil, err
	}
	return &plan, nil
}

// ListByProject 返回项目下的全部计划（按更新时间倒序），并填充节点树。
func (r *gormPlanStore) ListByProject(ctx context.Context, projectID int64) ([]*agent.AgentPlan, error) {
	var plans []*agent.AgentPlan
	err := r.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("updated_at DESC, id DESC").
		Find(&plans).Error
	if err != nil || len(plans) == 0 {
		return plans, err
	}

	ids := make([]int64, 0, len(plans))
	for _, plan := range plans {
		ids = append(ids, plan.ID)
	}
	var items []*agent.PlanItem
	if err := r.db.WithContext(ctx).
		Where("plan_id IN ?", ids).
		Order("sort_order ASC, id ASC").
		Find(&items).Error; err != nil {
		return nil, err
	}
	byPlan := make(map[int64][]agent.PlanItem, len(plans))
	for _, item := range items {
		if item != nil {
			byPlan[item.PlanID] = append(byPlan[item.PlanID], *item)
		}
	}
	for _, plan := range plans {
		plan.Items = byPlan[plan.ID]
	}
	return plans, nil
}

func (r *gormPlanStore) Delete(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("plan_id = ?", id).Delete(&agent.PlanItem{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&agent.AgentPlan{}).Error
	})
}

// UpdateItemStatus 只更新状态列（结构列不受影响），在一个事务内逐条更新。
func (r *gormPlanStore) UpdateItemStatus(ctx context.Context, planID, sessionID int64, updates []agent.PlanItemStatusUpdate) (int64, error) {
	if len(updates) == 0 {
		return 0, nil
	}
	var affected int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		for _, update := range updates {
			res := tx.Model(&agent.PlanItem{}).
				Where("id = ? AND plan_id = ?", update.ItemID, planID).
				Updates(map[string]any{
					"status":     update.Status,
					"session_id": sessionID,
					"note":       update.Note,
					"updated_at": now,
				})
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected == 0 {
				return agent.ErrPlanItemNotFound
			}
			affected += res.RowsAffected
		}
		return nil
	})
	return affected, err
}

// loadItems 读取单个计划的节点树（按 sort_order 排序）。
func (r *gormPlanStore) loadItems(ctx context.Context, plan *agent.AgentPlan) error {
	var items []*agent.PlanItem
	if err := r.db.WithContext(ctx).
		Where("plan_id = ?", plan.ID).
		Order("sort_order ASC, id ASC").
		Find(&items).Error; err != nil {
		return err
	}
	plan.Items = make([]agent.PlanItem, 0, len(items))
	for _, item := range items {
		if item != nil {
			plan.Items = append(plan.Items, *item)
		}
	}
	return nil
}
