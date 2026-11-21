package repository

import (
	"context"
	"errors"

	agent "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"gorm.io/gorm"
)

// NewGormReportPlanStore 创建基于数据库的报告大纲 Store。
func NewGormReportPlanStore(db *gorm.DB) agentinterfaces.ReportPlanStore {
	return &gormReportPlanStore{db: db}
}

type gormReportPlanStore struct {
	db *gorm.DB
}

func (r *gormReportPlanStore) Save(ctx context.Context, plan *agent.ReportPlan) error {
	if plan == nil {
		return errors.New("report plan is nil")
	}
	return r.db.WithContext(ctx).Save(plan).Error
}

func (r *gormReportPlanStore) Get(ctx context.Context, id int64) (*agent.ReportPlan, error) {
	var plan agent.ReportPlan
	if err := r.db.WithContext(ctx).Where("id = ?", id).Take(&plan).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agent.ErrReportPlanNotFound
		}
		return nil, err
	}
	return &plan, nil
}

// ListByProject 返回项目下的全部大纲（按更新时间倒序）。
func (r *gormReportPlanStore) ListByProject(ctx context.Context, projectID int64) ([]*agent.ReportPlan, error) {
	var plans []*agent.ReportPlan
	err := r.db.WithContext(ctx).
		Where("project_id = ?", projectID).
		Order("updated_at DESC, id DESC").
		Find(&plans).Error
	return plans, err
}

func (r *gormReportPlanStore) Delete(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&agent.ReportPlan{}).Error
}
