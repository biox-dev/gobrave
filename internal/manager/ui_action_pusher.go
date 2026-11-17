package manager

import (
	"context"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/realtime"
)

var _ runtimecontext.UIActionPusher = (*UIActionPusher)(nil)

// UIActionPusher 把 Agent 发起的 UI 动作推给当前用户的在线前端（ui_action 工具）。
//
// 它是 ui_action 的唯一推送出口，和 AppSessionEventHandler 同层：只依赖 realtime.Hub，
// 不反向依赖 agent 包。消息格式与 terminal.exec 一致——顶层 {action, payload}，
// 由前端 registerHandlers 的 sse.onMessage 直接交给 ActionDispatcher 分发。
type UIActionPusher struct {
	hub *realtime.Hub
}

// NewUIActionPusher 构造 UIActionPusher。hub 为 nil 时所有推送都是空操作。
func NewUIActionPusher(hub *realtime.Hub) *UIActionPusher {
	return &UIActionPusher{hub: hub}
}

// PushUIAction 实现 runtimecontext.UIActionPusher。
//
// 没有在线客户端不算错误：UI 动作只是「让界面跟着动一下」，用户离线时静默丢弃即可，
// 与 AppSessionEventHandler 对 no_client_for_user 的处理保持一致。
func (p *UIActionPusher) PushUIAction(_ context.Context, userID int64, action string, payload map[string]any) error {
	if p.hub == nil || userID == 0 || action == "" {
		return nil
	}
	if payload == nil {
		payload = map[string]any{}
	}
	err := p.hub.PushMessage(userID, map[string]any{
		"action":  action,
		"payload": payload,
	})
	if err != nil && strings.Contains(err.Error(), "no_client_for_user:") {
		return nil
	}
	return err
}
