package repository

import (
	"context"
	"errors"

	agent "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NewGormSessionResourceStore 创建基于数据库的会话资源关联 Store。
func NewGormSessionResourceStore(db *gorm.DB) agentinterfaces.SessionResourceStore {
	return &gormSessionResourceStore{db: db}
}

type gormSessionResourceStore struct {
	db *gorm.DB
}

// Link 批量写入关联；已存在的 (session_id, resource_type, resource_id) 直接跳过。
//
// 返回值 inserted 表示本次真正写进数据库的行数：冲突跳过的关联不算新增
// （「别的会话再次修改同一脚本」只是重复关联，前端视图不会多出一行）。
func (r *gormSessionResourceStore) Link(ctx context.Context, sessionID int64, resourceType agent.SessionResourceType, resourceIDs []int64) (int64, error) {
	if sessionID <= 0 || len(resourceIDs) == 0 {
		return 0, nil
	}
	seen := make(map[int64]struct{}, len(resourceIDs))
	rows := make([]*agent.SessionResource, 0, len(resourceIDs))
	for _, id := range resourceIDs {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		rows = append(rows, &agent.SessionResource{
			SessionID:    sessionID,
			ResourceType: resourceType,
			ResourceID:   id,
		})
	}
	if len(rows) == 0 {
		return 0, nil
	}
	result := r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&rows)
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func (r *gormSessionResourceStore) List(ctx context.Context, sessionID int64, resourceType agent.SessionResourceType) ([]*agent.SessionResource, error) {
	var rows []*agent.SessionResource
	err := r.db.WithContext(ctx).
		Where("session_id = ? AND resource_type = ?", sessionID, resourceType).
		Order("created_at DESC, id DESC").
		Find(&rows).Error
	return rows, err
}

// ListBySession 返回某个会话关联的全部类型资源（按创建时间倒序）。
func (r *gormSessionResourceStore) ListBySession(ctx context.Context, sessionID int64) ([]*agent.SessionResource, error) {
	var rows []*agent.SessionResource
	err := r.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("created_at DESC, id DESC").
		Find(&rows).Error
	return rows, err
}

// Get 按关联行主键返回一条关联；不存在时返回 nil（由调用方决定 404 语义）。
func (r *gormSessionResourceStore) Get(ctx context.Context, id int64) (*agent.SessionResource, error) {
	if id <= 0 {
		return nil, nil
	}
	var row agent.SessionResource
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&row).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &row, nil
}

// Delete 按关联行主键删除一条关联；关联不存在时视为成功（幂等）。
func (r *gormSessionResourceStore) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return nil
	}
	return r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&agent.SessionResource{}).Error
}

// DeleteByResources 按资源批量删除关联；非法类型或空列表视为无需清理（幂等）。
//
// 资源被删除（脚本 / 工作流 / 分析节点 / 分析运行等）后必须清理这些关联行，
// 否则会话资源面板会残留指向已不存在资源的孤儿条目。
func (r *gormSessionResourceStore) DeleteByResources(ctx context.Context, resourceType agent.SessionResourceType, resourceIDs []int64) error {
	if !resourceType.Valid() {
		return nil
	}
	ids := make([]int64, 0, len(resourceIDs))
	for _, id := range resourceIDs {
		if id > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).
		Where("resource_type = ? AND resource_id IN ?", resourceType, ids).
		Delete(&agent.SessionResource{}).Error
}

func (r *gormSessionResourceStore) DeleteBySession(ctx context.Context, sessionID int64) error {
	return r.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Delete(&agent.SessionResource{}).Error
}
