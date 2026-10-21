package agentanalysis

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/analysisform"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
)

// UpdateScriptAnalysis 更新脚本型分析节点。
//
// 可更新字段：node_name / request_param；未给出的字段保持原值。
// request_param 非 nil 时按脚本最新 io_schema 重新解析 resolved_inputs，刷新
// inputs_patterns / output_patterns 并清空上一轮校验错误，最后重新生成 params.json，
// 同时清空节点输出目录（旧参数的结果已失效）。
// 传入空对象（{}）表示清空节点参数。
func (u *analysisNodeUpdater) UpdateScriptAnalysis(ctx context.Context, in tool.ScriptAnalysisUpdateInput) (tool.ScriptAnalysisUpdateResult, error) {
	result := tool.ScriptAnalysisUpdateResult{AnalysisNodeID: in.AnalysisNodeID}

	if in.ProjectID <= 0 {
		return result, fmt.Errorf("update_script_analysis: project_id is required")
	}
	if in.AnalysisNodeID <= 0 {
		return result, fmt.Errorf("update_script_analysis: analysis_node_id is required")
	}
	if u.analysisSvc == nil {
		return result, fmt.Errorf("update_script_analysis: required services are not configured")
	}
	if in.NodeName == nil && in.RequestParam == nil {
		return result, fmt.Errorf("update_script_analysis: at least one of node_name / request_param must be provided")
	}

	node, err := u.loadNodeForProject(ctx, in.ProjectID, in.AnalysisNodeID, "update_script_analysis")
	if err != nil {
		return result, err
	}
	nodeType := node.NodeType()
	if node.ScriptID <= 0 || (nodeType != types.AnalysisNodeCreationSourceScript && nodeType != types.AnalysisNodeCreationSourceScheduler) {
		return result, fmt.Errorf("update_script_analysis: analysis node %d is a %s node; only script / scheduler nodes (with a bound script) can be updated with this tool, use update_agent_analysis for agent nodes", node.ID, node.NodeTypeDescription())
	}

	update := make(map[string]any, 6)
	changed := make([]string, 0, 2)

	if in.NodeName != nil {
		name := strings.TrimSpace(*in.NodeName)
		if name == "" {
			return result, fmt.Errorf("update_script_analysis: node_name must not be empty")
		}
		if name != node.NodeName {
			update["node_name"] = name
			node.NodeName = name
			changed = append(changed, "node_name")
		}
	}

	var warnings []string
	if in.RequestParam != nil {
		if u.workflowSvc == nil {
			return result, fmt.Errorf("update_script_analysis: workflow service is not configured")
		}
		formJSONWrap, scriptIOSchema, err := u.workflowSvc.GetScriptFormJSONByID(ctx, node.ScriptID)
		if err != nil {
			return result, fmt.Errorf("update_script_analysis: failed to load script form (io_schema.json): %w", err)
		}

		resolvedInputs, err := analysisform.BuildParseAnalysisResult(ctx, u.dataSvc, in.RequestParam, formJSONWrap)
		if err != nil {
			return result, fmt.Errorf("update_script_analysis: failed to parse request_param with script io_schema: %w", err)
		}
		if resolvedInputs == nil {
			resolvedInputs = map[string]any{}
		}

		warnings = missingIOSchemaWarnings(formJSONWrap, in.RequestParam)
		if extra := unknownIOSchemaWarnings(formJSONWrap, in.RequestParam); len(extra) > 0 {
			warnings = append(warnings, extra...)
		}

		requestParamBytes, err := json.Marshal(in.RequestParam)
		if err != nil {
			return result, fmt.Errorf("update_script_analysis: failed to serialize request_param: %w", err)
		}
		update["inputs_patterns"] = types.JSONMap(utils.FormatIOSchemaItems(scriptIOSchema["inputs"]))
		update["output_patterns"] = types.JSONMap(utils.FormatIOSchemaItems(scriptIOSchema["outputs"]))
		update["resolved_inputs"] = types.JSONMap(cloneParamsMap(resolvedInputs))
		update["request_param"] = string(requestParamBytes)
		update["input_validation_errors"] = types.JSONSlice{}
		update["output_validation_errors"] = types.JSONSlice{}
		changed = append(changed, "request_param")

		// 让内存中的节点与刚写入的字段一致，Prepare 会依据它们构建 params.json。
		node.InputsPatterns = types.JSONMap(utils.FormatIOSchemaItems(scriptIOSchema["inputs"]))
		node.OutputPatterns = types.JSONMap(utils.FormatIOSchemaItems(scriptIOSchema["outputs"]))
		node.ResolvedInputs = types.JSONMap(cloneParamsMap(resolvedInputs))
		node.RequestParam = string(requestParamBytes)
		node.InputValidationErrors = types.JSONSlice{}
		node.OutputValidationErrors = types.JSONSlice{}
	}

	if len(update) > 0 {
		if err := u.analysisSvc.UpdateAnalysisNodeByID(ctx, node.ID, update); err != nil {
			return result, fmt.Errorf("update_script_analysis: failed to update node %d: %w", node.ID, err)
		}
	}

	if in.RequestParam != nil {
		if u.preparer == nil {
			return result, fmt.Errorf("update_script_analysis: node runtime preparer is not configured")
		}
		node.HydrateDerivedPaths()
		// 不携带 prepare.WithSkipCleanOutput：参数变更后旧输出已失效，重建运行期文件时
		// 一并清空 output，避免后续把上一轮结果当成新参数的结果。
		if err := u.preparer.Prepare(ctx, node); err != nil {
			return result, fmt.Errorf("update_script_analysis: node %d updated but failed to regenerate runtime files: %w", node.ID, err)
		}
	}

	node.HydrateDerivedPaths()
	requestParam := DecodeRequestParam(node.RequestParam)
	if in.RequestParam != nil {
		requestParam = in.RequestParam
	}

	return tool.ScriptAnalysisUpdateResult{
		AnalysisNodeID: node.ID,
		NodeName:       node.NodeName,
		ScriptID:       node.ScriptID,
		WorkspaceDir:   node.WorkspaceDir,
		ParamsPath:     node.ParamsPath,
		CommandPath:    node.CommandPath,
		RequestParam:   requestParam,
		Warnings:       warnings,
		Changed:        changed,
	}, nil
}
