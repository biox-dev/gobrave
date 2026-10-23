// Package message 保留旧导入路径的兼容层；实体在 types，接口在 types/interface，
// 实现在 application/service / application/repository。
package message

import (
	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

type (
	Message      = agenttypes.Message
	CreateParams = agenttypes.MessageCreateParams
	Store        = agentinterfaces.MessageStore
	Service      = agentinterfaces.MessageService
)

const (
	RoleSystem    = agenttypes.RoleSystem
	RoleUser      = agenttypes.RoleUser
	RoleAssistant = agenttypes.RoleAssistant
	RoleTool      = agenttypes.RoleTool

	KindUser           = agenttypes.KindUser
	KindAssistant      = agenttypes.KindAssistant
	KindAssistantFinal = agenttypes.KindAssistantFinal
	KindReasoning      = agenttypes.KindReasoning
	KindToolCall       = agenttypes.KindToolCall
	KindToolResult     = agenttypes.KindToolResult
	KindError          = agenttypes.KindError
)

var ErrNotFound = agenttypes.ErrMessageNotFound

// NewService 创建消息服务（实现见 application/service）。
func NewService(store Store) Service { return agentservice.NewMessageService(store) }
