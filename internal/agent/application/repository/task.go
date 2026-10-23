package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/biox-dev/gobrave/internal/agent"
	"gorm.io/gorm"
)

// NewGormTaskRepository 创建基于数据库的任务 Repository。
func NewGormTaskRepository(db *gorm.DB) agent.TaskRepository {
	return &gormTaskRepository{db: db}
}

type gormTaskRepository struct {
	db *gorm.DB
}

func (r *gormTaskRepository) Create(ctx context.Context, task *agent.Task) error {
	if task == nil {
		return nil
	}
	return r.db.WithContext(ctx).Create(task).Error
}

func (r *gormTaskRepository) Get(ctx context.Context, id int64) (*agent.Task, error) {
	var task agent.Task
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agent.ErrTaskNotFound
		}
		return nil, err
	}
	return &task, nil
}

func (r *gormTaskRepository) Update(ctx context.Context, task *agent.Task) error {
	if task == nil {
		return nil
	}
	// resume_state 由 UpdateResumeState 单独维护：这里必须 Omit，否则内存中
	// 较早加载的 Task 快照（ResumeState 为 nil）会在状态迁移时覆盖掉最新检查点。
	return r.db.WithContext(ctx).Omit("resume_state").Save(task).Error
}

// UpdateResumeState 只写 resume_state 列，避免 Save 全量覆盖与状态迁移并发冲突。
//
// resume_state 使用 serializer:json，但这里在 Go 侧显式序列化后用 Update 写入列，
// 既避免 GORM 对 map 更新的序列化差异，也让「只更新一列」的语义更直接。
func (r *gormTaskRepository) UpdateResumeState(ctx context.Context, id int64, state *agent.TaskResumeState) error {
	if id == 0 {
		return nil
	}
	if state == nil {
		return r.db.WithContext(ctx).Model(&agent.Task{}).
			Where("id = ?", id).
			Update("resume_state", nil).Error
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Model(&agent.Task{}).
		Where("id = ?", id).
		Update("resume_state", string(data)).Error
}

func (r *gormTaskRepository) ListByStatus(ctx context.Context, statuses ...agent.TaskStatus) ([]*agent.Task, error) {
	q := r.db.WithContext(ctx)
	if len(statuses) > 0 {
		q = q.Where("status IN ?", statuses)
	}
	var tasks []*agent.Task
	if err := q.Order("created_at ASC").Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

func (r *gormTaskRepository) Page(ctx context.Context, offset, limit int, statuses ...agent.TaskStatus) ([]*agent.Task, int64, error) {
	q := r.db.WithContext(ctx).Model(&agent.Task{})
	if len(statuses) > 0 {
		q = q.Where("status IN ?", statuses)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var tasks []*agent.Task
	if err := q.Order("created_at ASC").Offset(offset).Limit(limit).Find(&tasks).Error; err != nil {
		return nil, 0, err
	}
	return tasks, total, nil
}
