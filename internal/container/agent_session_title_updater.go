package container

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/logger"
)

// sessionTitleUpdater 把会话服务的标题写能力适配为 update_session_title 工具端口。
//
// 只更新当前执行会话（sessionID 来自执行事实），并按 userID 做归属校验：
// 会话属于某个用户，工具不接收模型指定的任意 session_id，避免越权改名。
type sessionTitleUpdater struct {
	sessions  agentinterfaces.SessionService
	uiActions runtimecontext.UIActionPusher
}

func newSessionTitleUpdater(sessions agentinterfaces.SessionService, uiActions runtimecontext.UIActionPusher) *sessionTitleUpdater {
	return &sessionTitleUpdater{sessions: sessions, uiActions: uiActions}
}

func (u *sessionTitleUpdater) UpdateSessionTitle(ctx context.Context, sessionID, userID int64, title string) (runtimecontext.SessionTitleUpdateResult, error) {
	if sessionID <= 0 {
		return runtimecontext.SessionTitleUpdateResult{}, fmt.Errorf("update_session_title: session_id is required")
	}
	if u.sessions == nil {
		return runtimecontext.SessionTitleUpdateResult{}, fmt.Errorf("update_session_title: session service is not configured")
	}

	sess, err := u.sessions.Get(ctx, sessionID)
	if err != nil {
		if errors.Is(err, agenttypes.ErrSessionNotFound) {
			return runtimecontext.SessionTitleUpdateResult{}, fmt.Errorf("update_session_title: session %d not found", sessionID)
		}
		return runtimecontext.SessionTitleUpdateResult{}, fmt.Errorf("update_session_title: failed to load session: %w", err)
	}
	if userID > 0 && sess.UserID > 0 && sess.UserID != userID {
		return runtimecontext.SessionTitleUpdateResult{}, fmt.Errorf("update_session_title: session %d does not belong to the current user", sessionID)
	}

	updated, err := u.sessions.UpdateTitle(ctx, sessionID, title)
	if err != nil {
		return runtimecontext.SessionTitleUpdateResult{}, fmt.Errorf("update_session_title: failed to update title: %w", err)
	}

	u.pushTitleUpdated(ctx, updated)
	return runtimecontext.SessionTitleUpdateResult{SessionID: updated.ID, Title: updated.Title}, nil
}

// pushTitleUpdated 通知该用户的在线前端刷新会话标题。
//
// 事件口径与异步标题生成（ChatService.scheduleSessionTitle）保持一致：
// 会话列表组件监听 agent-session 频道的 sessionTitleUpdated 方法；
// session_id 必须是字符串（雪花 ID 超出 JS 安全整数范围）。
// 推送是尽力而为：失败只记日志，不影响改名结果。
func (u *sessionTitleUpdater) pushTitleUpdated(ctx context.Context, sess agenttypes.Session) {
	if u.uiActions == nil || sess.UserID <= 0 {
		return
	}
	if err := u.uiActions.PushUIAction(ctx, sess.UserID, "component.invoke", map[string]any{
		"category": "agent-session",
		"id":       "*",
		"method":   "sessionTitleUpdated",
		"args": map[string]any{
			"session_id": strconv.FormatInt(sess.ID, 10),
			"title":      sess.Title,
		},
	}); err != nil {
		logger.Warnf(ctx, "[Agent] push session title event failed (session=%d): %v", sess.ID, err)
	}
}
