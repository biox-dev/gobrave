package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

// AgentAnalysisRepository 持久化 Agent 发起的一次性容器任务。
type AgentAnalysisRepository interface {
	CreateAgentAnalysis(ctx context.Context, item *types.AgentAnalysis) error
	GetAgentAnalysisByID(ctx context.Context, id int64) (*types.AgentAnalysis, error)
	// UpdateAgentAnalysisByID 只更新给定列，避免覆盖并发写入的其它字段。
	UpdateAgentAnalysisByID(ctx context.Context, id int64, values map[string]any) error
}
