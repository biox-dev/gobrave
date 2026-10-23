// Package session 保留旧导入路径的兼容层；实体在 types，接口在 types/interface，
// 实现在 application/service / application/repository。
package session

import (
	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

type (
	Session            = agenttypes.Session
	CreateParams       = agenttypes.SessionCreateParams
	ChildSessionParams = agenttypes.SessionChildParams
	ListParams         = agenttypes.SessionListParams
	Store              = agentinterfaces.SessionStore
	Service            = agentinterfaces.SessionService
)

const TitleSessionTitle = agenttypes.TitleSessionTitle

var (
	ErrNotFound             = agenttypes.ErrSessionNotFound
	ErrInvalidParentSession = agenttypes.ErrInvalidParentSession
)

// NewService 创建会话服务（实现见 application/service）。
func NewService(store Store) Service { return agentservice.NewSessionService(store) }
