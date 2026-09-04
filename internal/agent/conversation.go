package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// 本文件定义「多轮对话」层：把一次会话(Conversation)与一轮执行(Task)拆成两层。
//
//   - Conversation：跨轮次持久化的历史(Message 列表)与上下文，是会话的聚合根；
//   - Task：一轮执行，复用 AgentService 既有的 created→running→waiting→completed 状态机。
//
// ConversationService 只负责「历史拼接」与「轮次编排」，把每一轮都当作一次普通的
// Request 交给 AgentService 执行；AgentService 完全感知不到「多轮」这个概念。

// 会话相关错误。
var (
	// ErrConversationNotFound 表示会话不存在。
	ErrConversationNotFound = errors.New("agent: conversation not found")
)

// Conversation 是一次多轮对话的持久化对象。
//
// Messages 保存完整历史（system 提示词之外的 user/assistant 消息），
// 每一轮执行前把历史拼进 Request.Messages，执行结束后把 assistant 回复写回历史。
type Conversation struct {
	ID       int64  `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	UserID   string `json:"user_id" gorm:"column:user_id;type:varchar(64);index"`
	Provider string `json:"provider" gorm:"column:provider;type:varchar(64)"`
	Model    string `json:"model" gorm:"column:model;type:varchar(128)"`
	// Messages 保存完整历史（system 提示词之外的 user/assistant 消息）。
	// 关系由 Repository 自行维护（独立表 agent_conversation_messages），
	// 不使用 GORM 的 HasMany 关联，因此标记为 gorm:"-"。
	Messages []Message `json:"messages" gorm:"-"`
	// CurrentTaskID 记录当前活跃轮次（running / waiting_permission）的任务 ID；
	// 轮次结束时置空。供前端刷新 / 切换会话时恢复实时流。
	CurrentTaskID int64     `json:"current_task_id,string" gorm:"column:current_task_id;type:bigint"`
	CreatedAt     time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt     time.Time `json:"updated_at" gorm:"column:updated_at"`
}

// TableName 返回会话表的表名。
func (Conversation) TableName() string { return "agent_conversations" }

// BeforeCreate 在写入数据库前用雪花 ID 初始化主键。
func (c *Conversation) BeforeCreate(_ *gorm.DB) error {
	if c.ID == 0 {
		c.ID = utils.GenerateID()
	}
	return nil
}

// ConversationMessage 是会话中的一条消息，以独立表持久化。
//
// 与 Conversation 的关系（ConversationID 外键）由 Repository 自行维护，
// 不使用 GORM 的 HasMany / Preload 关联机制。
type ConversationMessage struct {
	ID             int64     `json:"id,string" gorm:"column:id;primaryKey;type:bigint;autoIncrement:false"`
	ConversationID int64     `json:"conversation_id,string" gorm:"column:conversation_id;type:bigint;index"`
	Seq            int       `json:"seq" gorm:"column:seq;index"`
	Role           string    `json:"role" gorm:"column:role;type:varchar(32)"`
	Kind           string    `json:"kind,omitempty" gorm:"column:kind;type:varchar(32);index"`
	TaskID         int64     `json:"task_id,string,omitempty" gorm:"column:task_id;type:bigint;index"`
	Content        string    `json:"content" gorm:"column:content;type:text"`
	Data           any       `json:"data,omitempty" gorm:"serializer:json"`
	CreatedAt      time.Time `json:"created_at" gorm:"column:created_at"`
}

// TableName 返回会话消息表的表名。
func (ConversationMessage) TableName() string { return "agent_conversation_messages" }

// BeforeCreate 在写入数据库前用雪花 ID 初始化主键。
func (m *ConversationMessage) BeforeCreate(_ *gorm.DB) error {
	if m.ID == 0 {
		m.ID = utils.GenerateID()
	}
	return nil
}

// NewConversation 创建一个空的会话。
func NewConversation(userID, provider, model string) *Conversation {
	now := time.Now()
	return &Conversation{
		UserID:    userID,
		Provider:  provider,
		Model:     model,
		Messages:  []Message{},
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// ConversationRepository 持久化会话。
//
// 与 TaskRepository 一致：当前提供内存实现保证链路可跑通，后续可替换为 DB 实现。
type ConversationRepository interface {
	Get(ctx context.Context, id int64) (*Conversation, error)
	Create(ctx context.Context, conv *Conversation) error
	Update(ctx context.Context, conv *Conversation) error
	// Page 分页查询会话（按 UpdatedAt 降序）；userID 为空表示全部用户。
	Page(ctx context.Context, userID string, offset, limit int) ([]*Conversation, int64, error)
}

// NewMemoryConversationRepository 创建会话的内存实现。
func NewMemoryConversationRepository() ConversationRepository {
	return &memoryConversationRepository{convs: make(map[int64]*Conversation)}
}

type memoryConversationRepository struct {
	mu    sync.RWMutex
	convs map[int64]*Conversation
}

func (r *memoryConversationRepository) Get(_ context.Context, id int64) (*Conversation, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.convs[id]
	if !ok {
		return nil, ErrConversationNotFound
	}
	return cloneConversation(c), nil
}

func (r *memoryConversationRepository) Create(_ context.Context, conv *Conversation) error {
	if conv == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.convs[conv.ID] = cloneConversation(conv)
	return nil
}

func (r *memoryConversationRepository) Update(_ context.Context, conv *Conversation) error {
	if conv == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.convs[conv.ID] = cloneConversation(conv)
	return nil
}

func (r *memoryConversationRepository) Page(_ context.Context, userID string, offset, limit int) ([]*Conversation, int64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	all := make([]*Conversation, 0)
	for _, c := range r.convs {
		if userID == "" || c.UserID == userID {
			all = append(all, c)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].UpdatedAt.After(all[j].UpdatedAt) })
	total := int64(len(all))
	out := make([]*Conversation, 0, limit)
	for _, c := range sliceRange(all, offset, limit) {
		out = append(out, cloneConversation(c))
	}
	return out, total, nil
}

func cloneConversation(c *Conversation) *Conversation {
	if c == nil {
		return nil
	}
	d := *c
	d.Messages = append([]Message(nil), c.Messages...)
	return &d
}

// TurnInput 是一次「轮次」的输入，由 HTTP 层构造。
type TurnInput struct {
	UserID         string
	ConversationID int64  // 为 0 则创建新会话
	Message        string // 本轮用户输入
	Provider       string
	Model          string
	SystemPrompt   string
	Profile        string // AgentProfile 名称（为空取默认）
	WorkingDir     string
}

// turnState 记录一轮执行期间的中间态（从 CreateTurn 到本轮结束）。
//
// 每轮持有一个 per-conversation 锁，保证同一会话同一时刻只有一轮在执行；
// 锁在本轮到达终态（done / error / 启动失败）时释放。
type turnState struct {
	conv   *Conversation
	unlock func()

	text           strings.Builder // 累积的 assistant 文本增量
	messageContent string          // 完整 assistant 消息（text 为空时回退）
}

// ConversationService 编排多轮对话：负责历史拼接 + 复用 AgentService 执行每一轮。
type ConversationService struct {
	agent *AgentService
	repo  ConversationRepository

	mu    sync.Mutex
	locks map[int64]*sync.Mutex // conversationID → 会话级轮次锁
	turns map[int64]*turnState  // taskID → 本轮中间态
}

// NewConversationService 创建 ConversationService；repo 为空时使用内存实现。
func NewConversationService(agent *AgentService, repo ConversationRepository) *ConversationService {
	if repo == nil {
		repo = NewMemoryConversationRepository()
	}
	return &ConversationService{
		agent: agent,
		repo:  repo,
		locks: make(map[int64]*sync.Mutex),
		turns: make(map[int64]*turnState),
	}
}

// lockFor 返回会话级锁（惰性创建）。
func (s *ConversationService) lockFor(conversationID int64) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[conversationID]
	if !ok {
		l = &sync.Mutex{}
		s.locks[conversationID] = l
	}
	return l
}

// CreateTurn 准备一轮执行（不启动）：
//
//  1. 取得会话级锁（保证同一会话轮次串行）；
//  2. 加载 / 创建会话，追加 user 消息并持久化；
//  3. 用完整历史组装 Request（SessionID = 会话 ID），交给 AgentService.CreateTask；
//  4. 登记本轮中间态，返回 task 供上层（handler）在启动前订阅 WS。
//
// 锁在本轮结束时由 finishTurn 释放。
func (s *ConversationService) CreateTurn(ctx context.Context, in TurnInput) (*Task, *Conversation, error) {
	conv, err := s.getOrCreate(ctx, in)
	if err != nil {
		return nil, nil, err
	}

	l := s.lockFor(conv.ID)
	l.Lock()

	// 追加 user 消息。
	conv.Messages = append(conv.Messages, Message{Role: RoleUser, Kind: MessageKindUser, Content: in.Message})
	conv.UpdatedAt = time.Now()
	if err := s.repo.Update(ctx, conv); err != nil {
		l.Unlock()
		return nil, nil, err
	}

	// 用完整历史组装请求（快照，避免后续 assistant 回复混入本轮 prompt）。
	req := Request{
		Provider:     in.Provider,
		Model:        in.Model,
		SessionID:    strconv.FormatInt(conv.ID, 10),
		UserID:       in.UserID,
		SystemPrompt: in.SystemPrompt,
		Profile:      in.Profile,
		Messages:     promptMessagesForRequest(conv.Messages),
		WorkingDir:   in.WorkingDir,
		Stream:       true, // 会话轮次统一走流式，便于捕获 assistant 文本
	}

	task, err := s.agent.CreateTask(ctx, req)
	if err != nil {
		l.Unlock()
		return nil, nil, err
	}

	// 记录当前活跃轮次，供前端刷新 / 切换会话时恢复实时流。
	conv.CurrentTaskID = task.ID
	conv.UpdatedAt = time.Now()
	if err := s.repo.Update(ctx, conv); err != nil {
		l.Unlock()
		return nil, nil, err
	}

	s.mu.Lock()
	s.turns[task.ID] = &turnState{conv: conv, unlock: l.Unlock}
	s.mu.Unlock()

	return task, conv, nil
}

// StartTurn 启动本轮执行，并注册捕获回调：累积 assistant 文本，在本轮终态时
// 写回历史并释放会话锁。
func (s *ConversationService) StartTurn(ctx context.Context, taskID int64) error {
	s.mu.Lock()
	ts, ok := s.turns[taskID]
	s.mu.Unlock()
	if !ok {
		return ErrConversationNotFound
	}

	handler := func(_ context.Context, ev StreamEvent) error {
		switch ev.Type {
		case StreamEventText:
			ts.text.WriteString(ev.Content)
		case StreamEventMessage:
			if mb, ok := messageBlockContent(ev.Data); ok {
				ts.messageContent = mb
			}
		case StreamEventDone:
			s.finishTurn(taskID, "")
		case StreamEventError:
			s.finishTurn(taskID, "stream error")
		}
		return nil
	}

	if err := s.agent.StartTask(ctx, taskID, handler); err != nil {
		s.finishTurn(taskID, err.Error())
		return err
	}
	return nil
}

// finishTurn 在本轮结束时收尾：把 assistant 回复写回历史，释放会话锁并清理中间态。
//
// 幂等（基于 turns 中的登记），可安全处理 done / error / 启动失败任一终态。
func (s *ConversationService) finishTurn(taskID int64, errMsg string) {
	_ = errMsg

	// 幂等：先从登记表中“领取”本轮状态，保证 done / error / 启动失败 任一终态只收尾一次。
	// Provider 可能已自行补发 error 事件、run 又补发一次，若重复收尾会导致 assistant 消息
	// 重复追加、并对同一 sync.Mutex 二次 Unlock（panic）。
	s.mu.Lock()
	ts, ok := s.turns[taskID]
	if ok {
		delete(s.turns, taskID)
	}
	s.mu.Unlock()
	if !ok {
		return
	}

	final := s.appendTimelineMessages(context.Background(), ts.conv, taskID)
	if strings.TrimSpace(final) == "" {
		final = ts.text.String()
	}
	if strings.TrimSpace(final) == "" {
		final = ts.messageContent
	}
	if strings.TrimSpace(final) != "" {
		ts.conv.Messages = append(ts.conv.Messages, Message{
			Role:    RoleAssistant,
			Kind:    MessageKindAssistantFinal,
			TaskID:  taskID,
			Content: final,
		})
	}
	ts.conv.CurrentTaskID = 0
	ts.conv.UpdatedAt = time.Now()
	_ = s.repo.Update(context.Background(), ts.conv)

	ts.unlock()
}

// promptMessagesForRequest 仅挑选可用于下一轮 prompt 的语义消息。
func promptMessagesForRequest(history []Message) []Message {
	out := make([]Message, 0, len(history))
	for _, m := range history {
		if !shouldIncludeInPrompt(m) {
			continue
		}
		out = append(out, Message{Role: m.Role, Content: m.Content})
	}
	return out
}

func shouldIncludeInPrompt(m Message) bool {
	switch m.Role {
	case RoleUser:
		return true
	case RoleAssistant:
		switch m.Kind {
		case "", MessageKindAssistant, MessageKindAssistantFinal:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// appendTimelineMessages 从任务事件流投影会话历史，并返回最终 assistant 文本。
func (s *ConversationService) appendTimelineMessages(ctx context.Context, conv *Conversation, taskID int64) string {
	if conv == nil || s.agent == nil || taskID == 0 {
		return ""
	}
	events, err := s.agent.GetEvents(ctx, taskID, 0)
	if err != nil {
		return ""
	}

	var final string
	for _, ev := range events {
		if ev == nil || ev.Type != EventStream {
			continue
		}
		se, ok := streamEventFromPayload(ev.Payload)
		if !ok {
			continue
		}
		switch se.Type {
		case StreamEventReasoning:
			if content, ok := reasoningBlockContent(se.Data); ok && strings.TrimSpace(content) != "" {
				conv.Messages = append(conv.Messages, Message{
					Role:    RoleAssistant,
					Kind:    MessageKindReasoning,
					TaskID:  taskID,
					Content: content,
					Data:    se.Data,
				})
			}
		case StreamEventToolCall:
			conv.Messages = append(conv.Messages, Message{
				Role:    RoleAssistant,
				Kind:    MessageKindToolCall,
				TaskID:  taskID,
				Content: timelineEventSummary("tool", se.Data),
				Data:    se.Data,
			})
		case StreamEventToolResult:
			conv.Messages = append(conv.Messages, Message{
				Role:    RoleAssistant,
				Kind:    MessageKindToolResult,
				TaskID:  taskID,
				Content: timelineEventSummary("tool_result", se.Data),
				Data:    se.Data,
			})
		case StreamEventSkillCall:
			conv.Messages = append(conv.Messages, Message{
				Role:    RoleAssistant,
				Kind:    MessageKindSkillCall,
				TaskID:  taskID,
				Content: timelineEventSummary("skill", se.Data),
				Data:    se.Data,
			})
		case StreamEventSkillResult:
			conv.Messages = append(conv.Messages, Message{
				Role:    RoleAssistant,
				Kind:    MessageKindSkillResult,
				TaskID:  taskID,
				Content: timelineEventSummary("skill_result", se.Data),
				Data:    se.Data,
			})
		case StreamEventMessage:
			if content, ok := messageBlockContent(se.Data); ok && strings.TrimSpace(content) != "" {
				final = content
			}
		}
	}
	return final
}

func streamEventFromPayload(payload any) (StreamEvent, bool) {
	switch p := payload.(type) {
	case StreamEvent:
		return p, true
	case *StreamEvent:
		if p == nil {
			return StreamEvent{}, false
		}
		return *p, true
	case map[string]any:
		t, ok := p["type"].(string)
		if !ok || strings.TrimSpace(t) == "" {
			return StreamEvent{}, false
		}
		e := StreamEvent{Type: StreamEventType(t)}
		if content, ok := p["content"].(string); ok {
			e.Content = content
		}
		e.Data = p["data"]
		return e, true
	default:
		return StreamEvent{}, false
	}
}

func reasoningBlockContent(data any) (string, bool) {
	switch v := data.(type) {
	case ReasoningBlock:
		return v.Content, true
	case *ReasoningBlock:
		if v == nil {
			return "", false
		}
		return v.Content, true
	case map[string]any:
		content, ok := v["content"].(string)
		return content, ok
	default:
		return "", false
	}
}

func timelineEventSummary(prefix string, data any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Sprintf("%s event", prefix)
	}
	return string(b)
}

// PageConversations 分页查询会话；userID 为空表示全部用户。
func (s *ConversationService) PageConversations(ctx context.Context, userID string, offset, limit int) ([]*Conversation, int64, error) {
	return s.repo.Page(ctx, userID, offset, limit)
}

// GetConversation 查询会话（含完整历史消息）。
func (s *ConversationService) GetConversation(ctx context.Context, id int64) (*Conversation, error) {
	return s.repo.Get(ctx, id)
}

// getOrCreate 加载已有会话；ConversationID 为 0 时创建新会话。
func (s *ConversationService) getOrCreate(ctx context.Context, in TurnInput) (*Conversation, error) {
	if in.ConversationID == 0 {
		conv := NewConversation(in.UserID, in.Provider, in.Model)
		if err := s.repo.Create(ctx, conv); err != nil {
			return nil, err
		}
		return conv, nil
	}
	return s.repo.Get(ctx, in.ConversationID)
}

// messageBlockContent 从 StreamEventMessage 的 Data 中提取完整 assistant 文本。
func messageBlockContent(data any) (string, bool) {
	switch mb := data.(type) {
	case MessageBlock:
		return mb.Content, true
	case *MessageBlock:
		if mb == nil {
			return "", false
		}
		return mb.Content, true
	case map[string]any:
		content, ok := mb["content"].(string)
		return content, ok
	default:
		return "", false
	}
}
