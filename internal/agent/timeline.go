package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/message"
)

// 本文件是「流式事件 → 会话消息」的唯一投影规则。
//
// 父会话（ChatService 从任务事件流回放）与子 Agent 会话（执行期实时记录）共用它，
// 避免两套规则漂移：reasoning / tool_call / tool_result 产生消息，其余事件（增量、
// message 块、done/error 等）不产生消息——最终回答由调用方单独写 assistant_final。

// ProjectTimelineEvent 把一条流式完整块事件投影为可持久化的会话消息。
//
// sessionID / taskID 写入消息归属；taskID 为 0 表示不属于 agent_tasks（如子 Agent 会话）。
// 返回 ok=false 表示该事件不应落库。
func ProjectTimelineEvent(sessionID, taskID int64, event StreamEvent) (message.CreateParams, bool) {
	switch event.Type {
	case StreamEventReasoning:
		content, ok := ReasoningBlockContent(event.Data)
		if !ok || strings.TrimSpace(content) == "" {
			return message.CreateParams{}, false
		}
		return message.CreateParams{
			SessionID: sessionID,
			Role:      message.RoleAssistant,
			Kind:      message.KindReasoning,
			TaskID:    taskID,
			Content:   content,
			Data:      event.Data,
		}, true
	case StreamEventToolCall:
		return message.CreateParams{
			SessionID: sessionID,
			Role:      message.RoleAssistant,
			Kind:      message.KindToolCall,
			TaskID:    taskID,
			Content:   TimelineEventSummary("tool", event.Data),
			Data:      event.Data,
		}, true
	case StreamEventToolResult:
		return message.CreateParams{
			SessionID: sessionID,
			Role:      message.RoleAssistant,
			Kind:      message.KindToolResult,
			TaskID:    taskID,
			Content:   TimelineEventSummary("tool_result", event.Data),
			Data:      event.Data,
		}, true
	default:
		return message.CreateParams{}, false
	}
}

// ReasoningBlockContent 从 reasoning 事件体中提取完整思考文本。
func ReasoningBlockContent(data any) (string, bool) {
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

// TimelineEventSummary 把事件体序列化为消息正文（序列化失败时退化为 "<prefix> event"）。
func TimelineEventSummary(prefix string, data any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Sprintf("%s event", prefix)
	}
	return string(b)
}
