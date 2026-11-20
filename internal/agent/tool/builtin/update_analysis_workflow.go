package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// UpdateAnalysisWorkflowToolName 是「更新分析运行（工作流运行）」工具名。
const UpdateAnalysisWorkflowToolName = "update_analysis_workflow"

// UpdateAnalysisWorkflowInput 是 update_analysis_workflow 工具的入参。
//
// 所有可选字段用指针 / nil map 表示：nil 表示不修改，非 nil 时按值覆盖。
// RequestParam 显式传空对象（{}）表示清空参数。
type UpdateAnalysisWorkflowInput struct {
	AnalysisID   string         `json:"analysis_id"`
	AnalysisName *string        `json:"analysis_name"`
	RequestParam map[string]any `json:"request_param"`
	CacheType    *int           `json:"cache_type"`
	// File 固定为 params（params.json 文件编辑），配合 content 或 old_string → new_string。
	File      string  `json:"file"`
	Content   *string `json:"content"`
	OldString string  `json:"old_string"`
	NewString string  `json:"new_string"`
}

// UpdateAnalysisWorkflowOutput 是 update_analysis_workflow 工具返回给模型的结果。
type UpdateAnalysisWorkflowOutput struct {
	runtimecontext.AnalysisUpdateResult
	Error string `json:"error,omitempty"`
}

// UpdateAnalysisWorkflow 返回一个「更新分析运行」工具。
//
// 行为：
//   - analysis_id 必填，且分析必须属于当前激活项目；
//   - 只覆盖本次给出的字段；未给出的保持原值；
//   - request_param 非 nil 时按工作流表单重新解析并重写 params.json，节点状态按
//     cache_type 重置（不做提交，运行请用 run_analysis_workflow）；
//   - 运行中的分析（running / stopping）拒绝修改。
//
// 该工具会写库并重写 params.json，因此声明 write 授权。
func UpdateAnalysisWorkflow(updater runtimecontext.AnalysisUpdater, browser runtimecontext.AnalysisBrowser) tool.Tool {
	return tool.NewFunc(
		UpdateAnalysisWorkflowToolName,
		`更新一次分析运行（nextflow 行）的分析名称 / 请求参数 / 缓存策略，不会提交运行。

analysis_id 必填；analysis_name / request_param / cache_type 至少给出一个，未给出的字段保持原值。

字段语义：
- analysis_name：分析名称。
- request_param：请求参数对象，键为工作流 io_schema.inputs 中各条目的 name。给出时会按工作流表单重新解析并重写分析目录下的 params.json，同时按 cache_type 重置节点状态；显式传空对象（{}）表示清空参数。
- cache_type：缓存策略。1=全量重跑；2=复用已有节点；3=脚本未变则复用；4=脚本与参数都未变才复用。

运行中的分析（job_status=running / stopping）不允许修改，需要先停止。要改完立即运行，改完再调用 run_analysis_workflow（或直接用 run_analysis_workflow 传 request_param 一步完成）。

返回：更新后的分析摘要（含 job_status / scheduler_mode / params_path / request_param / 节点状态分布）与 changed。`,
		tool.Schema("更新分析运行的入参", map[string]any{
			"analysis_id":   tool.StringProperty("要更新的分析运行主键 ID（必填，整数，字符串以保精度）。"),
			"analysis_name": tool.StringProperty("新的分析名称；不修改时留空。"),
			"request_param": map[string]any{
				"type":        "object",
				"description": "新的请求参数对象，键为工作流 io_schema.inputs 中各条目的 name。缺省表示不修改；显式传空对象（{}）表示清空参数。",
			},
			"cache_type": tool.IntegerProperty("缓存策略：1 全量重跑；2 复用已有节点；3 脚本未变则复用；4 脚本与参数都未变才复用。不修改时留空。"),
			"file":       tool.StringProperty("要直接编辑的文件：params（params.json）。留空表示不改文件。"),
			"content":    tool.StringProperty("整体覆盖 params.json 正文（必须是合法 JSON 对象）；与 old_string/new_string 二选一。"),
			"old_string": tool.StringProperty("文本替换模式：要替换的原文，必须在 params.json 中唯一出现。"),
			"new_string": tool.StringProperty("文本替换模式：替换后的文本。"),
		}, "analysis_id"),
		func(ctx context.Context, in UpdateAnalysisWorkflowInput) (UpdateAnalysisWorkflowOutput, error) {
			projectID, err := requireProjectID(ctx, UpdateAnalysisWorkflowToolName)
			if err != nil {
				return UpdateAnalysisWorkflowOutput{}, err
			}
			analysisID, err := parseAnalysisID(in.AnalysisID)
			if err != nil {
				return UpdateAnalysisWorkflowOutput{}, wrapToolNameError(UpdateAnalysisWorkflowToolName, err)
			}
			file, err := parseResourceFile(in.File, UpdateAnalysisWorkflowToolName, runtimecontext.ResourceFileParams)
			if err != nil {
				return UpdateAnalysisWorkflowOutput{}, err
			}
			edit, err := resourceEditFrom(UpdateAnalysisWorkflowToolName, file, in.Content, in.OldString, in.NewString)
			if err != nil {
				return UpdateAnalysisWorkflowOutput{}, err
			}
			if !edit.IsZero() && in.RequestParam != nil {
				return UpdateAnalysisWorkflowOutput{}, fmt.Errorf("%s: file edit cannot be combined with request_param; choose one style", UpdateAnalysisWorkflowToolName)
			}
			if in.AnalysisName == nil && in.RequestParam == nil && in.CacheType == nil && edit.IsZero() {
				return UpdateAnalysisWorkflowOutput{}, fmt.Errorf("%s: at least one of analysis_name / request_param / cache_type / file edit must be provided", UpdateAnalysisWorkflowToolName)
			}
			if updater == nil {
				return UpdateAnalysisWorkflowOutput{}, fmt.Errorf("%s: analysis updater is not available in this runtime", UpdateAnalysisWorkflowToolName)
			}

			preview, err := previewAnalysisParams(ctx, browser, projectID, analysisID, in.RequestParam, edit)
			if err != nil {
				return UpdateAnalysisWorkflowOutput{Error: err.Error()}, nil
			}
			if err := authorizeResourceWrite(ctx, UpdateAnalysisWorkflowToolName, fmt.Sprintf("Update analysis run %d", analysisID), preview); err != nil {
				return UpdateAnalysisWorkflowOutput{}, err
			}

			result, updateErr := updater.UpdateAnalysis(ctx, runtimecontext.AnalysisUpdateInput{
				ProjectID:    projectID,
				AnalysisID:   analysisID,
				AnalysisName: in.AnalysisName,
				RequestParam: in.RequestParam,
				CacheType:    in.CacheType,
				File:         edit.File,
				Content:      edit.Content,
				OldString:    edit.OldString,
				NewString:    edit.NewString,
			})
			out := UpdateAnalysisWorkflowOutput{AnalysisUpdateResult: result}
			if updateErr != nil {
				out.Error = updateErr.Error()
			}
			return out, nil
		},
	)
}

// previewAnalysisParams 生成批准前预览：params.json 旧内容 → 新内容 diff。
//
// 仅改名 / 缓存策略时没有文件改动可预览；旧内容通过浏览器直接读取。
func previewAnalysisParams(ctx context.Context, browser runtimecontext.AnalysisBrowser, projectID, analysisID int64, requestParam map[string]any, edit runtimecontext.ResourceFileEdit) (tool.PermissionPreview, error) {
	if requestParam == nil && edit.IsZero() {
		return tool.PermissionPreview{}, nil
	}
	if browser == nil {
		// 无浏览器时退化为「无旧值」的预览，不影响授权与写入本身。
		if requestParam != nil {
			return textDiffPreview("params.json", "", paramsJSONContent(requestParam)), nil
		}
		return tool.PermissionPreview{}, nil
	}
	current, err := browser.ReadAnalysisFile(ctx, projectID, analysisID, runtimecontext.ResourceFileParams)
	if err != nil {
		return tool.PermissionPreview{}, err
	}
	before := current.Content
	after := ""
	if !edit.IsZero() {
		after, err = edit.Apply(before)
		if err != nil {
			return tool.PermissionPreview{}, err
		}
	} else {
		after = paramsJSONContent(requestParam)
	}
	if after == "" {
		return tool.PermissionPreview{}, nil
	}
	path := current.Path
	if path == "" {
		path = "params.json"
	}
	return textDiffPreview(path, before, after), nil
}
