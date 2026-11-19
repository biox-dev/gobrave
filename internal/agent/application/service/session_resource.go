package service

import (
	"context"

	agenttool "github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// sessionResourceService 实现 SessionResourceService：把「当前工具执行会话 ↔ 资源」
// 的关联写入持久层，并提供按类型查询。
type sessionResourceService struct {
	store agentinterfaces.SessionResourceStore
}

// NewSessionResourceService 创建会话资源关联服务。
func NewSessionResourceService(store agentinterfaces.SessionResourceStore) agentinterfaces.SessionResourceService {
	return &sessionResourceService{store: store}
}

// LinkFromContext 记录当前工具执行会话与资源的关联。
//
// 会话 ID 取自工具执行事实（tool.TaskFacts）：由业务实现（脚本 / 工作流 / 分析节点）
// 在创建 / 更新成功后调用，因此「别的会话修改同一资源」也会各自记录一条关联。
func (s *sessionResourceService) LinkFromContext(ctx context.Context, resourceType agenttypes.SessionResourceType, resourceIDs ...int64) error {
	if s == nil || s.store == nil || !resourceType.Valid() {
		return nil
	}
	sessionID := agenttool.SessionIDFrom(ctx)
	if sessionID <= 0 {
		return nil
	}
	return s.store.Link(ctx, sessionID, resourceType, resourceIDs)
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
