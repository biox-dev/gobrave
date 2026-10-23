package repository

import (
	"context"
	"errors"
	"strings"

	agent "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"gorm.io/gorm"
)

// NewGormMemoryRepository 创建基于数据库的记忆 Repository。
func NewGormMemoryRepository(db *gorm.DB) agentinterfaces.MemoryRepository {
	return &gormMemoryRepository{db: db}
}

type gormMemoryRepository struct {
	db *gorm.DB
}

func (r *gormMemoryRepository) Create(ctx context.Context, m *agent.Memory) error {
	if m == nil {
		return nil
	}
	return r.db.WithContext(ctx).Create(m).Error
}

func (r *gormMemoryRepository) Get(ctx context.Context, id int64) (*agent.Memory, error) {
	var m agent.Memory
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agent.ErrMemoryNotFound
		}
		return nil, err
	}
	return &m, nil
}

func (r *gormMemoryRepository) Update(ctx context.Context, m *agent.Memory) error {
	if m == nil {
		return nil
	}
	return r.db.WithContext(ctx).Save(m).Error
}

func (r *gormMemoryRepository) Delete(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&agent.Memory{}).Error
}

func (r *gormMemoryRepository) ListByUser(ctx context.Context, userID int64, offset, limit int, kinds ...agent.MemoryKind) ([]*agent.Memory, int64, error) {
	q := r.db.WithContext(ctx).Model(&agent.Memory{}).Where("user_id = ?", userID)
	if len(kinds) > 0 {
		q = q.Where("kind IN ?", kinds)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var mems []*agent.Memory
	if err := q.Order("updated_at DESC").Offset(offset).Limit(limit).Find(&mems).Error; err != nil {
		return nil, 0, err
	}
	return mems, total, nil
}

func (r *gormMemoryRepository) Search(ctx context.Context, userID int64, query string, limit int) ([]*agent.Memory, error) {
	q := r.db.WithContext(ctx).Model(&agent.Memory{}).Where("user_id = ?", userID)
	if strings.TrimSpace(query) != "" {
		q = q.Where("content LIKE ?", "%"+query+"%")
	}

	var mems []*agent.Memory
	if err := q.Order("importance DESC").Order("updated_at DESC").Limit(limit).Find(&mems).Error; err != nil {
		return nil, err
	}
	return mems, nil
}

// ListByUserContext 优先返回当前业务上下文（项目 / 会话）下的记忆，
// 其余记忆按重要度与更新时间作为补充。
//
// project_id / session_id 是记忆表的独立列（非空时标识归属上下文）；两者都不存在时
// 退化为 ListByUser 的语义。排序规则：
//
//	上下文匹配（project_id 或 session_id 命中）→ importance DESC → updated_at DESC
func (r *gormMemoryRepository) ListByUserContext(ctx context.Context, userID int64, projectID int64, sessionID int64, offset, limit int, kinds ...agent.MemoryKind) ([]*agent.Memory, int64, error) {
	if projectID <= 0 && sessionID <= 0 {
		return r.ListByUser(ctx, userID, offset, limit, kinds...)
	}

	q := r.db.WithContext(ctx).Model(&agent.Memory{}).Where("user_id = ?", userID)
	if len(kinds) > 0 {
		q = q.Where("kind IN ?", kinds)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	matches := make([]string, 0, 2)
	args := make([]any, 0, 2)
	if projectID > 0 {
		matches = append(matches, "project_id = ?")
		args = append(args, projectID)
	}
	if sessionID > 0 {
		matches = append(matches, "session_id = ?")
		args = append(args, sessionID)
	}

	var mems []*agent.Memory
	if err := q.Order(gorm.Expr("("+strings.Join(matches, " OR ")+") DESC", args...)).
		Order("importance DESC").
		Order("updated_at DESC").
		Offset(offset).
		Limit(limit).
		Find(&mems).Error; err != nil {
		return nil, 0, err
	}
	return mems, total, nil
}
