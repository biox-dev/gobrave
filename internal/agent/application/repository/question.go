package repository

import (
	"context"
	"errors"

	agent "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"gorm.io/gorm"
)

// NewGormQuestionRepository 创建基于数据库的提问请求 Repository。
func NewGormQuestionRepository(db *gorm.DB) agentinterfaces.QuestionRepository {
	return &gormQuestionRepository{db: db}
}

type gormQuestionRepository struct {
	db *gorm.DB
}

func (r *gormQuestionRepository) Create(ctx context.Context, q *agent.QuestionRequest) error {
	if q == nil {
		return nil
	}
	return r.db.WithContext(ctx).Create(q).Error
}

func (r *gormQuestionRepository) Get(ctx context.Context, id int64) (*agent.QuestionRequest, error) {
	var q agent.QuestionRequest
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&q).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agent.ErrQuestionNotFound
		}
		return nil, err
	}
	return &q, nil
}

func (r *gormQuestionRepository) Update(ctx context.Context, q *agent.QuestionRequest) error {
	if q == nil {
		return nil
	}
	return r.db.WithContext(ctx).Save(q).Error
}

func (r *gormQuestionRepository) ListPendingByTask(ctx context.Context, taskID int64) ([]*agent.QuestionRequest, error) {
	var questions []*agent.QuestionRequest
	if err := r.db.WithContext(ctx).
		Where("task_id = ? AND status = ?", taskID, agent.QuestionPending).
		Order("created_at ASC").
		Find(&questions).Error; err != nil {
		return nil, err
	}
	return questions, nil
}

// ListByTask 返回任务的全部提问请求（最新在前），供恢复执行时按 tool_call_id 复用已作答结果。
func (r *gormQuestionRepository) ListByTask(ctx context.Context, taskID int64) ([]*agent.QuestionRequest, error) {
	var questions []*agent.QuestionRequest
	if err := r.db.WithContext(ctx).
		Where("task_id = ?", taskID).
		Order("created_at DESC").
		Find(&questions).Error; err != nil {
		return nil, err
	}
	return questions, nil
}

func (r *gormQuestionRepository) Page(ctx context.Context, offset, limit int, taskID int64, statuses ...agent.QuestionStatus) ([]*agent.QuestionRequest, int64, error) {
	q := r.db.WithContext(ctx).Model(&agent.QuestionRequest{})
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

	var questions []*agent.QuestionRequest
	if err := q.Order("created_at ASC").Offset(offset).Limit(limit).Find(&questions).Error; err != nil {
		return nil, 0, err
	}
	return questions, total, nil
}
