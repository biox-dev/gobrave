package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/utils"
)

// UpdateWorkflowToolName 是「更新工作流」工具名。
const UpdateWorkflowToolName = "update_workflow"

// UpdateWorkflowInput 是 update_workflow 工具的入参。
//
// WorkflowID 为必填；其余字段均可选，nil / 空串表示不修改。
// DagDefinition / IOSchema 是整体替换语义（不是 patch）：给出时按新内容覆盖文件。
type UpdateWorkflowInput struct {
	WorkflowID    string    `json:"workflow_id"`
	Name          *string   `json:"name"`
	Description   *string   `json:"description"`
	Category      *string   `json:"category"`
	DagDefinition *JSONText `json:"dag_definition"`
	IOSchema      *JSONText `json:"io_schema"`
}

// UpdateWorkflowOutput 是 update_workflow 工具返回给模型的结果。
type UpdateWorkflowOutput struct {
	runtimecontext.WorkflowUpdateResult
	Error string `json:"error,omitempty"`
}

// UpdateWorkflow 返回一个「更新工作流」工具。
//
// 行为：
//   - workflow_id 必填，工作流必须属于当前激活项目；
//   - 只覆盖本次给出的字段，未给出的字段保持原值（读改写）；
//   - dag_definition / io_schema 给出时整体替换磁盘文件，并重新生成 workflow.json；
//   - 空串视为不修改，避免模型用空值误清空 DAG / schema（要删节点请显式提交新的完整 DAG）。
//
// 该工具会写库并重写工作流目录内文件，因此声明 write 授权。
func UpdateWorkflow(updater runtimecontext.WorkflowUpdater, browser runtimecontext.WorkflowBrowser) tool.Tool {
	return tool.NewFunc(
		UpdateWorkflowToolName,
		`更新已有工作流的元数据与 DAG 定义 / io_schema。workflow_id 必填；name / description / category / dag_definition / io_schema 至少给出一个，未给出的字段保持原值。

字段语义：
- name：工作流名称（空串视为不修改）。
- description / category：描述与分类（空串视为不修改）。
- dag_definition：DAG 定义，整体替换（不是增量 patch）。既可直接写 JSON 对象（推荐），也可写 JSON 字符串；结构为 {"nodes": [...], "edges": [...]}，nodes[] 含 id / name / script_id / inputs / outputs，edges[] 含 source / sourceHandle / target / targetHandle。先调用 get_workflow 拿到现有 DAG，在其基础上改完再整体提交，避免丢节点。
- io_schema：工作流级输入输出契约，整体替换；结构为 {"inputs": [...], "outputs": [...]}，inputs 的 name 就是分析运行 request_param 的键。

空串表示不修改对应文件（不会清空已有 DAG / schema）。

返回：更新后的工作流摘要、dag_definition_path / io_schema_path 等工作流目录路径、changed（本次实际修改的字段）与 warning（workflow.json 重新生成失败时的提示）。`,
		tool.Schema("更新工作流的入参", map[string]any{
			"workflow_id": tool.StringProperty("要更新的工作流主键 ID（必填，整数，字符串以保精度）。"),
			"name":        tool.StringProperty("新的工作流名称；不修改时留空。"),
			"description": tool.StringProperty("新的工作流描述；不修改时留空。"),
			"category":    tool.StringProperty("新的工作流分类；不修改时留空。"),
			"dag_definition": map[string]any{
				"type":        "object",
				"description": "新的 DAG 定义（整体替换，也接受 JSON 字符串）：{\"nodes\": [...], \"edges\": [...]}。不修改时留空。",
			},
			"io_schema": map[string]any{
				"type":        "object",
				"description": "新的工作流级输入输出契约（整体替换，也接受 JSON 字符串）：{\"inputs\": [...], \"outputs\": [...]}。不修改时留空。",
			},
		}, "workflow_id"),
		func(ctx context.Context, in UpdateWorkflowInput) (UpdateWorkflowOutput, error) {
			projectID, err := requireProjectID(ctx, UpdateWorkflowToolName)
			if err != nil {
				return UpdateWorkflowOutput{}, err
			}
			workflowID, err := parseWorkflowID(in.WorkflowID)
			if err != nil {
				return UpdateWorkflowOutput{}, wrapToolNameError(UpdateWorkflowToolName, err)
			}
			if updater == nil {
				return UpdateWorkflowOutput{}, fmt.Errorf("%s: workflow updater is not available in this runtime", UpdateWorkflowToolName)
			}

			input := runtimecontext.WorkflowUpdateInput{
				ProjectID:  projectID,
				WorkflowID: workflowID,
			}
			// 空串统一视为「不修改」：避免模型用空值误清空名称 / 描述 / 分类。
			if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
				name := strings.TrimSpace(*in.Name)
				input.Name = &name
			}
			if in.Description != nil && strings.TrimSpace(*in.Description) != "" {
				description := strings.TrimSpace(*in.Description)
				input.Description = &description
			}
			if in.Category != nil && strings.TrimSpace(*in.Category) != "" {
				category := strings.TrimSpace(*in.Category)
				input.Category = &category
			}
			if dag := in.DagDefinition.value(); strings.TrimSpace(dag) != "" {
				input.DagDefinition = &dag
			}
			if ioSchema := in.IOSchema.value(); strings.TrimSpace(ioSchema) != "" {
				input.IOSchema = &ioSchema
			}

			result, updateErr := updater.UpdateWorkflow(ctx, input)
			out := UpdateWorkflowOutput{WorkflowUpdateResult: result}
			if updateErr != nil {
				out.Error = updateErr.Error()
			}
			return out, nil
		},
	).WithPermissionPreview("write", "", "更新工作流元数据与 DAG 定义（写库并重写工作流目录文件）", func(ctx context.Context, in UpdateWorkflowInput) tool.PermissionPreview {
		return previewUpdateWorkflow(ctx, browser, in)
	})
}

// previewUpdateWorkflow 生成批准前预览：dag_definition.json 旧内容 → 新内容的 diff。
//
// 未给出 dag_definition 时没有文件改动可预览（仅改元数据）；读取不到旧 DAG 时
// 退化为「覆盖 dag_definition.json」的预览，不影响授权流程本身。
func previewUpdateWorkflow(ctx context.Context, browser runtimecontext.WorkflowBrowser, in UpdateWorkflowInput) tool.PermissionPreview {
	after := in.DagDefinition.value()
	if strings.TrimSpace(after) == "" {
		return tool.PermissionPreview{}
	}

	path := utils.DagDefinitionFileName
	before := ""
	if workflow, ok := workflowForPreview(ctx, browser, UpdateWorkflowToolName, in.WorkflowID); ok {
		if workflow.DagDefinitionPath != "" {
			path = workflow.DagDefinitionPath
		}
		before = formattedJSONContent(workflow.DagDefinition)
	}
	return textDiffPreview(path, before, formattedJSONContent(after))
}

// workflowForPreview 读取工作流当前状态，用于生成更新类工具的 diff 预览。
//
// 任何失败（上下文缺项目 / ID 非法 / 未注入浏览器 / 工作流不存在）都返回 ok=false，
// 由调用方退化为「无旧值」的预览；真正的错误仍由工具主体返回给模型。
func workflowForPreview(ctx context.Context, browser runtimecontext.WorkflowBrowser, toolName, rawWorkflowID string) (runtimecontext.WorkflowDetail, bool) {
	projectID, err := requireProjectID(ctx, toolName)
	if err != nil {
		return runtimecontext.WorkflowDetail{}, false
	}
	workflowID, err := parseWorkflowID(rawWorkflowID)
	if err != nil {
		return runtimecontext.WorkflowDetail{}, false
	}
	if browser == nil {
		return runtimecontext.WorkflowDetail{}, false
	}
	detail, err := browser.GetWorkflowByID(ctx, projectID, workflowID)
	if err != nil {
		return runtimecontext.WorkflowDetail{}, false
	}
	return detail, true
}
