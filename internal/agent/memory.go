// Package agent 中的 memory 层：Agent 的「长期记忆」子系统。
//
// 与 conversation.go 中的「短期上下文」（单次会话的多轮历史）不同，memory 负责跨轮次、
// 跨会话、跨任务的持久化记忆，例如用户偏好、已确认的事实、任务摘要等。它遵循与权限
// 子系统一致的架构原则：Repository 是状态源，Manager 负责编排，检索 / 提取可插拔。
package agent

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryKind 描述记忆的类别，用于检索排序与前端展示。
type MemoryKind string

const (
	MemoryKindFact    MemoryKind = "fact"    // 用户偏好 / 事实（长期有效）
	MemoryKindSummary MemoryKind = "summary" // 会话 / 任务摘要
	MemoryKindNote    MemoryKind = "note"    // 通用备注
	MemoryKindEvent   MemoryKind = "event"   // 事件记录（带时间语义）
)

// 记忆相关错误。
var (
	// ErrMemoryNotFound 表示记忆不存在。
	ErrMemoryNotFound = errors.New("agent: memory not found")
	// ErrMemoryNotConfigured 表示 AgentService 未配置记忆管理器。
	ErrMemoryNotConfigured = errors.New("agent: memory manager not configured")
)

// Memory 是一条可跨轮次 / 跨会话检索的持久化记忆。
//
// UserID 是记忆的主归属维度（长期记忆按用户隔离）；SessionID 可选，用于把记忆
// 与某个具体会话关联（例如该会话的摘要）。
type Memory struct {
	ID        string     `json:"id" gorm:"column:id;primaryKey;type:varchar(64)"`
	UserID    string     `json:"user_id" gorm:"column:user_id;type:varchar(64);index:idx_agent_memories_user_kind,priority:1"`
	SessionID string     `json:"session_id,omitempty" gorm:"column:session_id;type:varchar(64);index"`
	Kind      MemoryKind `json:"kind" gorm:"column:kind;type:varchar(32);index:idx_agent_memories_user_kind,priority:2"`
	Content   string     `json:"content" gorm:"column:content;type:text"`

	// Importance 表示记忆重要度（0-10），检索排序时权重更高。
	Importance int `json:"importance" gorm:"column:importance"`
	// Metadata 承载扩展信息（来源、标签等），由序列化为 JSON 持久化。
	Metadata map[string]any `json:"metadata,omitempty" gorm:"column:metadata;serializer:json"`

	CreatedAt      time.Time  `json:"created_at" gorm:"column:created_at"`
	UpdatedAt      time.Time  `json:"updated_at" gorm:"column:updated_at"`
	LastAccessedAt *time.Time `json:"last_accessed_at,omitempty" gorm:"column:last_accessed_at"`
}

// TableName 返回记忆表的表名。
func (Memory) TableName() string { return "agent_memories" }

// NewMemory 构建一条记忆；ID 为空时由 Save 分配。
func NewMemory(userID, sessionID string, kind MemoryKind, content string) *Memory {
	now := time.Now()
	return &Memory{
		ID:        newID("mem"),
		UserID:    userID,
		SessionID: sessionID,
		Kind:      kind,
		Content:   content,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// MemoryRepository 持久化记忆。
//
// 与 TaskRepository / PermissionRepository 一致：当前提供内存实现，后续可替换为
// GORM 等基于 DB 的实现（见 repository_gorm.go）。
type MemoryRepository interface {
	Create(ctx context.Context, memory *Memory) error
	Get(ctx context.Context, id string) (*Memory, error)
	Update(ctx context.Context, memory *Memory) error
	Delete(ctx context.Context, id string) error
	// ListByUser 分页查询某用户的记忆（按 UpdatedAt 降序）；kinds 为空表示全部类别。
	ListByUser(ctx context.Context, userID string, offset, limit int, kinds ...MemoryKind) ([]*Memory, int64, error)
	// Search 按关键词检索某用户的记忆（返回按相关度 + 重要度排序）。
	// 基础实现为子串 / LIKE 匹配，可被更高级的检索器（向量 / 语义）替换。
	Search(ctx context.Context, userID, query string, limit int) ([]*Memory, error)
}

// MemoryRetriever 从记忆中检索与查询相关的内容。
//
// 与 Repository.Search 的区别：Retriever 是可插拔的「检索策略」抽象，
// 默认实现退化为关键词匹配（委托 Repository.Search）；后续可接入向量检索 / 语义检索，
// 而无须改动 AgentService 的注入逻辑。
type MemoryRetriever interface {
	// Retrieve 返回与 query 相关的记忆，按相关度降序，最多 limit 条。
	Retrieve(ctx context.Context, userID, query string, limit int) ([]*Memory, error)
}

// keywordMemoryRetriever 是默认检索器：直接委托 Repository.Search 做关键词匹配。
type keywordMemoryRetriever struct {
	repo MemoryRepository
}

func (r keywordMemoryRetriever) Retrieve(ctx context.Context, userID, query string, limit int) ([]*Memory, error) {
	// return r.repo.Search(ctx, userID, query, limit)
	memories, _, err := r.repo.ListByUser(ctx, userID, 0, limit)
	return memories, err
}

// MemoryTurn 描述一轮已完成执行的上下文，供 MemoryExtractor 提取记忆。
type MemoryTurn struct {
	UserID    string
	SessionID string
	Request   Request
	Result    *Result
}

// MemoryExtractor 从一轮完成的对话 / 任务结果中提取值得长期记住的记忆。
//
// 这是一个可选钩子：AgentService 在任务完成后调用，把提取出的记忆写入 Repository。
// 返回 nil 表示本轮无需写入任何记忆。默认不配置（不提取）；可由上层注入基于规则
// 或基于 LLM 的实现。
type MemoryExtractor interface {
	Extract(ctx context.Context, turn MemoryTurn) ([]*Memory, error)
}

// MemoryExtractorFunc 是把普通函数适配为 MemoryExtractor 的辅助类型。
type MemoryExtractorFunc func(ctx context.Context, turn MemoryTurn) ([]*Memory, error)

// Extract 实现 MemoryExtractor 接口。
func (f MemoryExtractorFunc) Extract(ctx context.Context, turn MemoryTurn) ([]*Memory, error) {
	return f(ctx, turn)
}

// MockMemoryExtractor 返回一个模拟的记忆提取器：把本轮的用户提问与助手回复
// 组合成一条 summary 记忆（内容做截断防止过长）。
//
// 用于演示 / 测试「任务完成 → 提取 → 落库」的调用链，不接入真实 LLM 或规则引擎；
// 生产环境应替换为基于规则或基于 LLM 的 MemoryExtractor 实现。
func MockMemoryExtractor() MemoryExtractor {
	return MemoryExtractorFunc(func(_ context.Context, turn MemoryTurn) ([]*Memory, error) {
		if turn.Result == nil || strings.TrimSpace(turn.Result.Content) == "" {
			return nil, nil
		}
		query := truncateRunes(lastUserMessage(turn.Request.Messages), 80)
		answer := truncateRunes(turn.Result.Content, 200)
		content := "用户问：" + query + "\n回答：" + answer
		return []*Memory{
			NewMemory(turn.UserID, turn.SessionID, MemoryKindSummary, content),
		}, nil
	})
}

// truncateRunes 按字符（rune）截断文本，超出时追加省略号，避免截断多字节字符。
func truncateRunes(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max]) + "…"
}

// MemoryConfig 是 MemoryManager 的依赖配置。
type MemoryConfig struct {
	Repo      MemoryRepository
	Retriever MemoryRetriever
	Extractor MemoryExtractor
}

// MemoryManager 负责记忆的创建、查询、检索与提取编排。
//
// 它是 memory 域的「门面」：上层（AgentService / HTTP 层）只依赖 MemoryManager，
// 不直接感知 Repository / Retriever / Extractor 的具体实现。
type MemoryManager struct {
	repo      MemoryRepository
	retriever MemoryRetriever
	extractor MemoryExtractor
}

// NewMemoryManager 创建记忆管理器；未提供的依赖使用安全默认值。
func NewMemoryManager(cfg MemoryConfig) *MemoryManager {
	if cfg.Repo == nil {
		cfg.Repo = NewMemoryRepository()
	}
	if cfg.Retriever == nil {
		cfg.Retriever = keywordMemoryRetriever{repo: cfg.Repo}
	}
	return &MemoryManager{
		repo:      cfg.Repo,
		retriever: cfg.Retriever,
		extractor: cfg.Extractor,
	}
}

// Save 创建或更新一条记忆：ID 为空时分配新 ID 并创建，否则更新。
func (m *MemoryManager) Save(ctx context.Context, memory *Memory) error {
	if memory == nil {
		return nil
	}
	now := time.Now()
	if memory.ID == "" {
		memory.ID = newID("mem")
		memory.CreatedAt = now
		memory.UpdatedAt = now
		return m.repo.Create(ctx, memory)
	}
	memory.UpdatedAt = now
	return m.repo.Update(ctx, memory)
}

// Get 按 ID 查询记忆。
func (m *MemoryManager) Get(ctx context.Context, id string) (*Memory, error) {
	return m.repo.Get(ctx, id)
}

// List 分页查询某用户的记忆；kinds 为空表示全部类别。
func (m *MemoryManager) List(ctx context.Context, userID string, offset, limit int, kinds ...MemoryKind) ([]*Memory, int64, error) {
	return m.repo.ListByUser(ctx, userID, offset, limit, kinds...)
}

// Retrieve 检索与 query 相关的记忆，并刷新这些记忆的访问时间（Touch）。
func (m *MemoryManager) Retrieve(ctx context.Context, userID, query string, limit int) ([]*Memory, error) {
	mems, err := m.retriever.Retrieve(ctx, userID, query, limit)
	if err != nil {
		return nil, err
	}
	for _, mem := range mems {
		_ = m.Touch(ctx, mem.ID)
	}
	return mems, nil
}

// Delete 删除记忆。
func (m *MemoryManager) Delete(ctx context.Context, id string) error {
	return m.repo.Delete(ctx, id)
}

// Touch 更新记忆的最近访问时间（用于淘汰 / 排序策略）。
func (m *MemoryManager) Touch(ctx context.Context, id string) error {
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

// BuildMemoryContext 把检索到的记忆格式化为注入上下文的文本块。
//
// 用于 AgentService 在调用前把相关记忆注入 SystemPrompt，让 Agent「记得」相关背景。
// 无记忆时返回空字符串。
func BuildMemoryContext(memories []*Memory) string {
	if len(memories) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## 相关记忆 (memory)\n")
	b.WriteString("以下是与此前对话相关的持久化记忆，请结合这些背景进行回答：\n")
	for _, mem := range memories {
		if mem == nil || strings.TrimSpace(mem.Content) == "" {
			continue
		}
		b.WriteString("- [")
		b.WriteString(string(mem.Kind))
		b.WriteString("] ")
		b.WriteString(mem.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// lastUserMessage 返回请求中的最后一条 user 消息，作为记忆检索的查询文本。
func lastUserMessage(messages []Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == RoleUser {
			return messages[i].Content
		}
	}
	return ""
}

// defaultMemoryRetrieveLimit 是单次注入检索记忆的默认条数上限。
const defaultMemoryRetrieveLimit = 5

// ---- 内存实现 ----

// NewMemoryRepository 创建记忆的内存实现。
func NewMemoryRepository() MemoryRepository {
	return &memoryMemoryRepository{mems: make(map[string]*Memory)}
}

type memoryMemoryRepository struct {
	mu   sync.RWMutex
	mems map[string]*Memory
}

func (r *memoryMemoryRepository) Create(_ context.Context, m *Memory) error {
	if m == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mems[m.ID] = cloneMemory(m)
	return nil
}

func (r *memoryMemoryRepository) Get(_ context.Context, id string) (*Memory, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.mems[id]
	if !ok {
		return nil, ErrMemoryNotFound
	}
	return cloneMemory(m), nil
}

func (r *memoryMemoryRepository) Update(_ context.Context, m *Memory) error {
	if m == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mems[m.ID] = cloneMemory(m)
	return nil
}

func (r *memoryMemoryRepository) Delete(_ context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.mems, id)
	return nil
}

func (r *memoryMemoryRepository) ListByUser(_ context.Context, userID string, offset, limit int, kinds ...MemoryKind) ([]*Memory, int64, error) {
	want := make(map[MemoryKind]bool, len(kinds))
	for _, k := range kinds {
		want[k] = true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := make([]*Memory, 0)
	for _, m := range r.mems {
		if m.UserID != userID {
			continue
		}
		if len(want) > 0 && !want[m.Kind] {
			continue
		}
		all = append(all, m)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].UpdatedAt.After(all[j].UpdatedAt) })
	total := int64(len(all))
	out := make([]*Memory, 0, limit)
	for _, m := range sliceRange(all, offset, limit) {
		out = append(out, cloneMemory(m))
	}
	return out, total, nil
}

func (r *memoryMemoryRepository) Search(_ context.Context, userID, query string, limit int) ([]*Memory, error) {
	query = strings.TrimSpace(query)
	r.mu.RLock()
	defer r.mu.RUnlock()
	type scored struct {
		m     *Memory
		score int
	}
	results := make([]scored, 0)
	for _, m := range r.mems {
		if m.UserID != userID {
			continue
		}
		score := matchMemoryScore(m, query)
		if score <= 0 {
			continue
		}
		results = append(results, scored{m: m, score: score})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].score != results[j].score {
			return results[i].score > results[j].score
		}
		return results[i].m.UpdatedAt.After(results[j].m.UpdatedAt)
	})
	out := make([]*Memory, 0, limit)
	for _, s := range sliceRange(results, 0, limit) {
		out = append(out, cloneMemory(s.m))
	}
	return out, nil
}

// matchMemoryScore 计算记忆与查询的匹配得分：空查询匹配全部（得分为重要度 + 1）；
// 否则先做整句短语匹配（加分），再按空白 / 标点切词做关键词匹配（命中一个词加分）。
func matchMemoryScore(m *Memory, query string) int {
	query = strings.TrimSpace(query)
	if query == "" {
		return m.Importance + 1
	}
	content := strings.ToLower(m.Content)
	ql := strings.ToLower(query)

	score := 0
	if strings.Contains(content, ql) {
		score += 20 // 整句命中，权重最高
	}
	terms := strings.FieldsFunc(ql, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', ',', '，', '。', '.', '!', '！', '?', '？', ';', '；', ':', '：':
			return true
		default:
			return false
		}
	})
	for _, term := range terms {
		if term != "" && strings.Contains(content, term) {
			score += 10 // 关键词命中
		}
	}
	if score == 0 {
		return 0
	}
	return score + m.Importance
}

// cloneMemory 返回记忆的副本，避免并发读写共享结构。
func cloneMemory(m *Memory) *Memory {
	if m == nil {
		return nil
	}
	c := *m
	if m.Metadata != nil {
		md := make(map[string]any, len(m.Metadata))
		for k, v := range m.Metadata {
			md[k] = v
		}
		c.Metadata = md
	}
	if m.LastAccessedAt != nil {
		v := *m.LastAccessedAt
		c.LastAccessedAt = &v
	}
	return &c
}
