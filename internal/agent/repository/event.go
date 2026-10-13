package repository

import (
	"context"
	"sort"

	"github.com/biox-dev/gobrave/internal/agent"
	"gorm.io/gorm"
)

// NewGormEventRepository 创建基于数据库的事件流 Repository。
func NewGormEventRepository(db *gorm.DB) agent.EventRepository {
	return &gormEventRepository{db: db}
}

type gormEventRepository struct {
	db *gorm.DB
}

func (r *gormEventRepository) Append(ctx context.Context, event *agent.AgentEvent) error {
	if event == nil {
		return nil
	}
	// 在事务内分配 per-task 单调递增的 sequence。单个任务的执行 goroutine 串行产出
	// 事件，因此同一 task 的 Append 不会并发；事务保证并发任务之间互不干扰。
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int64
		if err := tx.Model(&agent.AgentEvent{}).
			Where("task_id = ?", event.TaskID).
			Select("COALESCE(MAX(sequence), 0)").
			Scan(&maxSeq).Error; err != nil {
			return err
		}
		event.Sequence = maxSeq + 1
		return tx.Create(event).Error
	})
}

func (r *gormEventRepository) ListByTask(ctx context.Context, taskID int64, after int64) ([]*agent.AgentEvent, error) {
	var events []*agent.AgentEvent
	if err := r.db.WithContext(ctx).
		Where("task_id = ? AND sequence > ?", taskID, after).
		Order("sequence ASC").
		Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

func (r *gormEventRepository) Page(ctx context.Context, taskID int64, offset, limit int) ([]*agent.AgentEvent, int64, error) {
	q := r.db.WithContext(ctx).Model(&agent.AgentEvent{})
	if taskID != 0 {
		q = q.Where("task_id = ?", taskID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var events []*agent.AgentEvent
	if err := q.Order("created_at ASC").Order("sequence ASC").Offset(offset).Limit(limit).Find(&events).Error; err != nil {
		return nil, 0, err
	}
	// 保证 created_at 相同时按 sequence 稳定排序。
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].CreatedAt.Equal(events[j].CreatedAt) {
			return events[i].Sequence < events[j].Sequence
		}
		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})
	return events, total, nil
}
