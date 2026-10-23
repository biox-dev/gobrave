package providers

import (
	"context"
	"strings"

	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	"github.com/biox-dev/gobrave/internal/agent/native"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// subAgentSessionStore 把子 Agent 的会话与消息写入 agent_sessions / agent_messages。
//
// 它是 native.SubAgentSessionStore 的数据库实现：
//   - 子会话的 ParentSessionID 指向父会话，便于按父会话追溯；
//   - 消息 kind 与 ChatService 对父会话的投影规则保持一致
//     （reasoning / tool_call / tool_result / assistant_final）。
type subAgentSessionStore struct {
	sessions agentinterfaces.SessionService
	messages agentinterfaces.MessageService
}

// newSubAgentSessionStore 构建子 Agent 持久化适配器；任一服务缺失时返回 nil。
//
// SessionStore 现在是子 Agent 的必需依赖（子 Agent 始终要落一个完整会话），
// 因此返回 nil 会让子 Agent 执行失败，而不是静默降级为无会话运行。
func newSubAgentSessionStore(sessions agentinterfaces.SessionService, messages agentinterfaces.MessageService) native.SubAgentSessionStore {
	if sessions == nil || messages == nil {
		return nil
	}
	return subAgentSessionStore{sessions: sessions, messages: messages}
}

// CreateChildSession 实现 native.SubAgentSessionStore。
func (s subAgentSessionStore) CreateChildSession(ctx context.Context, info native.SubAgentSessionInfo) (int64, error) {
	sess, err := s.sessions.CreateChildSession(ctx, agenttypes.SessionChildParams{
		ProjectID:       info.ProjectID,
		ParentSessionID: info.ParentSessionID,
		Title:           info.Title,
		UserID:          info.UserID,
		Provider:        info.Provider,
		Model:           info.Model,
	})
	if err != nil {
		return 0, err
	}
	return sess.ID, nil
}

// CreateSession 实现 native.SubAgentSessionStore：无父会话时创建独立会话。
func (s subAgentSessionStore) CreateSession(ctx context.Context, info native.SubAgentSessionInfo) (int64, error) {
	sess, err := s.sessions.Create(ctx, agenttypes.SessionCreateParams{
		ProjectID: info.ProjectID,
		Title:     info.Title,
		UserID:    info.UserID,
		Provider:  info.Provider,
		Model:     info.Model,
	})
	if err != nil {
		return 0, err
	}
	return sess.ID, nil
}

// AppendUserMessage 实现 native.SubAgentSessionStore。
func (s subAgentSessionStore) AppendUserMessage(ctx context.Context, sessionID int64, content string) error {
	return s.append(ctx, sessionID, agenttypes.RoleUser, agenttypes.KindUser, content, nil)
}

// AppendEvent 实现 native.SubAgentSessionStore：复用父会话同一条投影规则。
func (s subAgentSessionStore) AppendEvent(ctx context.Context, sessionID int64, event agenttypes.StreamEvent) error {
	params, ok := agentservice.ProjectTimelineEvent(sessionID, 0, event)
	if !ok {
		return nil
	}
	_, err := s.messages.Create(ctx, params)
	return err
}

// AppendFinalMessage 实现 native.SubAgentSessionStore。
func (s subAgentSessionStore) AppendFinalMessage(ctx context.Context, sessionID int64, content string) error {
	if content = strings.TrimSpace(content); content == "" {
		return nil
	}
	return s.append(ctx, sessionID, agenttypes.RoleAssistant, agenttypes.KindAssistantFinal, content, nil)
}

func (s subAgentSessionStore) append(ctx context.Context, sessionID int64, role, kind, content string, data any) error {
	_, err := s.messages.Create(ctx, agenttypes.MessageCreateParams{
		SessionID: sessionID,
		Role:      role,
		Kind:      kind,
		Content:   content,
		Data:      data,
	})
	return err
}
