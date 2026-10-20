package container

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/analysisform"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/dag/prepare"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

// agentRunAnalysis 实现 tool.AgentAnalysisRunner：把「名称 + 容器模板 + 命令」的模型请求
// 转换成一条命令型 AnalysisNode，并复用通用节点链路运行、等待与读取日志。
//
// 与脚本节点唯一的区别是：命令节点没有脚本，run.sh 由节点自身的 Command 生成
// （见 prepare.FileSystemNodeRuntimePreparer.prepareCommandNode），容器模板取节点上的
// ContainerTemplateID。因此运行 / 停止 / 缓存 / 日志口径与普通分析节点完全一致。
type agentRunAnalysis struct {
	analysisRepo  interfaces.AnalysisRepository
	containerRepo interfaces.ContainerRepository
	preparer      prepare.NodeRuntimePreparer
	nodeRunner    tool.AnalysisNodeRunner
	cfg           *config.Config
}

// newAgentAnalysisRunner 构建 run_agent_analysis 工具的执行器。
func newAgentAnalysisRunner(
	analysisRepo interfaces.AnalysisRepository,
	containerRepo interfaces.ContainerRepository,
	preparer prepare.NodeRuntimePreparer,
	nodeRunner tool.AnalysisNodeRunner,
	cfg *config.Config,
) tool.AgentAnalysisRunner {
	return &agentRunAnalysis{
		analysisRepo:  analysisRepo,
		containerRepo: containerRepo,
		preparer:      preparer,
		nodeRunner:    nodeRunner,
		cfg:           cfg,
	}
}

// RunAgentAnalysis 创建命令型 AnalysisNode 并运行到终态。
func (r *agentRunAnalysis) RunAgentAnalysis(ctx context.Context, in tool.AgentAnalysisInput) (tool.AgentAnalysisResult, error) {
	var result tool.AgentAnalysisResult

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
		resolved, err := r.resolveDefaultContainerTemplateID(ctx)
		if err != nil {
			return result, err
		}
		templateID = resolved
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
	if err := r.preparer.Prepare(prepare.WithSkipCleanOutput(ctx), node); err != nil {
		return result, fmt.Errorf("prepare node workspace: %w", err)
	}

	runResult, runErr := r.nodeRunner.RunAnalysisNode(ctx, node.ID, nil, in.TimeoutSeconds)
	result = tool.AgentAnalysisResult{
		AgentAnalysisID: node.ID,
		Status:          runResult.Status,
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
func (r *agentRunAnalysis) fillRuntimeFacts(ctx context.Context, nodeID int64, result *tool.AgentAnalysisResult) {
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

// resolveDefaultContainerTemplateID 返回系统默认容器模板（is_default = true 的绑定行）。
//
// 模型未指定 container_template_id 时回退到默认模板；没有默认模板时返回带用户指引导的
// 错误：模型会把错误原文回给用户，用户据此到「容器模板」页设置默认模板后重试。
func (r *agentRunAnalysis) resolveDefaultContainerTemplateID(ctx context.Context) (int64, error) {
	const guidance = "no container_template_id was provided and no default container template is configured; please ask the user to open the Container Template page and set one template as default"
	if r.containerRepo == nil {
		return 0, fmt.Errorf("%s", guidance)
	}
	definition, err := r.containerRepo.GetDefaultContainerTemplateDefinition(ctx)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, fmt.Errorf("%s", guidance)
		}
		return 0, fmt.Errorf("failed to resolve default container template: %w", err)
	}
	if definition == nil || definition.ID <= 0 {
		return 0, fmt.Errorf("%s", guidance)
	}
	return definition.ID, nil
}

// storageBaseDir 返回 storage.base_dir；未配置时返回空串（由节点构建回落到 "."）。
func (r *agentRunAnalysis) storageBaseDir() string {
	if r == nil || r.cfg == nil || r.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(r.cfg.Storage.BaseDir)
}
