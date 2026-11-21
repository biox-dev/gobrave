package repository

import (
	"context"
	"errors"

	agent "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"gorm.io/gorm"
)

// NewGormPlanStore 创建基于数据库的计划 Store。
func NewGormPlanStore(db *gorm.DB) agentinterfaces.PlanStore {
	return &gormPlanStore{db: db}
}

type gormPlanStore struct {
	db *gorm.DB
}

func (r *gormPlanStore) Save(ctx context.Context, plan *agent.AgentPlan) error {
	if plan == nil {
		return errors.New("plan is nil")
	}
	return r.db.WithContext(ctx).Save(plan).Error
}

func (r *gormPlanStore) Get(ctx context.Context, id int64) (*agent.AgentPlan, error) {
	var plan agent.AgentPlan
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(&plan).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agent.ErrPlanNotFound
		}
		return nil, err
	}
	return &plan, nil
}

// ListByProject 返回项目下的全部计划（按更新时间倒序）。
func (r *gormPlanStore) ListByProject(ctx context.Context, projectID int64) ([]*agent.AgentPlan, error) {
	var plans []*agent.AgentPlan
	err := r.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("updated_at DESC, id DESC").
		Find(&plans).Error
	return plans, err
}

func (r *gormPlanStore) Delete(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&agent.AgentPlan{}).Error
}
