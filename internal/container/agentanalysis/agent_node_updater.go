package agentanalysis

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/dag/prepare"
	"github.com/biox-dev/gobrave/internal/types"
	"gorm.io/gorm"
)

// UpdateAgentAnalysis 更新命令型（Agent 创建、无脚本）分析节点。
//
// 可更新字段：node_name / command / container_template_id；未给出的字段保持原值。
// command 不再落库（AnalysisNode 没有 Command 字段），而是直接写入节点 run.sh；
// 命令变更后重新生成 env.sh / params.json，并清空节点输出目录（旧命令的结果已失效），
// 保证下一次运行使用新命令且不复用上一轮残留产物。
func (u *analysisNodeUpdater) UpdateAgentAnalysis(ctx context.Context, in tool.AgentAnalysisUpdateInput) (tool.AgentAnalysisUpdateResult, error) {
	result := tool.AgentAnalysisUpdateResult{AnalysisNodeID: in.AnalysisNodeID}

	if in.ProjectID <= 0 {
		return result, fmt.Errorf("update_agent_analysis: project_id is required")
	}
	if in.AnalysisNodeID <= 0 {
		return result, fmt.Errorf("update_agent_analysis: analysis_node_id is required")
	}
	if u.analysisSvc == nil {
		return result, fmt.Errorf("update_agent_analysis: required services are not configured")
	}
	if in.NodeName == nil && in.Command == nil && in.ContainerTemplateID == nil {
		return result, fmt.Errorf("update_agent_analysis: at least one of node_name / command / container_template_id must be provided")
	}

	node, err := u.loadNodeForProject(ctx, in.ProjectID, in.AnalysisNodeID, "update_agent_analysis")
	if err != nil {
		return result, err
	}
	if node.NodeType() != types.AnalysisNodeCreationSourceAgent {
		return result, fmt.Errorf("update_agent_analysis: analysis node %d is a %s node; only agent (command) nodes can be updated with this tool, use update_script_analysis for script / scheduler nodes", node.ID, node.NodeTypeDescription())
	}
	// 派生路径以 WorkspaceDir 为准随时重算，下面比较 / 写入 run.sh 都要用到 CommandPath。
	node.HydrateDerivedPaths()

	update := make(map[string]any, 3)
	changed := make([]string, 0, 3)
	reprepare := false
	// commandText 记录需要写入 run.sh 的新命令（空串表示本次不改命令）。
	var commandText string

	if in.NodeName != nil {
		name := strings.TrimSpace(*in.NodeName)
		if name == "" {
			return result, fmt.Errorf("update_agent_analysis: node_name must not be empty")
		}
		if name != node.NodeName {
			update["node_name"] = name
			node.NodeName = name
			changed = append(changed, "node_name")
		}
	}

	if in.Command != nil {
		command := strings.TrimSpace(*in.Command)
		if command == "" {
			return result, fmt.Errorf("update_agent_analysis: command must not be empty")
		}
		// 命令只存在于 run.sh：与磁盘上的旧正文比较，决定是否需要重建运行期文件。
		current, readErr := prepare.ReadCommandRunScript(node.CommandPath)
		if readErr != nil {
			return result, fmt.Errorf("update_agent_analysis: failed to read run.sh of node %d: %w", node.ID, readErr)
		}
		commandText = command
		if command != strings.TrimSpace(current) {
			changed = append(changed, "command")
			reprepare = true
		}
	}

	if in.ContainerTemplateID != nil {
		templateID := *in.ContainerTemplateID
		if templateID <= 0 {
			return result, fmt.Errorf("update_agent_analysis: container_template_id must be a positive integer")
		}
		if err := u.ensureContainerTemplateExists(ctx, templateID, "update_agent_analysis"); err != nil {
			return result, err
		}
		if templateID != node.ContainerTemplateID {
			update["container_template_id"] = templateID
			node.ContainerTemplateID = templateID
			changed = append(changed, "container_template_id")
		}
	}

	if len(update) > 0 {
		if err := u.analysisSvc.UpdateAnalysisNodeByID(ctx, node.ID, update); err != nil {
			return result, fmt.Errorf("update_agent_analysis: failed to update node %d: %w", node.ID, err)
		}
	}

	if reprepare {
		if u.preparer == nil {
			return result, fmt.Errorf("update_agent_analysis: node runtime preparer is not configured")
		}
		node.HydrateDerivedPaths()
		// 不携带 prepare.WithSkipCleanOutput：命令变更后旧输出已失效，重建运行期文件时
		// 一并清空 output，避免后续把上一轮结果当成新命令的结果。
		if err := u.preparer.Prepare(ctx, node); err != nil {
			return result, fmt.Errorf("update_agent_analysis: node %d updated but failed to regenerate runtime files: %w", node.ID, err)
		}
	}

	// 命令变更时把新正文直接写入 run.sh（Prepare 只负责目录 / params.json / env.sh）。
	if commandText != "" {
		if err := prepare.WriteCommandRunScript(node.CommandPath, commandText); err != nil {
			return result, fmt.Errorf("update_agent_analysis: node %d updated but failed to write run.sh: %w", node.ID, err)
		}
	}

	node.HydrateDerivedPaths()
	// 回传当前 run.sh 正文（读失败时留空，不影响已经完成的更新）。
	currentCommand := ""
	if content, readErr := prepare.ReadCommandRunScript(node.CommandPath); readErr == nil {
		currentCommand = strings.TrimSpace(content)
	}
	return tool.AgentAnalysisUpdateResult{
		AnalysisNodeID:      node.ID,
		NodeName:            node.NodeName,
		Command:             currentCommand,
		ContainerTemplateID: node.ContainerTemplateID,
		WorkspaceDir:        node.WorkspaceDir,
		CommandPath:         node.CommandPath,
		ParamsPath:          node.ParamsPath,
		Changed:             changed,
	}, nil
}

// loadNodeForProject 读取节点并校验归属项目（避免跨项目写入）。
func (u *analysisNodeUpdater) loadNodeForProject(ctx context.Context, projectID, nodeID int64, toolName string) (*types.AnalysisNode, error) {
	node, err := u.analysisSvc.GetAnalysisNodeByID(ctx, nodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%s: analysis node %d not found", toolName, nodeID)
		}
		return nil, fmt.Errorf("%s: failed to get analysis node %d: %w", toolName, nodeID, err)
	}
	if node == nil || node.ID == 0 {
		return nil, fmt.Errorf("%s: analysis node %d not found", toolName, nodeID)
	}
	if node.ProjectID > 0 && node.ProjectID != projectID {
		return nil, fmt.Errorf("%s: analysis node %d belongs to project %d, not the active project %d", toolName, node.ID, node.ProjectID, projectID)
	}
	return node, nil
}

// ensureContainerTemplateExists 校验容器模板存在；未注入 containerRepo 时跳过校验。
func (u *analysisNodeUpdater) ensureContainerTemplateExists(ctx context.Context, templateID int64, toolName string) error {
	if u.containerRepo == nil {
		return nil
	}
	tpl, err := u.containerRepo.GetContainerTemplateByID(ctx, templateID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%s: container template %d not found", toolName, templateID)
		}
		return fmt.Errorf("%s: failed to get container template %d: %w", toolName, templateID, err)
	}
	if tpl == nil || tpl.ID == 0 {
		return fmt.Errorf("%s: container template %d not found", toolName, templateID)
	}
	return nil
}
