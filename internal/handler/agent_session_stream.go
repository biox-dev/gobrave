package handler

import (
	"context"
	"net/http"
	"sync"

	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/errors"
	"github.com/biox-dev/gobrave/internal/realtime"
	"github.com/gin-gonic/gin"
)

// sessionEventBridge 把 Agent 实时事件按「会话」路由到已订阅用户的 WS 连接。
//
// 为什么订阅键是 SessionID 而不是 TaskID：
//   - 订阅的生命周期是「用户正在看这个会话页」，而不是某一轮任务；
//   - 一个会话包含多轮任务（每轮都是新的 TaskID），页面无法预知后续 TaskID；
//   - 事件本身已经带 SessionID，用它做路由键可以让一次订阅覆盖该会话的所有轮次。
//
// 订阅关系只存在内存中：它描述的是「谁正在看这个会话」，不是持久状态。
// 订阅生命周期只有两个撤销入口：页面离开时的显式退订，以及任务进入终态
// （会话结束）时的批量清理；单次推送失败不会改变订阅关系。
type sessionEventBridge struct {
	hub *realtime.Hub

	mu   sync.RWMutex
	subs map[int64]map[int64]struct{} // sessionID → 订阅用户集合
}

func newSessionEventBridge(hub *realtime.Hub) *sessionEventBridge {
	return &sessionEventBridge{
		hub:  hub,
		subs: make(map[int64]map[int64]struct{}),
	}
}

// subscribe 登记「用户 userID 正在看会话 sessionID」（幂等）。
func (b *sessionEventBridge) subscribe(sessionID, userID int64) {
	if sessionID <= 0 || userID <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	users := b.subs[sessionID]
	if users == nil {
		users = make(map[int64]struct{}, 1)
		b.subs[sessionID] = users
	}
	users[userID] = struct{}{}
}

// unsubscribe 撤销单个用户对会话的订阅；会话没有订阅者时删除整条记录。
func (b *sessionEventBridge) unsubscribe(sessionID, userID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	users, ok := b.subs[sessionID]
	if !ok {
		return
	}
	delete(users, userID)
	if len(users) == 0 {
		delete(b.subs, sessionID)
	}
}

// dropSession 取消会话下所有用户的订阅（会话本轮结束）。
func (b *sessionEventBridge) dropSession(sessionID int64) {
	if sessionID <= 0 {
		return
	}
	b.mu.Lock()
	delete(b.subs, sessionID)
	b.mu.Unlock()
}

// dispatch 是事件桥入口：只推给该会话的订阅者。
//
// 没有任何订阅者时直接丢弃（这正是「不再全局按 UserID 推送」的核心差异）。
// 推送失败不代表用户不再观看：连接抖动 / 短暂离线很常见，而页面只在进入时订阅
// 一次，这里若移除订阅，用户会永久收不到后续事件（且无法自我恢复）。
func (b *sessionEventBridge) dispatch(_ context.Context, ev agenttypes.AgentEvent) {
	// 未绑定会话的任务（如 /agent/task/create 创建的一次性任务）没有可订阅的会话，
	// 仍按事件归属用户直推，保持原有的一次性任务实时行为。
	if ev.SessionID <= 0 {
		if b.hub != nil && ev.UserID > 0 {
			_ = b.hub.PushMessage(ev.UserID, agentWSEvent{Type: "agent.event", TaskID: ev.TaskID, Event: ev})
		}
		return
	}

	users := b.subscribers(ev.SessionID)
	if len(users) == 0 {
		return
	}

	if b.hub != nil {
		msg := agentWSEvent{Type: "agent.event", TaskID: ev.TaskID, Event: ev}
		for _, userID := range users {
			// 推送失败只丢弃这一帧（hub 内部已记录日志），不动订阅关系：
			// 页面仍然打开、只是 WS 正在重连时，退订会造成实时流永久静默。
			// 死连接留下的订阅由会话结束时的 dropSession 统一清理。
			_ = b.hub.PushMessage(userID, msg)
		}
	}

	// 本轮任务结束 = 会话结束：取消该会话下所有用户订阅。
	// 事件本身已经先推送出去（task.completed / failed / canceled 是最后一帧）；
	// 发起新一轮时 /agent/chat 会把发起人重新登记为订阅者。
	if isSessionEndEvent(ev.Type) {
		b.dropSession(ev.SessionID)
	}
}

// subscribers 返回会话订阅者的快照（在锁外推送，避免同步分发时持锁）。
func (b *sessionEventBridge) subscribers(sessionID int64) []int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	users := b.subs[sessionID]
	out := make([]int64, 0, len(users))
	for userID := range users {
		out = append(out, userID)
	}
	return out
}

// isSessionEndEvent 判断事件是否代表会话本轮结束（任务终态）。
func isSessionEndEvent(t agenttypes.AgentEventType) bool {
	switch t {
	case agenttypes.EventTaskCompleted, agenttypes.EventTaskFailed, agenttypes.EventTaskCanceled:
		return true
	default:
		return false
	}
}

// ---- HTTP：会话实时订阅 ----

// sessionSubscribeBody 是会话订阅 / 退订请求体（session_id 为雪花 ID）。
type sessionSubscribeBody struct {
	SessionID string `json:"session_id" binding:"required"`
}

// SubscribeSession godoc
// @Summary      订阅会话实时事件
// @Description  进入会话页时调用：把当前用户登记为该会话的实时事件接收者。
// @Description  会话按项目共享，项目内所有成员都会收到同一份 WS 事件流。
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.sessionSubscribeBody  true  "会话 ID"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      403      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/session/subscribe [post]
func (h *AgentHandler) SubscribeSession(c *gin.Context) {
	h.setSessionSubscribed(c, true)
}

// UnsubscribeSession godoc
// @Summary      退订会话实时事件
// @Description  离开会话页时调用：撤销当前用户对该会话的实时事件订阅。
// @Tags         Agent
// @Accept       json
// @Produce      json
// @Param        request  body      handler.sessionSubscribeBody  true  "会话 ID"
// @Success      200      {object}  gin.H
// @Failure      400      {object}  errors.AppError
// @Failure      401      {object}  errors.AppError
// @Failure      403      {object}  errors.AppError
// @Failure      404      {object}  errors.AppError
// @Security     Bearer
// @Router       /agent/session/unsubscribe [post]
func (h *AgentHandler) UnsubscribeSession(c *gin.Context) {
	h.setSessionSubscribed(c, false)
}

func (h *AgentHandler) setSessionSubscribed(c *gin.Context, subscribed bool) {
	userID, ok := getCurrentUserID(c)
	if !ok {
		return
	}

	var body sessionSubscribeBody
	if err := c.ShouldBindJSON(&body); err != nil {
		c.Error(errors.NewValidationError("invalid request body").WithDetails(err.Error()))
		return
	}
	sessionID, err := parseAgentID(body.SessionID)
	if err != nil {
		c.Error(errors.NewValidationError("invalid session_id").WithDetails(err.Error()))
		return
	}

	// 订阅是项目内共享的：只有会话所属项目的成员才能订阅 / 退订。
	meta, err := h.chat.GetSessionMeta(c.Request.Context(), sessionID)
	if err != nil {
		handleAgentError(c, err, "failed to get session")
		return
	}
	if err := ensureProjectAccessible(c.Request.Context(), h.projects, meta.ProjectID, userID); err != nil {
		c.Error(err)
		return
	}

	if subscribed {
		h.subscribeSession(sessionID, userID)
	} else {
		h.unsubscribeSession(sessionID, userID)
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "session_id": body.SessionID, "subscribed": subscribed})
}

// subscribeSession / unsubscribeSession 是内部入口（nil-safe），供 Chat / ResumeTask 复用。
func (h *AgentHandler) subscribeSession(sessionID, userID int64) {
	if h.stream != nil {
		h.stream.subscribe(sessionID, userID)
	}
}

func (h *AgentHandler) unsubscribeSession(sessionID, userID int64) {
	if h.stream != nil {
		h.stream.unsubscribe(sessionID, userID)
	}
}
