package repository

import (
	"context"
	"errors"

	agent "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"gorm.io/gorm"
)

// NewGormMessageStore 创建基于数据库的消息 Store。
func NewGormMessageStore(db *gorm.DB) agentinterfaces.MessageStore {
	return &gormMessageStore{db: db}
}

type gormMessageStore struct {
	db *gorm.DB
}

// Create 写入一条消息，Seq 由会话内当前最大序号 +1 自动分配。
func (r *gormMessageStore) Create(ctx context.Context, m *agent.Message) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int
		if err := tx.Model(&agent.Message{}).
			Where("session_id = ?", m.SessionID).
			Select("COALESCE(MAX(seq), -1)").
			Scan(&maxSeq).Error; err != nil {
			return err
		}
		m.Seq = maxSeq + 1
		return tx.Create(m).Error
	})
}

func (r *gormMessageStore) Update(ctx context.Context, m *agent.Message) error {
	return r.db.WithContext(ctx).Save(m).Error
}

func (r *gormMessageStore) Get(ctx context.Context, id int64) (*agent.Message, error) {
	var m agent.Message
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agent.ErrMessageNotFound
		}
		return nil, err
	}
	return &m, nil
}

func (r *gormMessageStore) List(ctx context.Context, sessionID int64) ([]*agent.Message, error) {
	var rows []*agent.Message
	if err := r.db.WithContext(ctx).
		Where("session_id = ?", sessionID).
		Order("seq ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListPage 按 Seq 倒序取一页（最新 / 游标之前的）消息，返回前翻转为 Seq 升序。
//
// 多取一条用于判断 hasMore，避免额外的 COUNT 查询；seq 上有索引，
// 因此 (session_id, seq) 范围扫描的代价与页大小成正比，而不是与历史总量成正比。
func (r *gormMessageStore) ListPage(ctx context.Context, sessionID int64, beforeSeq, limit int) ([]*agent.Message, bool, error) {
	if limit <= 0 {
		limit = 1
	}
	query := r.db.WithContext(ctx).Where("session_id = ?", sessionID)
	if beforeSeq > 0 {
		query = query.Where("seq < ?", beforeSeq)
	}

	var rows []*agent.Message
	if err := query.
		Order("seq DESC").
		Limit(limit + 1).
		Find(&rows).Error; err != nil {
		return nil, false, err
	}

	hasMore := len(rows) > limit
	if hasMore {
		rows = rows[:limit]
	}
	// 倒序取出，返回前翻转为 Seq 升序，前端可直接按顺序渲染。
	for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
		rows[i], rows[j] = rows[j], rows[i]
	}
	r.fillOwnerNames(ctx, rows)
	return rows, hasMore, nil
}

// fillOwnerNames 按 UserID 批量回填消息归属者的展示名（IN 查询，避免 N+1）：
//
//   - 用户消息（RoleUser）的 UserID 是用户 ID → users.username；
//   - Agent 侧消息的 UserID 是 Profile ID → agent_profiles.name。
//
// 精简库（单测）没有 users / agent_profiles 表时静默跳过，保持回填前的行为。
func (r *gormMessageStore) fillOwnerNames(ctx context.Context, rows []*agent.Message) {
	if len(rows) == 0 {
		return
	}

	var userIDs, profileIDs []int64
	seenUser := make(map[int64]struct{}, len(rows))
	seenProfile := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		if row.UserID == 0 {
			continue
		}
		seen := seenProfile
		ids := &profileIDs
		if row.Role == agent.RoleUser {
			seen = seenUser
			ids = &userIDs
		}
		if _, ok := seen[row.UserID]; ok {
			continue
		}
		seen[row.UserID] = struct{}{}
		*ids = append(*ids, row.UserID)
	}

	userNames := make(map[int64]string, len(userIDs))
	if len(userIDs) > 0 && r.db.Migrator().HasTable("users") {
		var users []struct {
			ID       int64
			Username string
		}
		if err := r.db.WithContext(ctx).Table("users").
			Select("id, username").Where("id IN ?", userIDs).Scan(&users).Error; err == nil {
			for _, u := range users {
				userNames[u.ID] = u.Username
			}
		}
	}

	profileNames := make(map[int64]string, len(profileIDs))
	if len(profileIDs) > 0 && r.db.Migrator().HasTable("agent_profiles") {
		var profiles []struct {
			ID   int64
			Name string
		}
		if err := r.db.WithContext(ctx).Table("agent_profiles").
			Select("id, name").Where("id IN ?", profileIDs).Scan(&profiles).Error; err == nil {
			for _, p := range profiles {
				profileNames[p.ID] = p.Name
			}
		}
	}

	for _, row := range rows {
		if row.Role == agent.RoleUser {
			row.Username = userNames[row.UserID]
			continue
		}
		row.ProfileName = profileNames[row.UserID]
	}
}

func (r *gormMessageStore) Delete(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&agent.Message{}).Error
}

func (r *gormMessageStore) DeleteBySession(ctx context.Context, sessionID int64) error {
	return r.db.WithContext(ctx).Where("session_id = ?", sessionID).Delete(&agent.Message{}).Error
}

// ListParticipants 返回在某会话中发过言（写过 user 消息）的用户（去重，按用户 ID 升序）。
//
// 直接用消息表作为 source of truth，不维护额外的 session_user 关系表：
//   - 查询条件固定为 session_id + role = 'user' + user_id > 0，命中复合索引
//     (session_id, role, user_id) 时只扫索引，不回表，代价与消息条数同阶但极小；
//   - 用户名在读取时从 users 表回填（IN 查询），用户改名后不会展示旧名字；
//   - 精简库（单测）没有 users 表时只返回 ID，Username 为空。
func (r *gormMessageStore) ListParticipants(ctx context.Context, sessionID int64) ([]agent.SessionUser, error) {
	var ids []int64
	if err := r.db.WithContext(ctx).Model(&agent.Message{}).
		Where("session_id = ? AND role = ? AND user_id > 0", sessionID, agent.RoleUser).
		Distinct().
		Order("user_id").
		Pluck("user_id", &ids).Error; err != nil {
		return nil, err
	}

	// 无参与者时返回空切片（而不是 nil），保证 JSON 序列化为 []。
	users := make([]agent.SessionUser, 0, len(ids))
	if len(ids) == 0 {
		return users, nil
	}

	names := make(map[int64]string, len(ids))
	if r.db.Migrator().HasTable("users") {
		var rows []struct {
			ID       int64
			Username string
		}
		if err := r.db.WithContext(ctx).Table("users").
			Select("id, username").Where("id IN ?", ids).Scan(&rows).Error; err == nil {
			for _, u := range rows {
				names[u.ID] = u.Username
			}
		}
	}

	for _, id := range ids {
		users = append(users, agent.SessionUser{ID: id, Username: names[id]})
	}
	return users, nil
}
