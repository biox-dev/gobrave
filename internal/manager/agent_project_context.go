package manager

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

// analysisNodeDoneStatus 是「分析节点已完成」的状态值，与 dag.StatusDone 保持一致。
const analysisNodeDoneStatus = "done"

// AgentProjectContextProvider 实现 agent.ProjectContextProvider：
// 查询当前用户激活项目下已完成（status=done）的分析节点，以及项目关联的参考文献，
// 并格式化为注入上下文的文本。
//
// 采用实时查询而非写入 memory repo 的设计：analysis_nodes / project_literature
// 是领域状态的唯一数据源，状态在运行期间频繁变化，实时查询可保证注入内容始终最新。
type AgentProjectContextProvider struct {
	projects interfaces.ProjectRepository
	analyses interfaces.AnalysisRepository
	cfg      *config.Config
}

// NewAgentProjectContextProvider 创建项目上下文提供者。
func NewAgentProjectContextProvider(
	projects interfaces.ProjectRepository,
	analyses interfaces.AnalysisRepository,
	cfg *config.Config,
) *AgentProjectContextProvider {
	return &AgentProjectContextProvider{projects: projects, analyses: analyses, cfg: cfg}
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

	var b strings.Builder

	nodes, err := p.analyses.ListAnalysisNodesByProjectIDAndStatus(ctx, project.ID, analysisNodeDoneStatus)
	if err != nil {
		return "", err
	}
	if analysisContext := formatAnalysisNodesContext(project, nodes); analysisContext != "" {
		b.WriteString(analysisContext)
	}

	literatures, err := p.projects.ListLiteratureByProjectID(ctx, project.ProjectID)
	if err != nil {
		return "", err
	}
	if literatureContext := p.formatLiteratureContext(project, literatures); literatureContext != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(literatureContext)
	}

	return b.String(), nil
}

// formatLiteratureContext 把项目关联的参考文献（标题 + 全文文件路径）格式化为文本块。
func (p *AgentProjectContextProvider) formatLiteratureContext(project *types.Project, literatures []*types.Literature) string {
	if len(literatures) == 0 {
		return ""
	}

	baseDir := p.resolveStorageBaseDir()

	var b strings.Builder
	b.WriteString("## 当前项目关联的参考文献 (literature)\n")
	fmt.Fprintf(&b, "项目「%s」(project_id=%s) 下关联的参考文献如下，可作为论文写作与背景资料的参考：\n",
		project.ProjectName, project.ProjectID)
	for _, lit := range literatures {
		if lit == nil {
			continue
		}
		b.WriteString("- ")
		b.WriteString(lit.Title)
		if lit.ID != 0 {
			b.WriteString(" (literature_id=")
			b.WriteString(strconv.FormatInt(lit.ID, 10))
			b.WriteString(")")
		}
		if filePath := p.literatureFilePath(lit, baseDir); filePath != "" {
			b.WriteString(" file=")
			b.WriteString(filePath)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (p *AgentProjectContextProvider) literatureFilePath(lit *types.Literature, baseDir string) string {
	if lit == nil || strings.TrimSpace(baseDir) == "" || strings.TrimSpace(lit.OwnerProjectID) == "" {
		return ""
	}
	filename := strings.TrimSpace(lit.Filename)
	if filename == "" {
		filename = types.DefaultLiteratureFilename
	}
	dir := utils.GetProjectLiteratureDir(baseDir, lit.OwnerProjectID, strconv.FormatInt(lit.ID, 10))
	return filepath.Join(dir, filename)
}

func (p *AgentProjectContextProvider) resolveStorageBaseDir() string {
	if p != nil && p.cfg != nil && p.cfg.Storage != nil {
		if base := strings.TrimSpace(p.cfg.Storage.BaseDir); base != "" {
			return base
		}
	}
	return ""
}

// formatAnalysisNodesContext 把已完成的分析节点格式化为注入上下文的文本块。
// 无节点时返回空字符串。
func formatAnalysisNodesContext(project *types.Project, nodes []*types.AnalysisNode) string {
	if len(nodes) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("## 当前项目已完成的独立分析节点 (analysis nodes)\n")
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
