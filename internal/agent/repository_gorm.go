package agent

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 本文件定义基于 GORM（数据库）的 Repository 实现，与 repository.go 中的内存实现
// 实现同一组接口。容器启动时通过依赖注入切换到这里的实现，即可让任务 / 权限 / 事件
// 状态跨进程重启持久化（“数据库才是状态源”，见 design.md）。

// NewGormTaskRepository 创建基于数据库的任务 Repository。
func NewGormTaskRepository(db *gorm.DB) TaskRepository {
	return &gormTaskRepository{db: db}
}

// NewGormPermissionRepository 创建基于数据库的权限请求 Repository。
func NewGormPermissionRepository(db *gorm.DB) PermissionRepository {
	return &gormPermissionRepository{db: db}
}

// NewGormEventRepository 创建基于数据库的事件流 Repository。
func NewGormEventRepository(db *gorm.DB) EventRepository {
	return &gormEventRepository{db: db}
}

// NewGormConversationRepository 创建基于数据库的会话 Repository。
//
// 会话与其消息（agent_conversation_messages 表）的关系由本实现自行维护，
// 不使用 GORM 的 HasMany 关联。
func NewGormConversationRepository(db *gorm.DB) ConversationRepository {
	return &gormConversationRepository{db: db}
}

// NewGormMemoryRepository 创建基于数据库的记忆 Repository。
func NewGormMemoryRepository(db *gorm.DB) MemoryRepository {
	return &gormMemoryRepository{db: db}
}

// ---- Task ----

type gormTaskRepository struct {
	db *gorm.DB
}

func (r *gormTaskRepository) Create(ctx context.Context, task *Task) error {
	if task == nil {
		return nil
	}
	return r.db.WithContext(ctx).Create(task).Error
}

func (r *gormTaskRepository) Get(ctx context.Context, id string) (*Task, error) {
	var task Task
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&task).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrTaskNotFound
		}
		return nil, err
	}
	return &task, nil
}

func (r *gormTaskRepository) Update(ctx context.Context, task *Task) error {
	if task == nil {
		return nil
	}
	return r.db.WithContext(ctx).Save(task).Error
}

func (r *gormTaskRepository) ListByStatus(ctx context.Context, statuses ...TaskStatus) ([]*Task, error) {
	q := r.db.WithContext(ctx)
	if len(statuses) > 0 {
		q = q.Where("status IN ?", statuses)
	}
	var tasks []*Task
	if err := q.Order("created_at ASC").Find(&tasks).Error; err != nil {
		return nil, err
	}
	return tasks, nil
}

func (r *gormTaskRepository) Page(ctx context.Context, offset, limit int, statuses ...TaskStatus) ([]*Task, int64, error) {
	q := r.db.WithContext(ctx).Model(&Task{})
	if len(statuses) > 0 {
		q = q.Where("status IN ?", statuses)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var tasks []*Task
	if err := q.Order("created_at ASC").Offset(offset).Limit(limit).Find(&tasks).Error; err != nil {
		return nil, 0, err
	}
	return tasks, total, nil
}

// ---- Permission ----

type gormPermissionRepository struct {
	db *gorm.DB
}

func (r *gormPermissionRepository) Create(ctx context.Context, p *PermissionRequest) error {
	if p == nil {
		return nil
	}
	return r.db.WithContext(ctx).Create(p).Error
}

func (r *gormPermissionRepository) Get(ctx context.Context, id string) (*PermissionRequest, error) {
	var p PermissionRequest
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&p).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrPermissionNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (r *gormPermissionRepository) Update(ctx context.Context, p *PermissionRequest) error {
	if p == nil {
		return nil
	}
	return r.db.WithContext(ctx).Save(p).Error
}

func (r *gormPermissionRepository) ListPendingByTask(ctx context.Context, taskID string) ([]*PermissionRequest, error) {
	var perms []*PermissionRequest
	if err := r.db.WithContext(ctx).
		Where("task_id = ? AND status = ?", taskID, PermissionPending).
		Order("created_at ASC").
		Find(&perms).Error; err != nil {
		return nil, err
	}
	return perms, nil
}

func (r *gormPermissionRepository) Page(ctx context.Context, offset, limit int, taskID string, statuses ...PermissionStatus) ([]*PermissionRequest, int64, error) {
	q := r.db.WithContext(ctx).Model(&PermissionRequest{})
	if taskID != "" {
		q = q.Where("task_id = ?", taskID)
	}
	if len(statuses) > 0 {
		q = q.Where("status IN ?", statuses)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var perms []*PermissionRequest
	if err := q.Order("created_at ASC").Offset(offset).Limit(limit).Find(&perms).Error; err != nil {
		return nil, 0, err
	}
	return perms, total, nil
}

// ---- Event ----

type gormEventRepository struct {
	db *gorm.DB
}

func (r *gormEventRepository) Append(ctx context.Context, event *AgentEvent) error {
	if event == nil {
		return nil
	}
	// 在事务内分配 per-task 单调递增的 sequence。单个任务的执行 goroutine 串行产出
	// 事件，因此同一 task 的 Append 不会并发；事务保证并发任务之间互不干扰。
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSeq int64
		if err := tx.Model(&AgentEvent{}).
			Where("task_id = ?", event.TaskID).
			Select("COALESCE(MAX(sequence), 0)").
			Scan(&maxSeq).Error; err != nil {
			return err
		}
		event.Sequence = maxSeq + 1
		return tx.Create(event).Error
	})
}

func (r *gormEventRepository) ListByTask(ctx context.Context, taskID string, after int64) ([]*AgentEvent, error) {
	var events []*AgentEvent
	if err := r.db.WithContext(ctx).
		Where("task_id = ? AND sequence > ?", taskID, after).
		Order("sequence ASC").
		Find(&events).Error; err != nil {
		return nil, err
	}
	return events, nil
}

func (r *gormEventRepository) Page(ctx context.Context, taskID string, offset, limit int) ([]*AgentEvent, int64, error) {
	q := r.db.WithContext(ctx).Model(&AgentEvent{})
	if taskID != "" {
		q = q.Where("task_id = ?", taskID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var events []*AgentEvent
	if err := q.Order("created_at ASC").Order("sequence ASC").Offset(offset).Limit(limit).Find(&events).Error; err != nil {
		return nil, 0, err
	}
	// 保证 created_at 相同时按 sequence 稳定排序（与内存实现语义一致）。
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].CreatedAt.Equal(events[j].CreatedAt) {
			return events[i].Sequence < events[j].Sequence
		}
		return events[i].CreatedAt.Before(events[j].CreatedAt)
	})
	return events, total, nil
}

// ---- Conversation ----

type gormConversationRepository struct {
	db *gorm.DB
}

func (r *gormConversationRepository) Get(ctx context.Context, id string) (*Conversation, error) {
	var conv Conversation
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&conv).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrConversationNotFound
		}
		return nil, err
	}
	msgs, err := r.listMessages(ctx, id)
	if err != nil {
		return nil, err
	}
	conv.Messages = msgs
	return &conv, nil
}

func (r *gormConversationRepository) Create(ctx context.Context, conv *Conversation) error {
	if conv == nil {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(conv).Error; err != nil {
			return err
		}
		return replaceMessages(tx, conv)
	})
}

func (r *gormConversationRepository) Update(ctx context.Context, conv *Conversation) error {
	if conv == nil {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(conv).Error; err != nil {
			return err
		}
		return replaceMessages(tx, conv)
	})
}

func (r *gormConversationRepository) Page(ctx context.Context, userID string, offset, limit int) ([]*Conversation, int64, error) {
	q := r.db.WithContext(ctx).Model(&Conversation{})
	if userID != "" {
		q = q.Where("user_id = ?", userID)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var convs []*Conversation
	if err := q.Order("updated_at DESC").Offset(offset).Limit(limit).Find(&convs).Error; err != nil {
		return nil, 0, err
	}
	for _, c := range convs {
		msgs, err := r.listMessages(ctx, c.ID)
		if err != nil {
			return nil, 0, err
		}
		c.Messages = msgs
	}
	return convs, total, nil
}

// listMessages 按顺序加载会话的消息。
func (r *gormConversationRepository) listMessages(ctx context.Context, conversationID string) ([]Message, error) {
	var rows []*ConversationMessage
	if err := r.db.WithContext(ctx).
		Where("conversation_id = ?", conversationID).
		Order("seq ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	msgs := make([]Message, 0, len(rows))
	for _, row := range rows {
		msgs = append(msgs, Message{Role: row.Role, Content: row.Content})
	}
	return msgs, nil
}

// replaceMessages 重建会话的消息：先删除旧消息，再按当前顺序重新写入。
//
// 由于 ConversationService 每次 Update 都携带完整消息历史，这里采用「先删后插」的
// 幂等策略，避免区分增量 / 全量带来的状态不一致。
func replaceMessages(tx *gorm.DB, conv *Conversation) error {
	if err := tx.Where("conversation_id = ?", conv.ID).Delete(&ConversationMessage{}).Error; err != nil {
		return err
	}
	now := time.Now()
	for i, m := range conv.Messages {
		row := &ConversationMessage{
			ID:             newID("msg"),
			ConversationID: conv.ID,
			Seq:            i,
			Role:           m.Role,
			Content:        m.Content,
			CreatedAt:      now,
		}
		if err := tx.Create(row).Error; err != nil {
			return err
		}
	}
	return nil
}

// ---- Memory ----

type gormMemoryRepository struct {
	db *gorm.DB
}

func (r *gormMemoryRepository) Create(ctx context.Context, m *Memory) error {
	if m == nil {
		return nil
	}
	return r.db.WithContext(ctx).Create(m).Error
}

func (r *gormMemoryRepository) Get(ctx context.Context, id string) (*Memory, error) {
	var m Memory
	if err := r.db.WithContext(ctx).Where("id = ?", id).First(&m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrMemoryNotFound
		}
		return nil, err
	}
	return &m, nil
}

func (r *gormMemoryRepository) Update(ctx context.Context, m *Memory) error {
	if m == nil {
		return nil
	}
	return r.db.WithContext(ctx).Save(m).Error
}

func (r *gormMemoryRepository) Delete(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Where("id = ?", id).Delete(&Memory{}).Error
}

func (r *gormMemoryRepository) ListByUser(ctx context.Context, userID string, offset, limit int, kinds ...MemoryKind) ([]*Memory, int64, error) {
	q := r.db.WithContext(ctx).Model(&Memory{}).Where("user_id = ?", userID)
	if len(kinds) > 0 {
		q = q.Where("kind IN ?", kinds)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var mems []*Memory
	if err := q.Order("updated_at DESC").Offset(offset).Limit(limit).Find(&mems).Error; err != nil {
		return nil, 0, err
	}
	return mems, total, nil
}

func (r *gormMemoryRepository) Search(ctx context.Context, userID, query string, limit int) ([]*Memory, error) {
	q := r.db.WithContext(ctx).Model(&Memory{}).Where("user_id = ?", userID)
	if strings.TrimSpace(query) != "" {
		q = q.Where("content LIKE ?", "%"+query+"%")
	}

	var mems []*Memory
	if err := q.Order("importance DESC").Order("updated_at DESC").Limit(limit).Find(&mems).Error; err != nil {
		return nil, err
	}
	return mems, nil
}
