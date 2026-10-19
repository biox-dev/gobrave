package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/message"
	"github.com/biox-dev/gobrave/internal/agent/native"
	"github.com/biox-dev/gobrave/internal/agent/session"
)

// subAgentSessionStore 把子 Agent 的会话与消息写入 agent_sessions / agent_messages。
//
// 它是 native.SubAgentSessionStore 的数据库实现：
//   - 子会话的 ParentSessionID 指向父会话，便于按父会话追溯；
//   - 消息 kind 与 ChatService 对父会话的投影规则保持一致
//     （reasoning / tool_call / tool_result / assistant_final）。
type subAgentSessionStore struct {
	sessions session.Service
	messages message.Service
}

// newSubAgentSessionStore 构建子 Agent 持久化适配器；任一服务缺失时返回 nil（不落库）。
func newSubAgentSessionStore(sessions session.Service, messages message.Service) native.SubAgentSessionStore {
	if sessions == nil || messages == nil {
		return nil
	}
	return subAgentSessionStore{sessions: sessions, messages: messages}
}

// CreateChildSession 实现 native.SubAgentSessionStore。
func (s subAgentSessionStore) CreateChildSession(ctx context.Context, info native.SubAgentSessionInfo) (int64, error) {
	sess, err := s.sessions.Create(ctx, session.CreateParams{
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

// AppendUserMessage 实现 native.SubAgentSessionStore。
func (s subAgentSessionStore) AppendUserMessage(ctx context.Context, sessionID int64, content string) error {
	_, err := s.messages.Create(ctx, message.CreateParams{
		SessionID: sessionID,
		Role:      message.RoleUser,
		Kind:      message.KindUser,
		Content:   content,
	})
	return err
}

// AppendReasoning 实现 native.SubAgentSessionStore。
func (s subAgentSessionStore) AppendReasoning(ctx context.Context, sessionID int64, content string, data any) error {
	if content = strings.TrimSpace(content); content == "" {
		return nil
	}
	return s.append(ctx, sessionID, message.RoleAssistant, message.KindReasoning, content, data)
}

// AppendToolCall 实现 native.SubAgentSessionStore。
func (s subAgentSessionStore) AppendToolCall(ctx context.Context, sessionID int64, data any) error {
	return s.append(ctx, sessionID, message.RoleAssistant, message.KindToolCall, subAgentEventSummary("tool", data), data)
}

// AppendToolResult 实现 native.SubAgentSessionStore。
func (s subAgentSessionStore) AppendToolResult(ctx context.Context, sessionID int64, data any) error {
	return s.append(ctx, sessionID, message.RoleAssistant, message.KindToolResult, subAgentEventSummary("tool_result", data), data)
}

// AppendFinalMessage 实现 native.SubAgentSessionStore。
func (s subAgentSessionStore) AppendFinalMessage(ctx context.Context, sessionID int64, content string) error {
	if content = strings.TrimSpace(content); content == "" {
		return nil
	}
	return s.append(ctx, sessionID, message.RoleAssistant, message.KindAssistantFinal, content, nil)
}

func (s subAgentSessionStore) append(ctx context.Context, sessionID int64, role, kind, content string, data any) error {
	_, err := s.messages.Create(ctx, message.CreateParams{
		SessionID: sessionID,
		Role:      role,
		Kind:      kind,
		Content:   content,
		Data:      data,
	})
	return err
}

// subAgentEventSummary 把工具调用 / 结果事件序列化为消息正文（与 ChatService 一致）。
func subAgentEventSummary(prefix string, data any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Sprintf("%s event", prefix)
	}
	return string(b)
}
