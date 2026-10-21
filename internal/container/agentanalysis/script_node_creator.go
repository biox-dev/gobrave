package agentanalysis

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/analysisform"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/dag/prepare"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

// scriptNodeCreator 实现 tool.AnalysisNodeCreator：为已有脚本创建一个独立分析节点
// （create_script_analysis）。
//
// 与 handler 保存独立节点保持同一口径：脚本必须属于 project_id，request_param 非空时
// 按其解析 resolved_inputs 并写入节点 params.json。节点的 inputs_patterns /
// output_patterns 取自脚本 io_schema.json，因此该文件必须已经存在（由 create_script
// 初始化，或模型已经写好）——缺失时直接报错，而不是带空契约建出一个跑不通的节点。
//
// 脚本与节点是两个独立资源：脚本本身的创建 / 更新由 agentscript 负责，本类型只做
// 「读取脚本契约 → 落库节点 → 准备工作目录」。
type scriptNodeCreator struct {
	// workflowSvc 用于读取脚本与其最新 io_schema.json。
	workflowSvc interfaces.WorkflowService
	// analysisRepo 用于落库 AnalysisNode。
	analysisRepo interfaces.AnalysisRepository
	// dataSvc 用于按脚本表单解析 request_param。
	dataSvc interfaces.DataService
	// preparer 负责写节点 params.json / run.sh / env.sh，并软链脚本与 io_schema.json。
	preparer prepare.NodeRuntimePreparer
	cfg      *config.Config
}

// NewScriptNodeCreator 构建「为已有脚本创建分析节点」执行器并注入内置工具体系。
func NewScriptNodeCreator(
	workflowSvc interfaces.WorkflowService,
	analysisRepo interfaces.AnalysisRepository,
	dataSvc interfaces.DataService,
	preparer prepare.NodeRuntimePreparer,
	cfg *config.Config,
) tool.AnalysisNodeCreator {
	return &scriptNodeCreator{
		workflowSvc:  workflowSvc,
		analysisRepo: analysisRepo,
		dataSvc:      dataSvc,
		preparer:     preparer,
		cfg:          cfg,
	}
}

// CreateAnalysisNode 为已有脚本创建一个独立分析节点（create_script_analysis）。
//
// 步骤：校验脚本归属 → 按脚本 io_schema 构建节点并落库 → 生成运行期文件 →
// 回传节点 ID 与脚本 / 节点两侧的关键路径。
func (c *scriptNodeCreator) CreateAnalysisNode(ctx context.Context, in tool.AnalysisNodeCreateInput) (tool.AnalysisNodeCreateResult, error) {
	if in.ProjectID <= 0 {
		return tool.AnalysisNodeCreateResult{}, fmt.Errorf("create_script_analysis: project_id is required")
	}
	if in.ScriptID <= 0 {
		return tool.AnalysisNodeCreateResult{}, fmt.Errorf("create_script_analysis: script_id is required")
	}
	if c.workflowSvc == nil || c.analysisRepo == nil {
		return tool.AnalysisNodeCreateResult{}, fmt.Errorf("create_script_analysis: required services are not configured")
	}

	script, err := c.workflowSvc.GetScriptByID(ctx, in.ScriptID)
	if err != nil {
		return tool.AnalysisNodeCreateResult{}, fmt.Errorf("create_script_analysis: failed to get script %d: %w", in.ScriptID, err)
	}
	if script == nil || script.ID == 0 {
		return tool.AnalysisNodeCreateResult{}, fmt.Errorf("create_script_analysis: script %d not found", in.ScriptID)
	}
	if script.ProjectID != in.ProjectID {
		return tool.AnalysisNodeCreateResult{}, fmt.Errorf("create_script_analysis: script %d belongs to project %d, not the active project %d", script.ID, script.ProjectID, in.ProjectID)
	}

	node, err := c.createNode(ctx, script, in.RequestParam, in.NodeName)
	if err != nil {
		return tool.AnalysisNodeCreateResult{}, fmt.Errorf("create_script_analysis: %w", err)
	}

	// 脚本侧路径由脚本目录派生（与 create_script / update_script 同口径）：
	// 建好节点后模型经常要紧接着改脚本或 io_schema，这里一并回传免得多查一次。
	result := tool.AnalysisNodeCreateResult{
		AnalysisNodeID: node.ID,
		NodeName:       node.NodeName,
		ScriptID:       script.ID,
		ScriptName:     script.Name,
		ScriptType:     script.ScriptType,
		WorkspaceDir:   node.WorkspaceDir,
		ParamsPath:     node.ParamsPath,
		RequestParam:   in.RequestParam,
	}
	if baseDir := c.storageBaseDir(); baseDir != "" {
		scriptDir, scriptFile, _ := utils.GetScriptFile(baseDir, script.ProjectID, script.ScriptType, script.ScriptID)
		result.ScriptFilePath = filepath.Join(scriptDir, scriptFile)
		result.IOSchemaPath = utils.ScriptIOSchemaPath(baseDir, script.ProjectID, script.ScriptID)
	}
	return result, nil
}

// createNode 按脚本契约构建、落库并准备工作目录，返回最终节点。
//
// 构建逻辑（含读取脚本表单）收敛在 analysisform.NewAnalysisNodeFromScript（前端保存 /
// create_script / create_script_analysis 共用），这里只负责落库与生成运行期文件。
// io_schema.json 缺失时 NewAnalysisNodeFromScript 会返回读取错误，因此调用方无需再校验。
func (c *scriptNodeCreator) createNode(ctx context.Context, script *types.Script, requestParam map[string]any, nodeName string) (*types.AnalysisNode, error) {
	built, err := analysisform.NewAnalysisNodeFromScript(ctx, c.dataSvc, c.workflowSvc, script, analysisform.AnalysisNodeSpec{
		ProjectID:      script.ProjectID,
		NodeName:       nodeName,
		RequestParam:   requestParam,
		CreationSource: analysisform.NodeCreationSourceScript,
		BaseDir:        c.storageBaseDir(),
	})
	if err != nil {
		return nil, err
	}
	node := built.Node

	if err := c.analysisRepo.CreateAnalysisNodes(ctx, []*types.AnalysisNode{node}); err != nil {
		return nil, fmt.Errorf("failed to create analysis node: %w", err)
	}

	// 运行期文件（params.json / run.sh / env.sh 与软链）；有 request_param 时 params.json 即已就绪。
	if c.preparer != nil {
		node.HydrateDerivedPaths()
		if err := c.preparer.Prepare(prepare.WithSkipCleanOutput(ctx), node); err != nil {
			return nil, fmt.Errorf("failed to prepare node workspace: %w", err)
		}
	}
	return node, nil
}

// storageBaseDir 返回存储根目录（容忍 cfg/Storage 为空）。
func (c *scriptNodeCreator) storageBaseDir() string {
	if c == nil || c.cfg == nil || c.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(c.cfg.Storage.BaseDir)
}
