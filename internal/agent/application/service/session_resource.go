package service

import (
	"context"
	"strconv"

	agenttool "github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/realtime"
)

// SessionResourceRealtimeCategory 是「会话资源」实时推送的组件类目。
//
// 与前端 brave-ui/src/event-bus/stores/components.ts 注册的 category 一致：
// 后端推 component.invoke(category, id=<session_id>, method=sessionResourcesChanged)，
// 前端 SessionResourceStack 收到后刷新 useAgentSessionResources（所有分区共享同一份数据）。
//
// id 直接用会话 ID 而不是 "*"：事件本身就是会话维度的，定向投递到该会话的前端
// 实例即可，其它会话的面板自然不会被误触发；前端同时也注册了 "*" 以兼容广播。
const SessionResourceRealtimeCategory = "agent-session-resource"

// SessionResourceRealtimeMethod 是「会话资源有新增」时推送的方法名。
const SessionResourceRealtimeMethod = "sessionResourcesChanged"

// sessionResourceService 实现 SessionResourceService：把「当前工具执行会话 ↔ 资源」
// 的关联写入持久层，并提供按类型查询。
type sessionResourceService struct {
	store agentinterfaces.SessionResourceStore
	hub   *realtime.Hub
}

// NewSessionResourceService 创建会话资源关联服务。hub 为 nil 时不推送实时事件。
func NewSessionResourceService(store agentinterfaces.SessionResourceStore, hub *realtime.Hub) agentinterfaces.SessionResourceService {
	return &sessionResourceService{store: store, hub: hub}
}

// LinkFromContext 记录当前工具执行会话与资源的关联。
//
// 会话 ID 取自工具执行事实（tool.TaskFacts）：由业务实现（脚本 / 工作流 / 分析节点）
// 在创建 / 更新成功后调用，因此「别的会话修改同一资源」也会各自记录一条关联。
//
// 这是「会话资源变化」唯一的推送出口：只有真正新增了关联（数据库里原来没有这一行）
// 才推送一次 component.invoke，前端右侧资源面板据此刷新；
// 重复关联（资源已在该会话的结果里）不推送，避免没有数据变化时白刷一次。
func (s *sessionResourceService) LinkFromContext(ctx context.Context, resourceType agenttypes.SessionResourceType, resourceIDs ...int64) error {
	if s == nil || s.store == nil || !resourceType.Valid() {
		return nil
	}
	sessionID := agenttool.SessionIDFrom(ctx)
	if sessionID <= 0 {
		return nil
	}
	inserted, err := s.store.Link(ctx, sessionID, resourceType, resourceIDs)
	if err != nil {
		return err
	}
	if inserted > 0 {
		s.notify(ctx, sessionID, resourceType, inserted)
	}
	return nil
}

// notify 把「会话资源有新增」推给该用户在线的前端。
//
// 推送失败不影响业务：它只是「让右侧面板刷新一下」的提示，
// 与 AppSessionEventHandler / UIActionPusher 对 no_client_for_user 的处理保持一致。
func (s *sessionResourceService) notify(ctx context.Context, sessionID int64, resourceType agenttypes.SessionResourceType, inserted int64) {
	if s.hub == nil {
		return
	}
	userID := agenttool.UserIDFrom(ctx)
	if userID == 0 {
		return
	}
	sessionKey := strconv.FormatInt(sessionID, 10)
	_ = s.hub.PushMessage(userID, map[string]any{
		"action": "component.invoke",
		"payload": map[string]any{
			"category": SessionResourceRealtimeCategory,
			// 用会话 ID 定位监听实例（前端按 sessionId 注册），而不是广播用的 "*"。
			"id":     sessionKey,
			"method": SessionResourceRealtimeMethod,
			"args": map[string]any{
				"session_id":    sessionKey,
				"resource_type": string(resourceType),
				"added":         inserted,
			},
		},
	})
}

func (s *sessionResourceService) List(ctx context.Context, sessionID int64, resourceType agenttypes.SessionResourceType) ([]agenttypes.SessionResource, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	rows, err := s.store.List(ctx, sessionID, resourceType)
	if err != nil {
		return nil, err
	}
	return sessionResourceValues(rows), nil
}

func (s *sessionResourceService) ListBySession(ctx context.Context, sessionID int64) ([]agenttypes.SessionResource, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	rows, err := s.store.ListBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	return sessionResourceValues(rows), nil
}

// sessionResourceValues 把指针列表转换为值列表，忽略 nil 行。
func sessionResourceValues(rows []*agenttypes.SessionResource) []agenttypes.SessionResource {
	out := make([]agenttypes.SessionResource, 0, len(rows))
	for _, row := range rows {
		if row != nil {
			out = append(out, *row)
		}
	}
	return out
}
