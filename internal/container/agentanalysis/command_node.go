package agentanalysis

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
	"github.com/biox-dev/gobrave/internal/analysisform"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/dag/prepare"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// agentRunAnalysis 实现 runtimecontext.AgentAnalysisRunner：把「名称 + 容器模板 + 命令」的模型请求
// 转换成一条命令型 AnalysisNode，并复用通用节点链路运行、等待与读取日志。
//
// 与脚本节点唯一的区别是：命令节点没有脚本，容器模板取节点上的 ContainerTemplateID，
// run.sh 由本执行器在 Prepare 之后直接写盘（prepare.WriteCommandRunScript）——命令不再
// 落库，因此运行 / 停止 / 缓存 / 日志口径与普通分析节点完全一致。
type agentRunAnalysis struct {
	analysisRepo  interfaces.AnalysisRepository
	containerRepo interfaces.ContainerRepository
	preparer      prepare.NodeRuntimePreparer
	nodeRunner    runtimecontext.AnalysisNodeRunner
	// resources 记录「当前会话 ↔ 分析节点」的关联。
	resources agentinterfaces.SessionResourceService
	cfg       *config.Config
}

// NewAgentAnalysisRunner 构建 create_analysis_node_agent 工具的执行器。
func NewAgentAnalysisRunner(
	analysisRepo interfaces.AnalysisRepository,
	containerRepo interfaces.ContainerRepository,
	preparer prepare.NodeRuntimePreparer,
	nodeRunner runtimecontext.AnalysisNodeRunner,
	resources agentinterfaces.SessionResourceService,
	cfg *config.Config,
) runtimecontext.AgentAnalysisRunner {
	return &agentRunAnalysis{
		analysisRepo:  analysisRepo,
		containerRepo: containerRepo,
		preparer:      preparer,
		nodeRunner:    nodeRunner,
		resources:     resources,
		cfg:           cfg,
	}
}

// CreateAgentAnalysis 创建命令型 AnalysisNode，并按 Run 决定是否运行到终态。
//
// Prepare 未携带 prepare.WithSkipCleanOutput，因此每次创建都会清理节点输出目录，
// 避免上一轮运行的残留文件干扰本次执行结果。
func (r *agentRunAnalysis) CreateAgentAnalysis(ctx context.Context, in runtimecontext.AgentAnalysisInput) (runtimecontext.AgentAnalysisResult, error) {
	var result runtimecontext.AgentAnalysisResult

	if in.ProjectID <= 0 {
		return result, fmt.Errorf("project id is required")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return result, fmt.Errorf("name is required")
	}
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return result, fmt.Errorf("command is required")
	}
	if r.analysisRepo == nil || r.preparer == nil || r.nodeRunner == nil {
		return result, fmt.Errorf("agent analysis services are not configured")
	}

	templateID := in.ContainerTemplateID
	if templateID <= 0 {
		return result, fmt.Errorf("container_template_id is required: the agent must pass an existing container template id")
	}

	node, err := analysisform.NewAnalysisNodeFromCommand(analysisform.CommandNodeSpec{
		ProjectID:           in.ProjectID,
		ContainerTemplateID: templateID,
		Command:             command,
		NodeName:            name,
		BaseDir:             r.storageBaseDir(),
	})
	if err != nil {
		return result, err
	}
	if err := r.analysisRepo.CreateAnalysisNodes(ctx, []*types.AnalysisNode{node}); err != nil {
		return result, fmt.Errorf("create analysis node: %w", err)
	}
	// 记录「当前会话创建了该分析节点」；关联失败不影响创建结果。
	if r.resources != nil {
		_ = r.resources.LinkFromContext(ctx, agenttypes.SessionResourceTypeAnalysisNode, node.ID)
	}
	if err := r.preparer.Prepare(ctx, node); err != nil {
		return result, fmt.Errorf("prepare node workspace: %w", err)
	}
	// 命令不再落库：Prepare 只建目录 / params.json / env.sh，run.sh 由这里直接写入。
	if err := prepare.WriteCommandRunScript(node.CommandFile(), command); err != nil {
		return result, fmt.Errorf("write node run.sh: %w", err)
	}

	// Run 可选且默认为运行：nil / true 表示创建后立即运行，false 表示只创建节点。
	if in.Run != nil && !*in.Run {
		result = runtimecontext.AgentAnalysisResult{
			AgentAnalysisID: node.ID,
			Status:          node.Status,
			Ran:             false,
			WorkspaceDir:    node.Dir(),
			CommandPath:     node.CommandFile(),
		}
		r.fillRuntimeFacts(ctx, node.ID, &result)
		return result, nil
	}

	runResult, runErr := r.nodeRunner.RunAnalysisNode(ctx, node.ID, nil, in.TimeoutSeconds)
	result = runtimecontext.AgentAnalysisResult{
		AgentAnalysisID: node.ID,
		Status:          runResult.Status,
		Ran:             true,
		Success:         runResult.Success,
		Log:             runResult.Log,
		LogPath:         runResult.LogPath,
		WorkspaceDir:    runResult.WorkspaceDir,
		CommandPath:     runResult.CommandPath,
	}
	r.fillRuntimeFacts(ctx, node.ID, &result)
	return result, runErr
}

// fillRuntimeFacts 回填容器实例 / 退出码等运行期事实（尽力而为，不影响运行结果）。
func (r *agentRunAnalysis) fillRuntimeFacts(ctx context.Context, nodeID int64, result *runtimecontext.AgentAnalysisResult) {
	if node, err := r.analysisRepo.GetAnalysisNodeByID(ctx, nodeID); err == nil && node != nil {
		result.ExitCode = node.ExitCode
	}
	if r.containerRepo == nil {
		return
	}
	inst, err := r.containerRepo.GetContainerInstanceByOwner(ctx, types.ContainerOwnerDagNode, nodeID)
	if err == nil && inst != nil {
		result.ContainerInstanceID = inst.ID
		if inst.ExitCode != nil {
			result.ExitCode = *inst.ExitCode
		}
	}
}

// storageBaseDir 返回 storage.base_dir；未配置时返回空串（由节点构建回落到 "."）。
func (r *agentRunAnalysis) storageBaseDir() string {
	if r == nil || r.cfg == nil || r.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(r.cfg.Storage.BaseDir)
}
