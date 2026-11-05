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
// 终端会话与「应用会话」是同一个实体（types.AppSession，表
// go_container_app_session），这里只暴露注册表需要的能力：定位会话、落库
// 持久标记、刷新活跃时间、删除会话。注册表不依赖完整 repository 层，
// 单测可以用内存 fake 或直接传 nil。
//
// 会话的对外标识是它的主键 ID（= AppSession.ID），因此 Get / Delete / Touch /
// MarkPersistent 既可以按 ID，也可以按 (userID, projectID, templateID, agentSessionID) 定位。
type TerminalSessionStore interface {
	// Get 按会话 key 读取记录；不存在返回 (nil, nil)。
	Get(ctx context.Context, key TerminalSessionKey) (*types.AppSession, error)
	// GetByID 按会话主键读取记录；不存在返回 (nil, nil)。
	GetByID(ctx context.Context, id int64) (*types.AppSession, error)
	// FindAgentSession 按 (userID, projectID, templateID, agentSessionID) 查找 Agent 会话对应的终端会话；
	// agentSessionID <= 0（UI 手动创建）时永远返回 (nil, nil)。
	FindAgentSession(ctx context.Context, userID, projectID, templateID, agentSessionID int64) (*types.AppSession, error)
	// Create 新增一条会话记录，并回填 rec.ID。
	//
	// name 只在创建时写入：之后 Update 不会再改它（DB 是 name 的唯一真相，
	// 改名必须走独立接口）。
	Create(ctx context.Context, rec *types.AppSession) error
	// Update 按主键更新已存在记录的运行时字段（status / persistent / last_access_at 等），
	// 不会修改 name。
	Update(ctx context.Context, rec *types.AppSession) error
	// Touch 刷新 last_access_at。
	Touch(ctx context.Context, key TerminalSessionKey, at time.Time) error
	// MarkPersistent 把会话标记为持久（幂等，只会 false → true）。
	MarkPersistent(ctx context.Context, key TerminalSessionKey) error
	// Delete 按会话 key 删除记录。
	Delete(ctx context.Context, key TerminalSessionKey) error
	// DeleteByID 按会话主键删除记录。
	DeleteByID(ctx context.Context, id int64) error
	// List 返回全部记录（用于启动/定期对账）。
	List(ctx context.Context) ([]*types.AppSession, error)
	// ListByUser 返回某用户的全部会话记录，供「终端会话列表」使用。
	ListByUser(ctx context.Context, userID int64) ([]*types.AppSession, error)
}

// NewGormTerminalSessionStore 构造基于 GORM 的会话记录存储。
func NewGormTerminalSessionStore(db *gorm.DB) *GormTerminalSessionStore {
	return &GormTerminalSessionStore{db: db}
}

// GormTerminalSessionStore 是 TerminalSessionStore 的默认实现（底层是 go_container_app_session）。
type GormTerminalSessionStore struct {
	db *gorm.DB
}

func (s *GormTerminalSessionStore) Get(ctx context.Context, key TerminalSessionKey) (*types.AppSession, error) {
	if s == nil || s.db == nil || key.ID <= 0 {
		return nil, nil
	}
	return s.GetByID(ctx, key.ID)
}

func (s *GormTerminalSessionStore) GetByID(ctx context.Context, id int64) (*types.AppSession, error) {
	if s == nil || s.db == nil || id <= 0 {
		return nil, nil
	}
	var rec types.AppSession
	err := s.db.WithContext(ctx).Where("id = ?", id).First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load app session %d: %w", id, err)
	}
	return &rec, nil
}

func (s *GormTerminalSessionStore) FindAgentSession(ctx context.Context, userID, projectID, templateID, agentSessionID int64) (*types.AppSession, error) {
	if s == nil || s.db == nil || agentSessionID <= 0 {
		return nil, nil
	}
	var rec types.AppSession
	err := s.db.WithContext(ctx).
		Where("user_id = ? AND project_id = ? AND container_template_id = ? AND agent_session_id = ?",
			userID, projectID, templateID, agentSessionID).
		Order("id DESC").
		First(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("find app session for agent session %d: %w", agentSessionID, err)
	}
	return &rec, nil
}

// Create 新增一条会话记录，并回填 rec.ID。
//
// 只在新会话创建时调用：name 在这里写入一次，之后 Update 不会修改它。
func (s *GormTerminalSessionStore) Create(ctx context.Context, rec *types.AppSession) error {
	if s == nil || s.db == nil || rec == nil {
		return nil
	}
	return s.db.WithContext(ctx).Create(rec).Error
}

// Update 按主键更新已存在记录的运行时字段。
//
// 刻意不包含 name：name 只在 Create 时写入，DB 是它的唯一真相；否则一旦改名
// 接口改写了 DB，后续任何一次运行态更新都会把旧名字写回去。
func (s *GormTerminalSessionStore) Update(ctx context.Context, rec *types.AppSession) error {
	if s == nil || s.db == nil || rec == nil || rec.ID <= 0 {
		return nil
	}
	updates := map[string]any{
		"status": rec.Status,
		// 持久标记只增不减：已持久化的会话不能被后续更新改回一次性。
		"persistent":     gorm.Expr("persistent OR ?", rec.Persistent),
		"last_access_at": rec.LastAccessAt,
		"updated_at":     time.Now(),
	}
	return s.db.WithContext(ctx).Model(&types.AppSession{}).Where("id = ?", rec.ID).Updates(updates).Error
}

func (s *GormTerminalSessionStore) Touch(ctx context.Context, key TerminalSessionKey, at time.Time) error {
	if s == nil || s.db == nil || key.ID <= 0 {
		return nil
	}
	return s.db.WithContext(ctx).
		Model(&types.AppSession{}).
		Where("id = ?", key.ID).
		Updates(map[string]any{"last_access_at": at, "updated_at": at}).Error
}

// MarkPersistent 把会话标记为持久（只会 false → true）。
//
// 记录不存在时是空操作：真正的持久化由随后的 Upsert 写回。
func (s *GormTerminalSessionStore) MarkPersistent(ctx context.Context, key TerminalSessionKey) error {
	if s == nil || s.db == nil || key.ID <= 0 {
		return nil
	}
	return s.db.WithContext(ctx).
		Model(&types.AppSession{}).
		Where("id = ? AND persistent = ?", key.ID, false).
		Updates(map[string]any{"persistent": true, "updated_at": time.Now()}).Error
}

func (s *GormTerminalSessionStore) Delete(ctx context.Context, key TerminalSessionKey) error {
	if s == nil || s.db == nil || key.ID <= 0 {
		return nil
	}
	return s.DeleteByID(ctx, key.ID)
}

func (s *GormTerminalSessionStore) DeleteByID(ctx context.Context, id int64) error {
	if s == nil || s.db == nil || id <= 0 {
		return nil
	}
	return s.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&types.AppSession{}).Error
}

func (s *GormTerminalSessionStore) List(ctx context.Context) ([]*types.AppSession, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	var rows []*types.AppSession
	if err := s.db.WithContext(ctx).Order("last_access_at ASC").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list app sessions: %w", err)
	}
	return rows, nil
}

func (s *GormTerminalSessionStore) ListByUser(ctx context.Context, userID int64) ([]*types.AppSession, error) {
	if s == nil || s.db == nil || userID <= 0 {
		return nil, nil
	}
	var rows []*types.AppSession
	err := s.db.WithContext(ctx).
		Where("user_id = ?", userID).
		Order("container_template_id ASC, created_at ASC").
		Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list app sessions for user %d: %w", userID, err)
	}
	return rows, nil
}
