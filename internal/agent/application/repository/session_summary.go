package repository

import (
	"context"
	"errors"
	"time"

	agent "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"gorm.io/gorm"
)

// NewGormSessionSummaryStore 创建基于数据库的会话摘要 Store。
func NewGormSessionSummaryStore(db *gorm.DB) agentinterfaces.SessionSummaryStore {
	return &gormSessionSummaryStore{db: db}
}

type gormSessionSummaryStore struct {
	db *gorm.DB
}

// Get 返回会话摘要；不存在时返回 (nil, nil)。
func (r *gormSessionSummaryStore) Get(ctx context.Context, sessionID int64) (*agent.SessionSummary, error) {
	var summary agent.SessionSummary
	err := r.db.WithContext(ctx).Where("session_id = ?", sessionID).First(&summary).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &summary, nil
}

// Upsert 写入或推进摘要水位。
//
// 水位只增不减：并发（同步压缩与轮末异步预压缩可能重叠）时，较旧的摘要不会
// 覆盖较新的结果。不存在时创建，已存在时用条件更新保证原子性。
func (r *gormSessionSummaryStore) Upsert(ctx context.Context, summary *agent.SessionSummary) error {
	if summary == nil || summary.SessionID <= 0 {
		return nil
	}
	now := time.Now()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing agent.SessionSummary
		err := tx.Where("session_id = ?", summary.SessionID).First(&existing).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			summary.CreatedAt = now
			summary.UpdatedAt = now
			return tx.Create(summary).Error
		case err != nil:
			return err
		case summary.UpToSeq <= existing.UpToSeq:
			return nil
		default:
			return tx.Model(&agent.SessionSummary{}).
				Where("session_id = ? AND up_to_seq < ?", summary.SessionID, summary.UpToSeq).
				Updates(map[string]any{
					"up_to_seq":  summary.UpToSeq,
					"content":    summary.Content,
					"model":      summary.Model,
					"updated_at": now,
				}).Error
		}
	})
}
