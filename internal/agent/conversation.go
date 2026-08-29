package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
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
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Provider  string    `json:"provider"`
	Model     string    `json:"model"`
	Messages  []Message `json:"messages"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewConversation 创建一个空的会话。
func NewConversation(userID, provider, model string) *Conversation {
	now := time.Now()
	return &Conversation{
		ID:        newID("conv"),
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
	Get(ctx context.Context, id string) (*Conversation, error)
	Create(ctx context.Context, conv *Conversation) error
	Update(ctx context.Context, conv *Conversation) error
}

// NewMemoryConversationRepository 创建会话的内存实现。
func NewMemoryConversationRepository() ConversationRepository {
	return &memoryConversationRepository{convs: make(map[string]*Conversation)}
}

type memoryConversationRepository struct {
	mu    sync.RWMutex
	convs map[string]*Conversation
}

func (r *memoryConversationRepository) Get(_ context.Context, id string) (*Conversation, error) {
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
	ConversationID string // 为空则创建新会话
	Message        string // 本轮用户输入
	Provider       string
	Model          string
	SystemPrompt   string
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
	hasText        bool
}

// ConversationService 编排多轮对话：负责历史拼接 + 复用 AgentService 执行每一轮。
type ConversationService struct {
	agent *AgentService
	repo  ConversationRepository

	mu    sync.Mutex
	locks map[string]*sync.Mutex // conversationID → 会话级轮次锁
	turns map[string]*turnState  // taskID → 本轮中间态
}

// NewConversationService 创建 ConversationService；repo 为空时使用内存实现。
func NewConversationService(agent *AgentService, repo ConversationRepository) *ConversationService {
	if repo == nil {
		repo = NewMemoryConversationRepository()
	}
	return &ConversationService{
		agent: agent,
		repo:  repo,
		locks: make(map[string]*sync.Mutex),
		turns: make(map[string]*turnState),
	}
}

// lockFor 返回会话级锁（惰性创建）。
func (s *ConversationService) lockFor(conversationID string) *sync.Mutex {
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
	conv.Messages = append(conv.Messages, Message{Role: RoleUser, Content: in.Message})
	conv.UpdatedAt = time.Now()
	if err := s.repo.Update(ctx, conv); err != nil {
		l.Unlock()
		return nil, nil, err
	}

	// 用完整历史组装请求（快照，避免后续 assistant 回复混入本轮 prompt）。
	req := Request{
		Provider:     in.Provider,
		Model:        in.Model,
		SessionID:    conv.ID,
		SystemPrompt: in.SystemPrompt,
		Messages:     append([]Message(nil), conv.Messages...),
		WorkingDir:   in.WorkingDir,
		Stream:       true, // 会话轮次统一走流式，便于捕获 assistant 文本
	}

	task, err := s.agent.CreateTask(ctx, req)
	if err != nil {
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
func (s *ConversationService) StartTurn(ctx context.Context, taskID string) error {
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
			ts.hasText = true
		case StreamEventMessage:
			if mb, ok := messageBlockContent(ev.Data); ok {
				ts.messageContent = mb
			}
		case StreamEventDone:
			s.finishTurn(taskID, ts, "")
		case StreamEventError:
			s.finishTurn(taskID, ts, "stream error")
		}
		return nil
	}

	if err := s.agent.StartTask(ctx, taskID, handler); err != nil {
		s.finishTurn(taskID, ts, err.Error())
		return err
	}
	return nil
}

// finishTurn 在本轮结束时收尾：把 assistant 回复写回历史，释放会话锁并清理中间态。
//
// 幂等（基于 turns 中的登记），可安全处理 done / error / 启动失败任一终态。
func (s *ConversationService) finishTurn(taskID string, ts *turnState, errMsg string) {
	content := ts.text.String()
	if content == "" {
		content = ts.messageContent
	}
	if content != "" {
		ts.conv.Messages = append(ts.conv.Messages, Message{Role: RoleAssistant, Content: content})
	}
	ts.conv.UpdatedAt = time.Now()
	_ = s.repo.Update(context.Background(), ts.conv)

	s.mu.Lock()
	delete(s.turns, taskID)
	s.mu.Unlock()
	ts.unlock()
}

// getOrCreate 加载已有会话；ConversationID 为空时创建新会话。
func (s *ConversationService) getOrCreate(ctx context.Context, in TurnInput) (*Conversation, error) {
	if strings.TrimSpace(in.ConversationID) == "" {
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
	default:
		return "", false
	}
}
