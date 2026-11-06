package manager

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/realtime"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

var _ event.Handler = (*AppSessionEventHandler)(nil)

// AppSessionEventHandler 是「应用会话」对前端的唯一推送出口。
//
// 它订阅两类事件：
//
//  1. types.ContainerEvent（容器生命周期，来自 outbox dispatcher）：
//     把容器状态同步到所属 AppSession 的 Status / StartedAt / StoppedAt。
//     只处理 OwnerType == ContainerOwnerAppSession 的容器，DAG 节点 / 服务忽略。
//
//  2. types.AppSessionEvent（会话运行态，来自 manager.TerminalSessionRegistry）：
//     终端会话（= AppSession）的创建 / 删除 / busy / 订阅者变化，以及 Agent 在终端里
//     执行命令，都由会话层发布到这里，再统一推给前端。
//
// 之所以只保留这一处推送：会话层不再直接持有 realtime.Hub，避免同一次状态变化
// 被「容器事件」和「会话事件」各推一遍（前端会连续 refetch 两次）。
type AppSessionEventHandler struct {
	repo interfaces.ContainerRepository
	hub  *realtime.Hub
}

func NewAppSessionEventHandler(repo interfaces.ContainerRepository, hub *realtime.Hub) *AppSessionEventHandler {
	return &AppSessionEventHandler{repo: repo, hub: hub}
}

func (h *AppSessionEventHandler) Handle(evt event.Event) {
	switch e := evt.(type) {
	case types.ContainerEvent:
		h.handleContainerEvent(e)
	case types.AppSessionEvent:
		h.handleAppSessionEvent(e)
	}
}

// handleContainerEvent 把容器生命周期同步到 AppSession。
//
// 只更新持久层状态；「启动侧」事件（creating / starting / running）不再推送前端——
// 这些事件已经由会话层通过 types.AppSessionEvent（sessionCreated / sessionRunning）
// 发出去，再推一次就是重复刷新。停止 / 失败由容器事件独占，仍然推给前端。
func (h *AppSessionEventHandler) handleContainerEvent(ce types.ContainerEvent) {
	ctx := context.Background()
	inst, err := h.repo.GetContainerInstanceByID(ctx, ce.ContainerInstanceID)
	if err != nil || inst == nil {
		return
	}
	if inst.OwnerType != types.ContainerOwnerAppSession {
		return
	}

	session, err := h.repo.GetAppSessionByID(ctx, inst.OwnerID)
	if err != nil || session == nil {
		return
	}

	now := time.Now()
	normalizedEvent := normalizeContainerEvent(ce.Event)
	notify := true
	switch normalizedEvent {
	case "running":
		session.Status = "RUNNING"
		session.StartedAt = &now
		session.StoppedAt = nil
		// 启动事件已由会话层推送（见 handleAppSessionEvent），这里只落库。
		// notify = false
	case "creating":
		session.Status = "CREATING"
		session.StoppedAt = nil
		// notify = false
	case "starting":
		session.Status = "STARTING"
		session.StoppedAt = nil
		// notify = false
	case "stopped":
		session.Status = "STOPPED"
		session.StoppedAt = &now
		session.StartedAt = nil
	case "failed":
		session.Status = "FAILED"
		session.StoppedAt = &now
	case "stopping":
		session.Status = "STOPPING"
		session.StoppedAt = &now
	// case "deleting":
	// 	session.Status = "DELETING"
	// 	session.StoppedAt = &now
	// case "deleted":
	// 	h.repo.DeleteAppSession(ctx, session.ID)
	default:
		return
	}

	if err := h.repo.UpdateAppSession(ctx, session); err != nil {
		logger.Warnf(ctx, "[AppSessionEventHandler] update app session failed, session_id=%d event=%s err=%v", session.ID, ce.Event, err)
		return
	}

	if !notify {
		return
	}
	h.pushComponentInvoke(ctx, session.UserID, session.ID, appSessionRealtimeMethod(normalizedEvent), map[string]any{
		"id":     strconv.FormatInt(session.ID, 10),
		"status": strings.ToLower(strings.TrimSpace(session.Status)),
	})
}

// handleAppSessionEvent 把会话层（TerminalSessionRegistry）发布的事件转成前端消息。
//
//   - Action 非空（如 terminal.exec）：把 Args 原样作为 payload 推送；
//   - 否则按 component.invoke 推送，method = Method，前端 app-session 面板据此 refetch。
func (h *AppSessionEventHandler) handleAppSessionEvent(e types.AppSessionEvent) {
	if h.hub == nil || e.UserID == 0 {
		return
	}
	msg, action := appSessionEventMessage(e)
	if err := h.hub.PushMessage(e.UserID, msg); err != nil {
		if strings.Contains(err.Error(), "no_client_for_user:") {
			return
		}
		logger.Warnf(context.Background(), "[AppSessionEventHandler] push app session event failed user_id=%d session_id=%d action=%s method=%s err=%v", e.UserID, e.AppSessionID, action, e.Method, err)
	}
}

// appSessionEventMessage 把 types.AppSessionEvent 转成前端实时消息，并返回其 action。
//
// 单独拆出来是为了在不依赖 realtime.Hub（需要真实连接）的情况下断言消息格式。
func appSessionEventMessage(e types.AppSessionEvent) (map[string]any, string) {
	if action := strings.TrimSpace(e.Action); action != "" && action != "component.invoke" {
		payload := e.Args
		if payload == nil {
			payload = map[string]any{}
		}
		return map[string]any{
			"action":  action,
			"payload": payload,
		}, action
	}

	method := strings.TrimSpace(e.Method)
	if method == "" {
		method = "sessionDeleted"
	}
	// 会话主键为 0 表示「尚未落库」，此时对外用空串，避免前端把 0 当成真实 ID。
	sessionID := ""
	if e.AppSessionID != 0 {
		sessionID = strconv.FormatInt(e.AppSessionID, 10)
	}
	args := make(map[string]any, len(e.Args)+3)
	for key, value := range e.Args {
		args[key] = value
	}
	args["id"] = sessionID
	args["session_id"] = sessionID
	args["template_id"] = strconv.FormatInt(e.ContainerTemplateID, 10)
	if status := strings.ToLower(strings.TrimSpace(e.Status)); status != "" {
		args["status"] = status
	}

	return map[string]any{
		"action": "component.invoke",
		"payload": map[string]any{
			"category": "app-session",
			"id":       sessionID,
			"method":   method,
			"args":     args,
		},
	}, "component.invoke"
}

// pushComponentInvoke 以 component.invoke 的形式把会话事件推给前端：
// 前端 ActionDispatcher 收到后调用 useComponentStore().invoke(category, id, method, args)，
// app-session 面板注册的 handler（和 method 同名的函数）负责 refetch。
func (h *AppSessionEventHandler) pushComponentInvoke(ctx context.Context, userID, sessionID int64, method string, args map[string]any) {
	if h.hub == nil || userID == 0 {
		return
	}
	msg := map[string]any{
		"action": "component.invoke",
		"payload": map[string]any{
			"category": "app-session",
			"id":       strconv.FormatInt(sessionID, 10),
			"method":   method,
			"args":     args,
		},
	}
	if err := h.hub.PushMessage(userID, msg); err != nil {
		if strings.Contains(err.Error(), "no_client_for_user:") {
			return
		}
		logger.Warnf(ctx, "[AppSessionEventHandler] push app session event failed user_id=%d session_id=%d method=%s err=%v", userID, sessionID, method, err)
	}
}

func appSessionRealtimeMethod(normalizedEvent string) string {
	switch normalizedEvent {
	case "creating", "starting":
		return "analysisSubmitted"
	case "running":
		return "analysisStarted"
	case "stopped", "failed", "stopping":
		return "analysisDone"
	default:
		return "analysisStarted"
	}
}

// normalizeContainerEvent maps the container-level event names (e.g. "ContainerStarted",
// "ContainerStopped") to a simplified AppSession status key: creating, running, stopped, or failed.
func normalizeContainerEvent(eventName string) string {
	eventName = strings.TrimSpace(eventName)
	switch eventName {
	case "ContainerCreating":
		return "creating"
	case "ContainerStarting":
		return "starting"
	case "ContainerStarted", "ContainerResumed":
		return "running"
	case "ContainerStopped", "ContainerExited":
		return "stopped"
	case "ContainerDeleting":
		return "deleting"
	case "ContainerDeleted":
		return "deleted"
	case "ContainerStopping":
		return "stopping"

	}

	if strings.Contains(strings.ToLower(eventName), "failed") {
		return "failed"
	}

	return ""
}
