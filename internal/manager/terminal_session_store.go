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
//
// 会话的对外标识是它的主键 ID（对应 types.TerminalSession.ID），
// 因此 Get / Delete / Touch / MarkPersistent 既可以按 ID，也可以按 (userID, projectID, templateID, agentSessionID) 定位。
type TerminalSessionStore interface {
	// Get 按会话 key 读取记录；不存在返回 (nil, nil)。
	Get(ctx context.Context, key TerminalSessionKey) (*types.TerminalSession, error)
	// GetByID 按终端会话主键读取记录；不存在返回 (nil, nil)。
	GetByID(ctx context.Context, id int64) (*types.TerminalSession, error)
	// FindAgentSession 按 (userID, projectID, templateID, agentSessionID) 查找 Agent 会话对应的终端会话；
	// agentSessionID <= 0（UI 手动创建）时永远返回 (nil, nil)。
	FindAgentSession(ctx context.Context, userID, projectID, templateID, agentSessionID int64) (*types.TerminalSession, error)
	// Upsert 按 session_key 新增或更新记录（已存在时回填 rec.ID）。
	Upsert(ctx context.Context, rec *types.TerminalSession) error
	// Touch 刷新 last_active_at。
	Touch(ctx context.Context, key TerminalSessionKey, at time.Time) error
	// MarkPersistent 把会话标记为持久（幂等，只会 false → true）。
	MarkPersistent(ctx context.Context, key TerminalSessionKey) error
	// Delete 按会话 key 删除记录。
	Delete(ctx context.Context, key TerminalSessionKey) error
	// DeleteByID 按终端会话主键删除记录。
	DeleteByID(ctx context.Context, id int64) error
	// List 返回全部记录（用于启动/定期对账）。
	List(ctx context.Context) ([]*types.TerminalSession, error)
	// ListByUser 返回某用户的全部存活记录（status=running），供「终端会话列表」使用。
	ListByUser(ctx context.Context, userID int64) ([]*types.TerminalSession, error)
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
	err := s.db.WithContext(ctx).Where("session_key = ?", key.SessionKey()).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load terminal session %s: %w", key.SessionKey(), err)
	}
	return &rec, nil
}

func (s *GormTerminalSessionStore) GetByID(ctx context.Context, id int64) (*types.TerminalSession, error) {
	if s == nil || s.db == nil || id <= 0 {
		return nil, nil
	}
	var rec types.TerminalSession
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load terminal session %d: %w", id, err)
	}
	return &rec, nil
}

func (s *GormTerminalSessionStore) FindAgentSession(ctx context.Context, userID, projectID, templateID, agentSessionID int64) (*types.TerminalSession, error) {
	if s == nil || s.db == nil || agentSessionID <= 0 {
		return nil, nil
	}
	var rec types.TerminalSession
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND project_id = ? AND template_id = ? AND agent_session_id = ?", userID, projectID, templateID, agentSessionID).
		Order("id DESC").
		First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find terminal session for agent session %d: %w", agentSessionID, err)
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
			"name":           rec.Name,
			"user_id":        rec.UserID,
			"project_id":     rec.ProjectID,
			"template_id":    rec.TemplateID,
			"runtime_name":   rec.RuntimeName,
			"runtime_id":     rec.RuntimeID,
			"container_name": rec.ContainerName,
			"status":         rec.Status,
			// 持久标记只增不减：已持久化的会话不能被后续 Upsert 改回一次性。
			"persistent":     existing.Persistent || rec.Persistent,
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
		Where("session_key = ?", key.SessionKey()).
		Updates(map[string]any{"last_active_at": at, "updated_at": at}).Error
}

// MarkPersistent 把会话标记为持久（只会 false → true）。
//
// 记录不存在时是空操作：真正的持久化由随后的 Upsert 写回。
func (s *GormTerminalSessionStore) MarkPersistent(ctx context.Context, key TerminalSessionKey) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.WithContext(ctx).
		Model(&types.TerminalSession{}).
		Where("session_key = ? AND persistent = ?", key.SessionKey(), false).
		Updates(map[string]any{"persistent": true, "updated_at": time.Now()}).Error
}

func (s *GormTerminalSessionStore) Delete(ctx context.Context, key TerminalSessionKey) error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.WithContext(ctx).
		Where("session_key = ?", key.SessionKey()).
		Delete(&types.TerminalSession{}).Error
}

func (s *GormTerminalSessionStore) DeleteByID(ctx context.Context, id int64) error {
	if s == nil || s.db == nil || id <= 0 {
		return nil
	}
	return s.db.WithContext(ctx).
		Where("id = ?", id).
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

func (s *GormTerminalSessionStore) ListByUser(ctx context.Context, userID int64) ([]*types.TerminalSession, error) {
	if s == nil || s.db == nil || userID <= 0 {
		return nil, nil
	}
	var rows []*types.TerminalSession
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND status = ?", userID, types.TerminalSessionStatusRunning).
		Order("template_id ASC, created_at ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list terminal sessions for user %d: %w", userID, err)
	}
	return rows, nil
}
