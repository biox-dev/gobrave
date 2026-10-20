package interfaces

import (
	"context"

	"github.com/biox-dev/gobrave/internal/types"
)

// AgentAnalysisRepository 持久化 Agent 发起的一次性容器任务。
type AgentAnalysisRepository interface {
	CreateAgentAnalysis(ctx context.Context, item *types.AgentAnalysis) error
	GetAgentAnalysisByID(ctx context.Context, id int64) (*types.AgentAnalysis, error)
	// ListAgentAnalysisByProjectID 按项目列出任务，最新的在前。
	ListAgentAnalysisByProjectID(ctx context.Context, projectID int64) ([]*types.AgentAnalysis, error)
	// PageAgentAnalysisByProjectID 按项目分页列出任务，最新的在前。
	PageAgentAnalysisByProjectID(ctx context.Context, projectID int64, pagination *types.Pagination) ([]*types.AgentAnalysis, int64, error)
	// UpdateAgentAnalysisByID 只更新给定列，避免覆盖并发写入的其它字段。
	UpdateAgentAnalysisByID(ctx context.Context, id int64, values map[string]any) error
	DeleteAgentAnalysis(ctx context.Context, id int64) error
}

// AgentAnalysisCreateInput 是创建一次 AgentAnalysis 的入参。
type AgentAnalysisCreateInput struct {
	ProjectID  int64
	Name       string
	TemplateID int64
	Command    string
	// WorkspaceDir 为空时使用默认目录 <projectDir>/agent-analysis/<id>；
	// 非空时必须是 storage.base_dir 下的路径（相对路径会以 base_dir 为根解析）。
	WorkspaceDir string
}

// AgentAnalysisUpdateInput 是更新 AgentAnalysis 的入参；nil 表示不修改对应字段。
type AgentAnalysisUpdateInput struct {
	Name         *string
	TemplateID   *int64
	Command      *string
	WorkspaceDir *string
}

// AgentAnalysisService 提供 AgentAnalysis 的创建 / 查询 / 更新 / 删除，
// 以及重新运行 / 停止其容器。
type AgentAnalysisService interface {
	CreateAgentAnalysis(ctx context.Context, input AgentAnalysisCreateInput) (*types.AgentAnalysis, error)
	GetAgentAnalysis(ctx context.Context, id int64) (*types.AgentAnalysis, error)
	ListAgentAnalysisByProjectID(ctx context.Context, projectID int64) ([]*types.AgentAnalysis, error)
	PageAgentAnalysisByProjectID(ctx context.Context, projectID int64, pagination *types.Pagination) ([]*types.AgentAnalysis, int64, error)
	UpdateAgentAnalysis(ctx context.Context, id int64, input AgentAnalysisUpdateInput) (*types.AgentAnalysis, error)
	// RerunAgentAnalysis 用当前模板 / 命令重新创建一个容器实例，并重置任务状态。
	RerunAgentAnalysis(ctx context.Context, id int64) (*types.AgentAnalysis, error)
	// StopAgentAnalysis 停止（或取消尚未创建的）容器，并把任务标记为 canceled。
	StopAgentAnalysis(ctx context.Context, id int64) (*types.AgentAnalysis, error)
	// DeleteAgentAnalysis 删除任务记录，同时提交容器删除请求。
	DeleteAgentAnalysis(ctx context.Context, id int64) error
}
