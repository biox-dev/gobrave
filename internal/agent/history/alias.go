// Package history 保留旧导入路径的兼容层；实体在 types，接口在 types/interface，
// 实现在 application/service / application/repository。
package history

import (
	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

type (
	File    = agenttypes.File
	Store   = agentinterfaces.HistoryStore
	Service = agentinterfaces.HistoryService
)

const InitialVersion = agenttypes.InitialVersion

var (
	ErrNotFound         = agenttypes.ErrHistoryNotFound
	ErrDuplicateVersion = agenttypes.ErrDuplicateVersion
)

// NewService 基于 Store 构建文件历史服务（实现见 application/service）。
func NewService(store Store) Service { return agentservice.NewHistoryService(store) }
