package manager

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// analysisNodeDoneStatus 是「分析节点已完成」的状态值，与 dag.StatusDone 保持一致。
const analysisNodeDoneStatus = "done"

// AgentProjectContextProvider 实现 agent.ProjectContextProvider：
// 查询当前用户激活项目下已完成（status=done）的分析节点，并格式化为注入上下文的文本。
//
// 采用实时查询而非写入 memory repo 的设计：analysis_nodes 是领域状态的唯一数据源，
// 状态在 DAG 运行期间频繁变化，实时查询可保证注入内容始终最新，避免复制与同步开销。
type AgentProjectContextProvider struct {
	projects interfaces.ProjectRepository
	analyses interfaces.AnalysisRepository
}

// NewAgentProjectContextProvider 创建项目上下文提供者。
func NewAgentProjectContextProvider(
	projects interfaces.ProjectRepository,
	analyses interfaces.AnalysisRepository,
) *AgentProjectContextProvider {
	return &AgentProjectContextProvider{projects: projects, analyses: analyses}
}

// ProjectContext 实现 agent.ProjectContextProvider。
func (p *AgentProjectContextProvider) ProjectContext(ctx context.Context, userID string) (string, error) {
	if p == nil || p.projects == nil || p.analyses == nil || strings.TrimSpace(userID) == "" {
		return "", nil
	}

	project, err := p.projects.GetActiveProjectByUserID(ctx, userID)
	if err != nil || project == nil {
		// 无激活项目时不注入任何内容，也不视为错误。
		return "", err
	}

	nodes, err := p.analyses.ListAnalysisNodesByProjectIDAndStatus(ctx, project.ID, analysisNodeDoneStatus)
	if err != nil {
		return "", err
	}

	return formatAnalysisNodesContext(project, nodes), nil
}

// formatAnalysisNodesContext 把已完成的分析节点格式化为注入上下文的文本块。
// 无节点时返回空字符串。
func formatAnalysisNodesContext(project *types.Project, nodes []*types.AnalysisNode) string {
	if len(nodes) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## 当前项目已完成的分析节点 (analysis nodes)\n")
	fmt.Fprintf(&b, "项目「%s」(project_id=%s) 下已完成（status=%s）的分析节点如下，可作为了解项目进展与可用分析结果的参考：\n",
		project.ProjectName, project.ProjectID, analysisNodeDoneStatus)
	for _, n := range nodes {
		if n == nil {
			continue
		}
		b.WriteString("- ")
		b.WriteString(n.NodeName)
		if strings.TrimSpace(n.NodeID) != "" {
			b.WriteString(" (node_id=")
			b.WriteString(n.NodeID)
			b.WriteString(")")
		}
		if strings.TrimSpace(n.SampleID) != "" {
			b.WriteString(" sample=")
			b.WriteString(n.SampleID)
		}
		if strings.TrimSpace(n.OutputDir) != "" {
			b.WriteString(" output=")
			b.WriteString(n.OutputDir)
		}
		b.WriteString("\n")
	}
	return b.String()
}
