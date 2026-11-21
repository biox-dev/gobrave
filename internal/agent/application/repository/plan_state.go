package repository

import (
	"context"

	agent "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NewGormPlanStateStore 创建基于数据库的计划运行态 Store。
func NewGormPlanStateStore(db *gorm.DB) agentinterfaces.PlanStateStore {
	return &gormPlanStateStore{db: db}
}

type gormPlanStateStore struct {
	db *gorm.DB
}

// Upsert 以 (plan_id, item_id) 为键写入 / 覆盖运行态。
func (r *gormPlanStateStore) Upsert(ctx context.Context, states []*agent.AgentPlanItemState) error {
	if len(states) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "plan_id"}, {Name: "item_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"status", "session_id", "note", "updated_at"}),
		}).
		Create(states).Error
}

func (r *gormPlanStateStore) ListByPlan(ctx context.Context, planID int64) ([]*agent.AgentPlanItemState, error) {
	var states []*agent.AgentPlanItemState
	err := r.db.WithContext(ctx).Where("plan_id = ?", planID).Find(&states).Error
	return states, err
}

func (r *gormPlanStateStore) DeleteByPlan(ctx context.Context, planID int64) error {
	return r.db.WithContext(ctx).Where("plan_id = ?", planID).Delete(&agent.AgentPlanItemState{}).Error
}
