package manager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/biox-dev/gobrave/internal/types"
	"gorm.io/gorm"
)

// TerminalSessionStore 是终端会话持久层的最小契约。
//
// 只做「让进程重启后能认领原容器」这一件事：注册表不依赖完整 repository 层，
// 单测可以用内存 fake 或直接传 nil。
type TerminalSessionStore interface {
	// Get 按会话 key 读取记录；不存在返回 (nil, nil)。
	Get(ctx context.Context, key TerminalSessionKey) (*types.TerminalSession, error)
	// Upsert 按 session_key 新增或更新记录。
	Upsert(ctx context.Context, rec *types.TerminalSession) error
	// Touch 刷新 last_active_at。
	Touch(ctx context.Context, key TerminalSessionKey, at time.Time) error
	// Delete 按会话 key 删除记录。
	Delete(ctx context.Context, key TerminalSessionKey) error
	// List 返回全部记录（用于启动/定期对账）。
	List(ctx context.Context) ([]*types.TerminalSession, error)
}

// NewGormTerminalSessionStore 构造基于 GORM 的会话记录存储。
func NewGormTerminalSessionStore(db *gorm.DB) *GormTerminalSessionStore {
	return &GormTerminalSessionStore{db: db}
}

// GormTerminalSessionStore 是 TerminalSessionStore 的默认实现。
type GormTerminalSessionStore struct {
	db *gorm.DB
}

func (s *GormTerminalSessionStore) Get(ctx context.Context, key TerminalSessionKey) (*types.TerminalSession, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	var rec types.TerminalSession
	err := s.db.WithContext(ctx).Where("session_key = ?", key.SessionID()).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load terminal session %s: %w", key.SessionID(), err)
	}
	return &rec, nil
}

func (s *GormTerminalSessionStore) Upsert(ctx context.Context, rec *types.TerminalSession) error {
	if s == nil || s.db == nil || rec == nil {
		return nil
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing types.TerminalSession
		err := tx.Where("session_key = ?", rec.SessionKey).First(&existing).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(rec).Error
		}
		if err != nil {
			return err
		}
		rec.ID = existing.ID
		updates := map[string]any{
			"user_id":        rec.UserID,
			"project_id":     rec.ProjectID,
			"template_id":    rec.TemplateID,
			"runtime_name":   rec.RuntimeName,
			"runtime_id":     rec.RuntimeID,
			"container_name": rec.ContainerName,
			"status":         rec.Status,
			"last_active_at": rec.LastActiveAt,
			"updated_at":     time.Now(),
		}
		return tx.Model(&types.TerminalSession{}).Where("id = ?", existing.ID).Updates(updates).Error
	})
}

func (s *GormTerminalSessionStore) Touch(ctx context.Context, key TerminalSessionKey, at time.Time) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.WithContext(ctx).
		Model(&types.TerminalSession{}).
		Where("session_key = ?", key.SessionID()).
		Updates(map[string]any{"last_active_at": at, "updated_at": at}).Error
}

func (s *GormTerminalSessionStore) Delete(ctx context.Context, key TerminalSessionKey) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.WithContext(ctx).
		Where("session_key = ?", key.SessionID()).
		Delete(&types.TerminalSession{}).Error
}

func (s *GormTerminalSessionStore) List(ctx context.Context) ([]*types.TerminalSession, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	var rows []*types.TerminalSession
	if err := s.db.WithContext(ctx).Order("last_active_at ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list terminal sessions: %w", err)
	}
	return rows, nil
}
