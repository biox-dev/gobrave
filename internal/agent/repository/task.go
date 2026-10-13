package repository

import (
	"context"
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
	return r.db.WithContext(ctx).Save(task).Error
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
