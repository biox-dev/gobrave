package repository

import (
	"context"
	"errors"
	"strings"
	"time"

	agent "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// NewGormSessionStore 创建基于数据库的会话 Store。
func NewGormSessionStore(db *gorm.DB) agentinterfaces.SessionStore {
	return &gormSessionStore{db: db}
}

type gormSessionStore struct {
	db *gorm.DB
}

func (r *gormSessionStore) Create(ctx context.Context, s *agent.Session) error {
	return r.db.WithContext(ctx).Create(s).Error
}

func (r *gormSessionStore) Get(ctx context.Context, id int64) (*agent.Session, error) {
	var s agent.Session
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&s).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, agent.ErrSessionNotFound
		}
		return nil, err
	}
	return &s, nil
}

func (r *gormSessionStore) Update(ctx context.Context, s *agent.Session) error {
	return r.db.WithContext(ctx).Save(s).Error
}

// Delete 删除会话，并级联清理其消息与文件历史。
func (r *gormSessionStore) Delete(ctx context.Context, id int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("session_id = ?", id).Delete(&agent.Message{}).Error; err != nil {
			return err
		}
		if err := tx.Where("session_id = ?", id).Delete(&agent.File{}).Error; err != nil {
			return err
		}
		// 会话删除时级联清理资源关联，避免留下指向已删除会话的孤儿记录。
		if err := tx.Where("session_id = ?", id).Delete(&agent.SessionResource{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&agent.Session{}).Error
	})
}

// Fork 在一个事务内把源会话的历史复制到新会话。
//
// 复制规则：
//   - 新会话是顶层会话（ParentSessionID = 0），CurrentTaskID 清 0，避免前端误判为
//     有可续跑的任务；
//   - 消息 ID / Seq 重新分配（雪花 ID + 会话内自增序号）；
//   - 消息的 TaskID 原样保留：前端按 task_id 分组回填时间线，若清 0 会导致
//     历史消息失去任务归属、与 fork 分支中的新消息混在一起；
//   - kind=error 的消息跳过（上一轮失败的产物不属于新分支）；
//   - UpToSeq > 0 时只复制 seq <= UpToSeq 的历史（从某条消息处分叉）；
//   - CopyFiles 时只复制每个 path 的最新版本，版本号重置为 initial。
//
// 事务只覆盖「会话行 + 消息行」；文件复制在事务提交后用独立连接执行，失败时补偿
// 删除刚建好的会话（级联删消息与已复制的文件行，见 copyLatestFiles 的注释）。
func (r *gormSessionStore) Fork(ctx context.Context, params agent.SessionForkParams) (*agent.Session, error) {
	var forked agent.Session
	var source agent.Session

	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ?", params.SourceSessionID).First(&source).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return agent.ErrSessionNotFound
			}
			return err
		}
		// 归属校验放在仓储层：所有 fork 入口都从这里过，不存在绕过校验的路径。
		if params.UserID != 0 && source.UserID != params.UserID {
			return agent.ErrSessionNotFound
		}

		// 统一用 UTC：GORM 的 NowFunc 与 MySQL(loc=UTC)/Postgres(TimeZone=UTC)
		// 都是 UTC，若这里用 time.Now()（本地时区）就会同一行出现
		// created_at=+08:00、updated_at=Z 的混用格式。
		now := time.Now().UTC()
		title := params.Title
		if title == "" {
			title = source.Title + " (fork)"
		}
		forked = agent.Session{
			ProjectID: source.ProjectID,
			Title:     title,
			UserID:    source.UserID,
			Provider:  source.Provider,
			Model:     source.Model,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := tx.Session(&gorm.Session{}).Create(&forked).Error; err != nil {
			return err
		}

		var messages []*agent.Message
		msgQuery := tx.Session(&gorm.Session{}).Where("session_id = ?", source.ID)
		// UpToSeq 非 nil：只复制截止到该 seq 的历史（含该条），即「从某条消息处分叉」。
		if params.UpToSeq != nil {
			msgQuery = msgQuery.Where("seq <= ?", *params.UpToSeq)
		}
		if err := msgQuery.Order("seq ASC").Find(&messages).Error; err != nil {
			return err
		}
		seq := 0
		for _, m := range messages {
			if m.Kind == agent.KindError {
				continue
			}
			copied := &agent.Message{
				SessionID: forked.ID,
				Seq:       seq,
				Role:      m.Role,
				Kind:      m.Kind,
				TaskID:    m.TaskID,
				Content:   m.Content,
				Data:      m.Data,
				CreatedAt: now,
			}
			seq++
			if err := tx.Session(&gorm.Session{}).Create(copied).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// 文件复制放在事务外：避免与事务内的 statement 条件复用问题纠缠，
	// 也避免把大量文件内容复制压进同一个长事务。
	if params.CopyFiles {
		if err := r.copyLatestFiles(ctx, source.ID, forked.ID, forked.CreatedAt); err != nil {
			// 补偿：删掉已写入的文件行与会话（会话 Del 会级联删消息与文件）。
			_ = r.Delete(context.Background(), forked.ID)
			return nil, err
		}
	}
	return &forked, nil
}

// copyLatestFiles 把源会话中每个 path 的最新版本复制为新会话的 initial 版本。
//
// 三点约束：
//   - 查询走独立的 db 连接（不在事务 Statement 上复用）：事务内先 Create 过会话行，
//     GORM 的链式 API 会把 statement 条件（session_id = forked.ID）带到后续查询里，
//     导致这里把目标会话自己的行当成源数据；
//   - 取最新版本按 id 最大，而不是 created_at（同毫秒内的多版本时间戳相同）；
//     用一条 JOIN 一次取出全部 path 的最新行，不再按 path 循环查询；
//   - 写入带 ON DUPLICATE KEY UPDATE（clause.OnConflict{DoNothing}）：唯一键正常是
//     (session_id, path, version)，新会话不会冲突；但 MySQL 的 utf8mb4_*_ai_ci 排序
//     规则把仅大小写不同（以及尾部空格）的 path 视为同一个值，源会话里同时存在
//     a.R / A.r 时第二条会撞键。此时跳过而不是让整个 fork 失败——缺的那条会在该
//     文件第一次被写入时由 Write/Edit 重新以 initial 版本补上。
//
// 注意：若部署库里 uk_history_session_path_version 仍是旧的 (path, version)（早期
// 模型定义，AutoMigrate 不会改写已存在的索引），复制会因跨会话同路径同版本而
// 全部被跳过，fork 仍能成功但不会带上文件历史。修复见
// migrations/2026-11-17-agent-history-unique-session-path-version。
func (r *gormSessionStore) copyLatestFiles(ctx context.Context, sourceID, targetID int64, now time.Time) error {
	latestIDs := r.db.WithContext(ctx).Model(&agent.File{}).
		Select("path, MAX(id) AS max_id").
		Where("session_id = ?", sourceID).
		Group("path")

	var sources []*agent.File
	if err := r.db.WithContext(ctx).
		Table("agent_history_files AS f").
		Joins("INNER JOIN (?) AS latest ON f.id = latest.max_id", latestIDs).
		Where("f.session_id = ?", sourceID).
		Find(&sources).Error; err != nil {
		return err
	}
	if len(sources) == 0 {
		return nil
	}

	copied := make([]*agent.File, 0, len(sources))
	for _, src := range sources {
		copied = append(copied, &agent.File{
			SessionID: targetID,
			Path:      src.Path,
			Content:   src.Content,
			Version:   agent.InitialVersion,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	return r.db.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		Create(&copied).Error
}

func (r *gormSessionStore) List(ctx context.Context, params agent.SessionListParams) ([]*agent.Session, int64, error) {
	// 会话列表只展示顶层会话：子 Agent / 标题生成等子会话通过 parent_session_id 关联，
	// 不属于会话列表（需要时按父会话单独查询）。
	q := r.db.WithContext(ctx).Model(&agent.Session{}).Where("parent_session_id = 0")
	// users 表只在真实库中存在；单测等精简库里没有 users，跳过 join 与用户名回填。
	hasUsers := r.db.Migrator().HasTable("users")
	if hasUsers {
		// LEFT JOIN users 用于「按发起人用户名搜索」；注意限定表名，避免 id / created_at 等列歧义。
		q = q.Joins("LEFT JOIN users ON users.id = agent_sessions.user_id")
	}
	if params.UserID != 0 {
		q = q.Where("user_id = ?", params.UserID)
	}
	// ProjectID 用于会话按项目隔离：0 表示不按项目过滤。
	if params.ProjectID != 0 {
		q = q.Where("project_id = ?", params.ProjectID)
	}
	if keyword := strings.TrimSpace(params.Keyword); keyword != "" {
		like := "%" + keyword + "%"
		if hasUsers {
			q = q.Where("(agent_sessions.title LIKE ? OR users.username LIKE ?)", like, like)
		} else {
			q = q.Where("agent_sessions.title LIKE ?", like)
		}
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []*agent.Session
	// join 后必须限定选择列，否则 users.id 会覆盖会话主键。
	if err := q.Select("agent_sessions.*").Order("agent_sessions.updated_at DESC").Offset(params.Offset).Limit(params.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	if hasUsers {
		r.fillUsernames(ctx, rows)
	}
	return rows, total, nil
}

// fillUsernames 按 UserID 批量回填用户显示名（一次查询，避免 N+1）。
func (r *gormSessionStore) fillUsernames(ctx context.Context, rows []*agent.Session) {
	if len(rows) == 0 {
		return
	}
	ids := make([]int64, 0, len(rows))
	seen := make(map[int64]struct{}, len(rows))
	for _, row := range rows {
		if row.UserID == 0 {
			continue
		}
		if _, ok := seen[row.UserID]; ok {
			continue
		}
		seen[row.UserID] = struct{}{}
		ids = append(ids, row.UserID)
	}
	if len(ids) == 0 {
		return
	}
	var users []struct {
		ID       int64
		Username string
	}
	if err := r.db.WithContext(ctx).Table("users").Select("id, username").Where("id IN ?", ids).Scan(&users).Error; err != nil {
		return
	}
	names := make(map[int64]string, len(users))
	for _, u := range users {
		names[u.ID] = u.Username
	}
	for _, row := range rows {
		row.Username = names[row.UserID]
	}
}
