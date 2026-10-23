package service

import (
	"context"
	"time"

	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// MemoryConfig 是 MemoryManager 的依赖配置。
type MemoryConfig struct {
	Repo      MemoryRepository
	Extractor MemoryExtractor
}

// MemoryManager 负责记忆的创建、查询、检索与提取编排。
//
// 它是 memory 域的「门面」：上层（AgentService / HTTP 层）只依赖 MemoryManager，
// 不直接感知 Repository / Retriever / Extractor 的具体实现。
type MemoryManager struct {
	repo      MemoryRepository
	extractor MemoryExtractor
}

// NewMemoryManager 创建记忆管理器。
func NewMemoryManager(cfg MemoryConfig) *MemoryManager {
	return &MemoryManager{
		repo:      cfg.Repo,
		extractor: cfg.Extractor,
	}
}

// Save 创建或更新一条记忆：ID 为 0（新建）时交给 Repository 创建，主键在写入前由
// BeforeCreate 分配；否则更新。
func (m *MemoryManager) Save(ctx context.Context, memory *Memory) error {
	if memory == nil {
		return nil
	}
	now := time.Now()
	if memory.ID == 0 {
		memory.CreatedAt = now
		memory.UpdatedAt = now
		return m.repo.Create(ctx, memory)
	}
	memory.UpdatedAt = now
	return m.repo.Update(ctx, memory)
}

// Get 按 ID 查询记忆。
func (m *MemoryManager) Get(ctx context.Context, id int64) (*Memory, error) {
	return m.repo.Get(ctx, id)
}

// List 分页查询某用户的记忆；kinds 为空表示全部类别。
func (m *MemoryManager) List(ctx context.Context, userID int64, offset, limit int, kinds ...MemoryKind) ([]*Memory, int64, error) {
	return m.repo.ListByUser(ctx, userID, offset, limit, kinds...)
}

// Retrieve 按「业务上下文 + 关键词」检索记忆，并刷新这些记忆的访问时间（Touch）。
func (m *MemoryManager) Retrieve(ctx context.Context, q MemoryQuery) ([]*Memory, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultMemoryRetrieveLimit
	}

	mems, _, err := m.repo.ListByUserContext(ctx, q.UserID, q.ProjectID, q.SessionID, 0, limit)
	if err != nil {
		return nil, err
	}
	for _, mem := range mems {
		_ = m.Touch(ctx, mem.ID)
	}
	return mems, nil
}

// Delete 删除记忆。
func (m *MemoryManager) Delete(ctx context.Context, id int64) error {
	return m.repo.Delete(ctx, id)
}

// Touch 更新记忆的最近访问时间（用于淘汰 / 排序策略）。
func (m *MemoryManager) Touch(ctx context.Context, id int64) error {
	mem, err := m.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	now := time.Now()
	mem.LastAccessedAt = &now
	return m.repo.Update(ctx, mem)
}

// Extract 调用已配置的提取器，从一轮执行中提取待写入的记忆。
// 未配置提取器时返回空（不提取）。
func (m *MemoryManager) Extract(ctx context.Context, turn MemoryTurn) ([]*Memory, error) {
	if m.extractor == nil {
		return nil, nil
	}
	return m.extractor.Extract(ctx, turn)
}
