package handler

import (
	"context"
	"net/http"
	"sync"
	"time"

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
//
// 清理策略（不再在本轮任务结束时清空）：
//   - 快速路径：页面离开时显式退订；
//   - 兜底路径：用户的最后一条实时连接断开后，宽限期结束仍未重连，才移除
//     该用户的全部会话订阅。
//
// 兜底的依据是 realtime.Hub 的连接生命周期，而不是「某次推送失败」：单次推送失败
// 可能只是 WS 正在重连，据此退订会让仍停留在会话页的用户永久失去实时流。
type sessionEventBridge struct {
	hub   *realtime.Hub
	grace time.Duration

	mu   sync.Mutex
	subs map[int64]map[int64]struct{} // sessionID → 订阅用户集合
	// byUser 是 subs 的反向索引：用户离线时按用户一次性清掉其全部会话订阅。
	byUser map[int64]map[int64]struct{} // userID → sessionID 集合
	// offlineTimers 保存等待中的离线清理定时器（重连即取消）。
	offlineTimers map[int64]*time.Timer
}

// offlineGrace 是「最后一条连接断开」到「真正清理订阅」之间的宽限期。
//
// 覆盖页面刷新 / WS 自动重连 / 网络抖动：这些场景下连接会短暂归零，
// 但用户其实还停留在会话页，不应被当成离开。
const offlineGrace = 60 * time.Second

func newSessionEventBridge(hub *realtime.Hub) *sessionEventBridge {
	return &sessionEventBridge{
		hub:           hub,
		grace:         offlineGrace,
		subs:          make(map[int64]map[int64]struct{}),
		byUser:        make(map[int64]map[int64]struct{}),
		offlineTimers: make(map[int64]*time.Timer),
	}
}

// subscribe 登记「用户 userID 正在看会话 sessionID」（幂等）。
func (b *sessionEventBridge) subscribe(sessionID, userID int64) {
	if sessionID <= 0 || userID <= 0 {
		return
	}
	b.mu.Lock()
	users := b.subs[sessionID]
	if users == nil {
		users = make(map[int64]struct{}, 1)
		b.subs[sessionID] = users
	}
	users[userID] = struct{}{}

	sessions := b.byUser[userID]
	if sessions == nil {
		sessions = make(map[int64]struct{}, 1)
		b.byUser[userID] = sessions
	}
	sessions[sessionID] = struct{}{}
	b.mu.Unlock()

	// HTTP 订阅可能早于 WS 建连：此时用户还没有在线连接，先按离线安排兜底清理；
	// 建连后 presence(online) 会取消它（宽限期远大于握手时间）。
	if b.hub != nil && !b.hub.IsUserOnline(userID) {
		b.setOnline(userID, false)
	}
}

// unsubscribe 撤销单个用户对会话的订阅；会话没有订阅者时删除整条记录。
func (b *sessionEventBridge) unsubscribe(sessionID, userID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	users, ok := b.subs[sessionID]
	if ok {
		delete(users, userID)
		if len(users) == 0 {
			delete(b.subs, sessionID)
		}
	}
	if sessions, ok := b.byUser[userID]; ok {
		delete(sessions, sessionID)
		if len(sessions) == 0 {
			delete(b.byUser, userID)
		}
	}
}

// removeUserLocked 移除某个用户的全部会话订阅（用户已离线且超过宽限期）。
func (b *sessionEventBridge) removeUserLocked(userID int64) {
	sessions := b.byUser[userID]
	delete(b.byUser, userID)
	for sessionID := range sessions {
		users := b.subs[sessionID]
		delete(users, userID)
		if len(users) == 0 {
			delete(b.subs, sessionID)
		}
	}
}

// setOnline 由 realtime.Hub 的连接生命周期驱动。
//
//   - online：用户（重新）有了连接，取消等待中的离线清理；
//   - offline：最后一条连接断开，宽限期后清理该用户的全部会话订阅。
//
// 宽限期让页面刷新 / 短暂断网不会被误判为「离开页面」，也避免了在每条事件上
// 做「连接还在不在」的探测。
func (b *sessionEventBridge) setOnline(userID int64, online bool) {
	if userID <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	if online {
		if timer, ok := b.offlineTimers[userID]; ok {
			timer.Stop()
			delete(b.offlineTimers, userID)
		}
		return
	}

	if _, exists := b.offlineTimers[userID]; exists {
		return
	}
	if _, hasSubs := b.byUser[userID]; !hasSubs {
		return
	}

	b.offlineTimers[userID] = time.AfterFunc(b.grace, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.offlineTimers, userID)
		// 宽限期内已重连（presence online 与定时器可能擦肩而过）：以连接状态为准。
		if b.hub != nil && b.hub.IsUserOnline(userID) {
			return
		}
		b.removeUserLocked(userID)
	})
}

// stop 停止所有离线清理定时器（容器关闭 / 测试清理）。
func (b *sessionEventBridge) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for userID, timer := range b.offlineTimers {
		timer.Stop()
		delete(b.offlineTimers, userID)
	}
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
			// 用户真正离开由 Hub 的连接断开 + 离线宽限期清理（见 setOnline）。
			_ = b.hub.PushMessage(userID, msg)
		}
	}
}

// subscribers 返回会话订阅者的快照（在锁外推送，避免同步分发时持锁）。
func (b *sessionEventBridge) subscribers(sessionID int64) []int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	users := b.subs[sessionID]
	out := make([]int64, 0, len(users))
	for userID := range users {
		out = append(out, userID)
	}
	return out
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
