package repository

import (
	"context"
	"errors"

	"github.com/biox-dev/gobrave/internal/agent"
	"gorm.io/gorm"
)

// NewGormPermissionRepository 创建基于数据库的权限请求 Repository。
func NewGormPermissionRepository(db *gorm.DB) agent.PermissionRepository {
	return &gormPermissionRepository{db: db}
}

type gormPermissionRepository struct {
	db *gorm.DB
}

func (r *gormPermissionRepository) Create(ctx context.Context, p *agent.PermissionRequest) error {
	if p == nil {
		return nil
	}
	return r.db.WithContext(ctx).Create(p).Error
}

func (r *gormPermissionRepository) Get(ctx context.Context, id int64) (*agent.PermissionRequest, error) {
	var p agent.PermissionRequest
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agent.ErrPermissionNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (r *gormPermissionRepository) Update(ctx context.Context, p *agent.PermissionRequest) error {
	if p == nil {
		return nil
	}
	return r.db.WithContext(ctx).Save(p).Error
}

func (r *gormPermissionRepository) ListPendingByTask(ctx context.Context, taskID int64) ([]*agent.PermissionRequest, error) {
	var perms []*agent.PermissionRequest
	if err := r.db.WithContext(ctx).
		Where("task_id = ? AND status = ?", taskID, agent.PermissionPending).
		Order("created_at ASC").
		Find(&perms).Error; err != nil {
		return nil, err
	}
	return perms, nil
}

// ListByTask 返回任务的全部权限请求（最新在前），供恢复执行时按 tool_call_id 配对。
func (r *gormPermissionRepository) ListByTask(ctx context.Context, taskID int64) ([]*agent.PermissionRequest, error) {
	var perms []*agent.PermissionRequest
	if err := r.db.WithContext(ctx).
		Where("task_id = ?", taskID).
		Order("created_at DESC").
		Find(&perms).Error; err != nil {
		return nil, err
	}
	return perms, nil
}

func (r *gormPermissionRepository) Page(ctx context.Context, offset, limit int, taskID int64, statuses ...agent.PermissionStatus) ([]*agent.PermissionRequest, int64, error) {
	q := r.db.WithContext(ctx).Model(&agent.PermissionRequest{})
	if taskID != 0 {
		q = q.Where("task_id = ?", taskID)
	}
	if len(statuses) > 0 {
		q = q.Where("status IN ?", statuses)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var perms []*agent.PermissionRequest
	if err := q.Order("created_at ASC").Offset(offset).Limit(limit).Find(&perms).Error; err != nil {
		return nil, 0, err
	}
	return perms, total, nil
}
