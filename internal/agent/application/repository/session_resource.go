package repository

import (
	"context"

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
func (r *gormSessionResourceStore) Link(ctx context.Context, sessionID int64, resourceType agent.SessionResourceType, resourceIDs []int64) error {
	if sessionID <= 0 || len(resourceIDs) == 0 {
		return nil
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
		return nil
	}
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&rows).Error
}

func (r *gormSessionResourceStore) List(ctx context.Context, sessionID int64, resourceType agent.SessionResourceType) ([]*agent.SessionResource, error) {
	var rows []*agent.SessionResource
	err := r.db.WithContext(ctx).
		Where("session_id = ? AND resource_type = ?", sessionID, resourceType).
		Order("created_at DESC, id DESC").
		Find(&rows).Error
	return rows, err
}

func (r *gormSessionResourceStore) DeleteBySession(ctx context.Context, sessionID int64) error {
	return r.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Delete(&agent.SessionResource{}).Error
}
