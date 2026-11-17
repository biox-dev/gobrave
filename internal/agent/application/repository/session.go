package repository

import (
	"context"
	"errors"
	"time"

	agent "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"gorm.io/gorm"
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
		return tx.Where("id = ?", id).Delete(&agent.Session{}).Error
	})
}

// Fork 在一个事务内把源会话的历史复制到新会话。
//
// 复制规则：
//   - 新会话是顶层会话（ParentSessionID = 0），CurrentTaskID 清 0，避免前端误判为
//     有可续跑的任务；
//   - 消息 ID / Seq 重新分配（雪花 ID + 会话内自增序号）；
//   - TaskID 清 0：fork 出的分支与原任务无关，否则按任务回填时间线会互相污染；
//   - kind=error 的消息跳过（上一轮失败的产物不属于新分支）；
//   - UpToSeq > 0 时只复制 seq <= UpToSeq 的历史（从某条消息处分叉）；
//   - CopyFiles 时只复制每个 path 的最新版本，版本号重置为 initial。
//
// 事务只覆盖「会话行 + 消息行」；文件复制在事务内通过独立连接执行，失败时整体回滚
// 并补偿删除已复制的文件行（见 copyLatestFiles 的注释）。
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
// 两点约束：
//   - 查询走独立的 db 连接（不在事务 Statement 上复用）：事务内先 Create 过会话行，
//     GORM 的链式 API 会把 statement 条件（session_id = forked.ID）带到后续查询里，
//     导致这里把目标会话自己的行当成源数据；
//   - 取最新版本按 id 最大，而不是 created_at（同毫秒内的多版本时间戳相同）。
func (r *gormSessionStore) copyLatestFiles(ctx context.Context, sourceID, targetID int64, now time.Time) error {
	var paths []string
	if err := r.db.WithContext(ctx).Model(&agent.File{}).
		Where("session_id = ?", sourceID).
		Group("path").
		Pluck("path", &paths).Error; err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}

	latestByPath := make(map[string]*agent.File, len(paths))
	for _, path := range paths {
		var latest agent.File
		if err := r.db.WithContext(ctx).
			Where("session_id = ? AND path = ?", sourceID, path).
			Order("id DESC").
			Take(&latest).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				continue
			}
			return err
		}
		latestByPath[path] = &latest
	}

	// 复制同样走独立连接（与事务分离）：fork 事务只负责新建会话行与消息行，
	// 文件复制失败时由调用方整体回滚（会话行随事务消失，文件行需显式清理）。
	for _, latest := range latestByPath {
		copied := &agent.File{
			SessionID: targetID,
			Path:      latest.Path,
			Content:   latest.Content,
			Version:   agent.InitialVersion,
			CreatedAt: now,
			UpdatedAt: now,
		}
		if err := r.db.WithContext(ctx).Create(copied).Error; err != nil {
			return err
		}
	}
	return nil
}

func (r *gormSessionStore) List(ctx context.Context, params agent.SessionListParams) ([]*agent.Session, int64, error) {
	// 会话列表只展示顶层会话：子 Agent / 标题生成等子会话通过 parent_session_id 关联，
	// 不属于会话列表（需要时按父会话单独查询）。
	q := r.db.WithContext(ctx).Model(&agent.Session{}).Where("parent_session_id = 0")
	if params.UserID != 0 {
		q = q.Where("user_id = ?", params.UserID)
	}
	// ProjectID 用于会话按项目隔离：0 表示不按项目过滤。
	if params.ProjectID != 0 {
		q = q.Where("project_id = ?", params.ProjectID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var rows []*agent.Session
	if err := q.Order("updated_at DESC").Offset(params.Offset).Limit(params.Limit).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
