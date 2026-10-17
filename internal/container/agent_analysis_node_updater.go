package container

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

// analysisNodeUpdater 实现 tool.AnalysisNodeUpdater：把「更新分析节点名称」的业务能力
// 暴露给内置工具 update_analysis_node。
//
// 只覆盖 node_name 这一个元数据字段：节点参数由 build_analysis_node_params /
// run_analysis_node 负责，patterns 由 io_schema 决定，运行状态由编排器维护。
type analysisNodeUpdater struct {
	analysisSvc interfaces.AnalysisService
}

// newAnalysisNodeUpdater 构建「更新分析节点基本信息」执行器并注入内置工具体系。
func newAnalysisNodeUpdater(analysisSvc interfaces.AnalysisService) tool.AnalysisNodeUpdater {
	return &analysisNodeUpdater{analysisSvc: analysisSvc}
}

// UpdateAnalysisNodeInfo 更新分析节点的 node_name，返回更新后的关键信息。
//
// 步骤：
//  1. 校验入参与服务依赖；
//  2. 读取节点并校验归属项目（避免跨项目写入）；
//  3. 只写入 node_name，不触碰参数 / patterns / 运行状态；
//  4. 回传节点名称与工作目录、params.json 路径。
func (u *analysisNodeUpdater) UpdateAnalysisNodeInfo(ctx context.Context, in tool.AnalysisNodeUpdateInput) (tool.AnalysisNodeUpdateResult, error) {
	result := tool.AnalysisNodeUpdateResult{AnalysisNodeID: in.AnalysisNodeID}

	if in.ProjectID <= 0 {
		return result, fmt.Errorf("update_analysis_node: project_id is required")
	}
	if in.AnalysisNodeID <= 0 {
		return result, fmt.Errorf("update_analysis_node: analysis_node_id is required")
	}
	if u.analysisSvc == nil {
		return result, fmt.Errorf("update_analysis_node: required services are not configured")
	}
	if in.NodeName == nil || strings.TrimSpace(*in.NodeName) == "" {
		return result, fmt.Errorf("update_analysis_node: node_name is required and must not be empty")
	}

	node, err := u.analysisSvc.GetAnalysisNodeByID(ctx, in.AnalysisNodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return result, fmt.Errorf("update_analysis_node: analysis node %d not found", in.AnalysisNodeID)
		}
		return result, fmt.Errorf("update_analysis_node: failed to get analysis node %d: %w", in.AnalysisNodeID, err)
	}
	if node == nil {
		return result, fmt.Errorf("update_analysis_node: analysis node %d not found", in.AnalysisNodeID)
	}
	if node.ProjectID > 0 && node.ProjectID != in.ProjectID {
		return result, fmt.Errorf("update_analysis_node: analysis node %d belongs to project %d, not the active project %d", node.ID, node.ProjectID, in.ProjectID)
	}

	changed := make([]string, 0, 1)
	nodeName := strings.TrimSpace(*in.NodeName)
	if nodeName != node.NodeName {
		changed = append(changed, "node_name")
	}
	if err := u.analysisSvc.UpdateAnalysisNodeByID(ctx, node.ID, map[string]any{
		"node_name": nodeName,
	}); err != nil {
		return result, fmt.Errorf("update_analysis_node: failed to update node %d: %w", node.ID, err)
	}

	return tool.AnalysisNodeUpdateResult{
		AnalysisNodeID: node.ID,
		NodeName:       nodeName,
		ScriptID:       node.ScriptID,
		WorkspaceDir:   node.WorkspaceDir,
		ParamsPath:     node.ParamsPath,
		Changed:        changed,
	}, nil
}
