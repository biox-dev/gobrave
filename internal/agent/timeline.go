package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/biox-dev/gobrave/internal/agent/message"
	"github.com/biox-dev/gobrave/internal/logger"
)

// 本文件是「流式事件 → 会话消息」的唯一投影规则。
//
// TimelineRecorder 在轮内即时写入父会话时间线，子 Agent 会话（执行期实时记录）共用同一规则，
// 避免两套规则漂移：reasoning / message / tool_call / tool_result 产生消息，其余事件
// （增量、turn_*、done/error 等）不产生消息——user 消息与轮末的 error 由 ChatService 写入。

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
	case StreamEventMessage:
		// assistant 文本块：内容为空（只发起工具调用的消息）时不产生时间线消息。
		content, ok := MessageBlockContent(event.Data)
		if !ok || strings.TrimSpace(content) == "" {
			return message.CreateParams{}, false
		}
		return message.CreateParams{
			SessionID: sessionID,
			Role:      message.RoleAssistant,
			Kind:      message.KindAssistant,
			TaskID:    taskID,
			Content:   content,
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

// MessageBlockContent 从 StreamEventMessage 的 Data 中提取完整 assistant 文本。
//
// 投影与轮末兜底共用它，保证「什么算一条 assistant 文本」只有一个口径。
func MessageBlockContent(data any) (string, bool) {
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

// TimelineEventSummary 把事件体序列化为消息正文（序列化失败时退化为 "<prefix> event"）。
func TimelineEventSummary(prefix string, data any) string {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Sprintf("%s event", prefix)
	}
	return string(b)
}

// TimelineRecorder 把流式完整块事件即时投影并写入 agent_messages。
//
// 它是「事件 → 会话消息」的唯一写入入口，取代原先「轮末从 AgentEvent 回放事件」的做法：
//   - 轮内即时落库：Agent 循环中途退出（进程重启 / panic / 长时间等待权限）时，
//     已经产生的时间线不会丢，为后续恢复留下事实；
//   - 投影规则复用 ProjectTimelineEvent，与子 Agent 会话保持同一口径；
//   - 只有 EventStream 里可投影的完整块（reasoning / message / tool_call / tool_result）产生消息；
//     增量（text / reasoning_delta）与生命周期信号（turn_start / done / error ...）不落库；
//   - Message.Data 保留事件原始结构，后续可从消息表重建结构化工具调用；
//   - RecordResult 是「结果兜底」：Provider 没发完整 message 块时，用最终结果补一条
//     assistant_final，保证刷新 / 恢复后回答不会丢。
//
// 不属于本组件职责的两类消息（它们不是事件流的产物，由 ChatService 直接写入）：
//   - user 消息：CreateTurn 在启动任务前写入；
//   - assistant 最终回答 / error：finishTurn 在轮末写入。
//
// nil receiver 安全：未注入 message.Service 时所有方法都是空操作。
type TimelineRecorder struct {
	tasks    TaskRepository
	messages message.Service

	mu       sync.RWMutex
	sessions map[int64]int64 // taskID → sessionID（避免每个事件都查一次任务）
	// sawAssistant 记录该任务是否已落过 assistant 文本块（KindAssistant），
	// 供 RecordResult 判断是否还需要用最终结果补一条 assistant_final。
	sawAssistant map[int64]bool
}

// NewTimelineRecorder 创建时间线记录器；messages 为 nil 时返回 nil（等价于关闭落库）。
func NewTimelineRecorder(tasks TaskRepository, messages message.Service) *TimelineRecorder {
	if messages == nil {
		return nil
	}
	return &TimelineRecorder{
		tasks:        tasks,
		messages:     messages,
		sessions:     make(map[int64]int64),
		sawAssistant: make(map[int64]bool),
	}
}

// Remember 记录任务归属的会话，供后续事件投影时直接取用。
//
// 在 CreateTask / StartTask / RunTaskSyncByID 拿到 Task 时调用，热点路径（Emit）
// 因此不需要再查一次任务。
func (r *TimelineRecorder) Remember(taskID, sessionID int64) {
	if r == nil || taskID == 0 {
		return
	}
	r.mu.Lock()
	r.sessions[taskID] = sessionID
	r.mu.Unlock()
}

// Forget 在任务进入终态后清理会话缓存，避免长期运行下无限增长。
func (r *TimelineRecorder) Forget(taskID int64) {
	if r == nil || taskID == 0 {
		return
	}
	r.mu.Lock()
	delete(r.sessions, taskID)
	delete(r.sawAssistant, taskID)
	r.mu.Unlock()
}

// Record 把一条事件 payload 投影为会话消息并落库；不可投影时静默跳过。
//
// 这是 Emit 的热点路径：只处理完整块事件，增量事件在 publish 侧就不会进来；
// 落库失败只记 Warn，绝不影响 Agent 主流程。
func (r *TimelineRecorder) Record(ctx context.Context, taskID int64, payload any) {
	if r == nil || r.messages == nil || taskID == 0 {
		return
	}
	event, ok := streamEventFromPayload(payload)
	if !ok {
		return
	}

	// Emit 的 ctx 可能是 Provider 的 trace ctx / 工具执行 ctx，工具返回后即被取消；
	// 落库是「事后必须完成」的动作，这里解绑取消信号，避免消息静默丢失。
	persistCtx := context.WithoutCancel(ctx)

	sessionID, err := r.sessionID(persistCtx, taskID)
	if err != nil || sessionID <= 0 {
		// 无会话归属的任务（如 AI 摘要等后台任务）没有 agent_messages 载体。
		return
	}
	params, ok := ProjectTimelineEvent(sessionID, taskID, event)
	if !ok {
		return
	}
	if _, err := r.messages.Create(persistCtx, params); err != nil {
		logger.Warnf(persistCtx, "[agent] persist timeline message failed: task=%d kind=%s err=%v",
			taskID, params.Kind, err)
		return
	}
	if params.Kind == message.KindAssistant {
		r.mu.Lock()
		r.sawAssistant[taskID] = true
		r.mu.Unlock()
	}
}

// RecordResult 是「Provider 没有发出完整 message 块」时的兜底：用最终结果补一条
// assistant_final，保证刷新 / 恢复后回答不会丢。
//
// 已经有 KindAssistant 块（Record 落过）时直接跳过，避免历史里出现两份相同文本。
// 必须在任务进入终态（Forget 清理缓存）之前调用。
func (r *TimelineRecorder) RecordResult(ctx context.Context, taskID int64, content string) {
	if r == nil || r.messages == nil || taskID == 0 {
		return
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}

	r.mu.RLock()
	saw := r.sawAssistant[taskID]
	r.mu.RUnlock()
	if saw {
		return
	}

	persistCtx := context.WithoutCancel(ctx)
	sessionID, err := r.sessionID(persistCtx, taskID)
	if err != nil || sessionID <= 0 {
		return
	}
	if _, err := r.messages.Create(persistCtx, message.CreateParams{
		SessionID: sessionID,
		Role:      message.RoleAssistant,
		Kind:      message.KindAssistantFinal,
		TaskID:    taskID,
		Content:   content,
	}); err != nil {
		logger.Warnf(persistCtx, "[agent] persist assistant_final failed: task=%d err=%v", taskID, err)
	}
}

// sessionID 返回任务所属会话（先查缓存，缺失时回查任务并缓存）。
func (r *TimelineRecorder) sessionID(ctx context.Context, taskID int64) (int64, error) {
	r.mu.RLock()
	sessionID, ok := r.sessions[taskID]
	r.mu.RUnlock()
	if ok {
		return sessionID, nil
	}
	if r.tasks == nil {
		return 0, nil
	}
	task, err := r.tasks.Get(ctx, taskID)
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	r.sessions[taskID] = task.SessionID
	r.mu.Unlock()
	return task.SessionID, nil
}

// streamEventFromPayload 把 publish 的 payload 还原为 StreamEvent。
//
// payload 可能是内存态（StreamEvent）也可能是事件落库再读出的通用 map，
// 因此两种形态都要兼容。
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
