package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/biox-dev/gobrave/internal/agent/message"
	"github.com/biox-dev/gobrave/internal/agent/session"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// 本文件定义「多轮对话」编排层：把一次会话(Session)与一轮执行(Task)拆成两层。
//
//   - Session / Message：跨轮次持久化的会话与历史消息，由 session.Service /
//     message.Service 负责增删改查（参考 opencode 的分层）；
//   - Task：一轮执行，复用 AgentService 既有的 created→running→waiting→completed 状态机。
//
// ChatService 只负责「历史拼接」与「轮次编排」，把每一轮都当作一次普通的 Request
// 交给 AgentService 执行；AgentService 完全感知不到「多轮」这个概念。

// TurnInput 是一次「轮次」的输入，由 HTTP 层构造。
type TurnInput struct {
	UserID    int64
	SessionID int64  // 为 0 则创建新会话
	Message   string // 本轮用户输入
	Provider  string
	Model     string
	// RuntimeContext 是本轮调用的运行时上下文（业务类型 / 变量 / 工作目录）。
	RuntimeContext *tool.RuntimeContext
	ProfileID      int64 // AgentProfile ID（为 0 取默认）
	WorkingDir     string
}

// turnState 记录一轮执行期间的中间态（从 CreateTurn 到本轮结束）。
//
// 每轮持有一个 per-session 锁，保证同一会话同一时刻只有一轮在执行；
// 锁在本轮到达终态（done / error / 启动失败）时释放。
type turnState struct {
	sessionID int64
	unlock    func()

	text           strings.Builder // 累积的 assistant 文本增量
	messageContent string          // 完整 assistant 消息（text 为空时回退）
}

// ChatService 编排多轮对话：负责历史拼接 + 复用 AgentService 执行每一轮。
type ChatService struct {
	agent    *AgentService
	sessions session.Service
	messages message.Service

	mu    sync.Mutex
	locks map[int64]*sync.Mutex // sessionID → 会话级轮次锁
	turns map[int64]*turnState  // taskID → 本轮中间态
}

// NewChatService 创建 ChatService。
func NewChatService(agent *AgentService, sessions session.Service, messages message.Service) *ChatService {
	return &ChatService{
		agent:    agent,
		sessions: sessions,
		messages: messages,
		locks:    make(map[int64]*sync.Mutex),
		turns:    make(map[int64]*turnState),
	}
}

// lockFor 返回会话级锁（惰性创建）。
func (s *ChatService) lockFor(sessionID int64) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.locks[sessionID]
	if !ok {
		l = &sync.Mutex{}
		s.locks[sessionID] = l
	}
	return l
}

// CreateTurn 准备一轮执行（不启动）：
//
//  1. 取得会话级锁（保证同一会话轮次串行）；
//  2. 加载 / 创建会话，追加 user 消息并持久化；
//  3. 读取完整历史组装 Request（SessionID = 会话 ID），交给 AgentService.CreateTask；
//  4. 登记本轮中间态，返回 task 供上层（handler）在启动前订阅 WS。
//
// 锁在本轮结束时由 finishTurn 释放。
func (s *ChatService) CreateTurn(ctx context.Context, in TurnInput) (*Task, session.Session, error) {
	sess, err := s.getOrCreate(ctx, in)
	if err != nil {
		return nil, session.Session{}, err
	}

	l := s.lockFor(sess.ID)
	l.Lock()

	// 追加 user 消息。
	if _, err := s.messages.Create(ctx, message.CreateParams{
		SessionID: sess.ID,
		Role:      message.RoleUser,
		Kind:      message.KindUser,
		Content:   in.Message,
	}); err != nil {
		l.Unlock()
		return nil, session.Session{}, err
	}

	// 读取完整历史组装请求（快照，避免后续 assistant 回复混入本轮 prompt）。
	history, err := s.messages.List(ctx, sess.ID)
	if err != nil {
		l.Unlock()
		return nil, session.Session{}, err
	}

	// 用户 / 会话事实统一由 RuntimeContext 承载：这里把会话 ID 与该轮用户写入上下文，
	// 使权限策略、ask_user 归属与记忆检索都能从同一处读取。
	rc := withRequestFacts(in.RuntimeContext, in.UserID, sess.ID)
	req := Request{
		Provider:       in.Provider,
		ModelID:        in.Model,
		SessionID:      sess.ID,
		RuntimeContext: rc,
		ProfileID:      in.ProfileID,
		Messages:       promptMessagesForRequest(history),
		WorkingDir:     in.WorkingDir,
		Stream:         true, // 会话轮次统一走流式，便于捕获 assistant 文本
	}

	task, err := s.agent.CreateTask(ctx, req)
	if err != nil {
		l.Unlock()
		return nil, session.Session{}, err
	}

	// 记录当前活跃轮次，供前端刷新 / 切换会话时恢复实时流。
	sess.CurrentTaskID = task.ID
	sess, err = s.sessions.Save(ctx, sess)
	if err != nil {
		l.Unlock()
		return nil, session.Session{}, err
	}

	s.mu.Lock()
	s.turns[task.ID] = &turnState{sessionID: sess.ID, unlock: l.Unlock}
	s.mu.Unlock()

	return task, sess, nil
}

// StartTurn 启动本轮执行，并注册捕获回调：累积 assistant 文本，在本轮终态时
// 写回历史并释放会话锁。
func (s *ChatService) StartTurn(ctx context.Context, taskID int64) error {
	s.mu.Lock()
	ts, ok := s.turns[taskID]
	s.mu.Unlock()
	if !ok {
		return session.ErrNotFound
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
func (s *ChatService) finishTurn(taskID int64, errMsg string) {
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

	ctx := context.Background()
	final := s.appendTimelineMessages(ctx, ts.sessionID, taskID)
	if strings.TrimSpace(final) == "" {
		final = ts.text.String()
	}
	if strings.TrimSpace(final) == "" {
		final = ts.messageContent
	}
	if strings.TrimSpace(final) != "" {
		_, _ = s.messages.Create(ctx, message.CreateParams{
			SessionID: ts.sessionID,
			Role:      message.RoleAssistant,
			Kind:      message.KindAssistantFinal,
			TaskID:    taskID,
			Content:   final,
		})
	}

	// 清理当前活跃轮次（会话可能已被删除，忽略错误）。
	if sess, err := s.sessions.Get(ctx, ts.sessionID); err == nil {
		sess.CurrentTaskID = 0
		_, _ = s.sessions.Save(ctx, sess)
	}

	ts.unlock()
}

// promptMessagesForRequest 仅挑选可用于下一轮 prompt 的语义消息。
func promptMessagesForRequest(history []message.Message) []message.Message {
	out := make([]message.Message, 0, len(history))
	for _, m := range history {
		if !shouldIncludeInPrompt(m) {
			continue
		}
		out = append(out, message.Message{Role: m.Role, Content: m.Content})
	}
	return out
}

func shouldIncludeInPrompt(m message.Message) bool {
	switch m.Role {
	case message.RoleUser:
		return true
	case message.RoleAssistant:
		switch m.Kind {
		case "", message.KindAssistant, message.KindAssistantFinal, message.KindToolCall, message.KindToolResult:
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// appendTimelineMessages 从任务事件流投影会话历史，并返回最终 assistant 文本。
func (s *ChatService) appendTimelineMessages(ctx context.Context, sessionID, taskID int64) string {
	if s.agent == nil || sessionID == 0 || taskID == 0 {
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
				s.appendMessage(ctx, message.CreateParams{
					SessionID: sessionID,
					Role:      message.RoleAssistant,
					Kind:      message.KindReasoning,
					TaskID:    taskID,
					Content:   content,
					Data:      se.Data,
				})
			}
		case StreamEventToolCall:
			s.appendMessage(ctx, message.CreateParams{
				SessionID: sessionID,
				Role:      message.RoleAssistant,
				Kind:      message.KindToolCall,
				TaskID:    taskID,
				Content:   timelineEventSummary("tool", se.Data),
				Data:      se.Data,
			})
		case StreamEventToolResult:
			s.appendMessage(ctx, message.CreateParams{
				SessionID: sessionID,
				Role:      message.RoleAssistant,
				Kind:      message.KindToolResult,
				TaskID:    taskID,
				Content:   timelineEventSummary("tool_result", se.Data),
				Data:      se.Data,
			})
		case StreamEventMessage:
			if content, ok := messageBlockContent(se.Data); ok && strings.TrimSpace(content) != "" {
				final = content
			}
		}
	}
	return final
}

func (s *ChatService) appendMessage(ctx context.Context, params message.CreateParams) {
	_, _ = s.messages.Create(ctx, params)
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

// ListSessions 分页查询会话；userID 为 0 表示全部用户。
func (s *ChatService) ListSessions(ctx context.Context, userID int64, offset, limit int) ([]session.Session, int64, error) {
	return s.sessions.List(ctx, session.ListParams{UserID: userID, Offset: offset, Limit: limit})
}

// ListMessages 查询某个会话的全部历史消息（按 Seq 升序）。
func (s *ChatService) ListMessages(ctx context.Context, sessionID int64) ([]message.Message, error) {
	return s.messages.List(ctx, sessionID)
}

// GetSession 查询会话及其完整历史消息。
func (s *ChatService) GetSession(ctx context.Context, id int64) (session.Session, []message.Message, error) {
	sess, err := s.sessions.Get(ctx, id)
	if err != nil {
		return session.Session{}, nil, err
	}
	msgs, err := s.messages.List(ctx, id)
	if err != nil {
		return session.Session{}, nil, err
	}
	return sess, msgs, nil
}

// getOrCreate 加载已有会话；SessionID 为 0 时创建新会话。
func (s *ChatService) getOrCreate(ctx context.Context, in TurnInput) (session.Session, error) {
	if in.SessionID == 0 {
		return s.sessions.Create(ctx, session.CreateParams{
			UserID:   in.UserID,
			Provider: in.Provider,
			Model:    in.Model,
		})
	}
	return s.sessions.Get(ctx, in.SessionID)
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
