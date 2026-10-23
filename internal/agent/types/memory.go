package types

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
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
// UserID 是记忆的主归属维度（长期记忆按用户隔离）；ProjectID / SessionID 可选，
// 用于把记忆与某个具体项目 / 会话关联（例如该会话的摘要），检索时优先命中。
type Memory struct {
	ID        int64      `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	UserID    int64      `json:"user_id,string" gorm:"column:user_id;type:bigint;index:idx_agent_memories_user_kind,priority:1"`
	ProjectID int64      `json:"project_id,omitempty,string" gorm:"column:project_id;type:bigint;index"`
	SessionID int64      `json:"session_id,omitempty,string" gorm:"column:session_id;type:bigint;index"`
	Kind      MemoryKind `json:"kind" gorm:"column:kind;type:varchar(32);index:idx_agent_memories_user_kind,priority:2"`
	// Content 可能是整段会话 / 任务摘要或长事实，64KB 的 text 会触发 1406，这里用 longtext。
	Content string `json:"content" gorm:"column:content;type:longtext"`

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

// BeforeCreate 在写入数据库前用雪花 ID 初始化主键。
func (m *Memory) BeforeCreate(_ *gorm.DB) error {
	if m.ID == 0 {
		m.ID = utils.GenerateID()
	}
	return nil
}

// NewMemory 构建一条记忆；主键（ID）由持久化层的 BeforeCreate 在首次写入时分配。
func NewMemory(userID int64, projectID int64, sessionID int64, kind MemoryKind, content string) *Memory {
	now := time.Now()
	return &Memory{
		UserID:    userID,
		ProjectID: projectID,
		SessionID: sessionID,
		Kind:      kind,
		Content:   content,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// MemoryTurn 描述一轮已完成执行的上下文，供 MemoryExtractor 提取记忆。
type MemoryTurn struct {
	UserID    int64
	ProjectID int64
	SessionID int64
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
			NewMemory(turn.UserID, turn.ProjectID, turn.SessionID, MemoryKindSummary, content),
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

// MemoryQuery 描述一次记忆检索的上下文。
//
// UserID 是归属用户（必填）；ProjectID / SessionID 是可选的业务上下文，用于优先
// 返回同项目 / 同会话的记忆。Text 为关键词；为空时退化为按上下文 + 更新时间读取。
type MemoryQuery struct {
	UserID    int64
	ProjectID int64
	SessionID int64
	// Text 是检索关键词；为空时退化为「按上下文 + 更新时间」的分页读取。
	Text  string
	Limit int
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

// DefaultMemoryRetrieveLimit 是单次注入检索记忆的默认条数上限。
const DefaultMemoryRetrieveLimit = 5
