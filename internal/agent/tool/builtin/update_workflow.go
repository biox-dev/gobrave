package builtin

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
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
	// File + Content / OldString → NewString 可以直接编辑工作流目录内的
	// dag_definition / io_schema / readme，无需再用 write / edit / bash。
	File      string  `json:"file"`
	Content   *string `json:"content"`
	OldString string  `json:"old_string"`
	NewString string  `json:"new_string"`
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
			"file":       tool.StringProperty("要编辑的工作流文件：dag_definition / io_schema / readme。留空表示不改文件。"),
			"content":    tool.StringProperty("整体覆盖该文件正文；与 old_string/new_string 二选一（不能与 dag_definition / io_schema 同时使用）。"),
			"old_string": tool.StringProperty("文本替换模式：要替换的原文，必须在文件中唯一出现。"),
			"new_string": tool.StringProperty("文本替换模式：替换后的文本。"),
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

			file, err := parseResourceFile(in.File, UpdateWorkflowToolName,
				runtimecontext.ResourceFileDagDefinition, runtimecontext.ResourceFileIOSchema, runtimecontext.ResourceFileReadme)
			if err != nil {
				return UpdateWorkflowOutput{}, err
			}
			edit, err := resourceEditFrom(UpdateWorkflowToolName, file, in.Content, in.OldString, in.NewString)
			if err != nil {
				return UpdateWorkflowOutput{}, err
			}
			if !edit.IsZero() && (strings.TrimSpace(in.DagDefinition.value()) != "" || strings.TrimSpace(in.IOSchema.value()) != "") {
				return UpdateWorkflowOutput{}, fmt.Errorf("%s: file edit cannot be combined with dag_definition / io_schema", UpdateWorkflowToolName)
			}

			input := runtimecontext.WorkflowUpdateInput{
				ProjectID:  projectID,
				WorkflowID: workflowID,
				File:       edit.File,
				Content:    edit.Content,
				OldString:  edit.OldString,
				NewString:  edit.NewString,
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

			preview, err := updateWorkflowPreview(ctx, browser, projectID, workflowID, edit)
			if err != nil {
				return UpdateWorkflowOutput{Error: err.Error()}, nil
			}
			if err := authorizeResourceWrite(ctx, UpdateWorkflowToolName, fmt.Sprintf("Update workflow %d", workflowID), preview); err != nil {
				return UpdateWorkflowOutput{}, err
			}

			result, updateErr := updater.UpdateWorkflow(ctx, input)
			out := UpdateWorkflowOutput{WorkflowUpdateResult: result}
			if updateErr != nil {
				out.Error = updateErr.Error()
			}
			return out, nil
		},
	)
}

// updateWorkflowPreview 生成批准前预览：优先展示 file 编辑的 before → after diff；
// 未指定 file 但给出 dag_definition 时，退化为 DAG 文件覆盖预览。
func updateWorkflowPreview(ctx context.Context, browser runtimecontext.WorkflowBrowser, projectID, workflowID int64, edit runtimecontext.ResourceFileEdit) (tool.PermissionPreview, error) {
	if edit.IsZero() {
		return tool.PermissionPreview{}, nil
	}
	if browser == nil {
		// 无浏览器时退化为「无旧值」的预览，不影响授权与写入本身。
		if edit.Content != nil {
			return tool.PermissionPreview{FilePath: string(edit.File), Diff: formattedJSONContent(*edit.Content)}, nil
		}
		return tool.PermissionPreview{}, nil
	}
	current, err := browser.ReadWorkflowFile(ctx, projectID, workflowID, edit.File)
	if err != nil {
		return tool.PermissionPreview{}, err
	}
	after, err := edit.Apply(current.Content)
	if err != nil {
		return tool.PermissionPreview{}, err
	}
	return previewFileEdit(current.Path, current.Content, after), nil
}
