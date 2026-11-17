package builtin

import (
	"context"
	"fmt"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// UpdateAnalysisToolName 是「更新分析运行」工具名。
const UpdateAnalysisToolName = "update_analysis"

// UpdateAnalysisInput 是 update_analysis 工具的入参。
//
// 所有可选字段用指针 / nil map 表示：nil 表示不修改，非 nil 时按值覆盖。
// RequestParam 显式传空对象（{}）表示清空参数。
type UpdateAnalysisInput struct {
	AnalysisID   string         `json:"analysis_id"`
	AnalysisName *string        `json:"analysis_name"`
	RequestParam map[string]any `json:"request_param"`
	CacheType    *int           `json:"cache_type"`
}

// UpdateAnalysisOutput 是 update_analysis 工具返回给模型的结果。
type UpdateAnalysisOutput struct {
	runtimecontext.AnalysisUpdateResult
	Error string `json:"error,omitempty"`
}

// UpdateAnalysis 返回一个「更新分析运行」工具。
//
// 行为：
//   - analysis_id 必填，且分析必须属于当前激活项目；
//   - 只覆盖本次给出的字段；未给出的保持原值；
//   - request_param 非 nil 时按工作流表单重新解析并重写 params.json，节点状态按
//     cache_type 重置（不做提交，运行请用 run_analysis）；
//   - 运行中的分析（running / stopping）拒绝修改。
//
// 该工具会写库并重写 params.json，因此声明 write 授权。
func UpdateAnalysis(updater runtimecontext.AnalysisUpdater, browser runtimecontext.AnalysisBrowser) tool.Tool {
	return tool.NewFunc(
		UpdateAnalysisToolName,
		`更新一次分析运行（nextflow 行）的分析名称 / 请求参数 / 缓存策略，不会提交运行。

analysis_id 必填；analysis_name / request_param / cache_type 至少给出一个，未给出的字段保持原值。

字段语义：
- analysis_name：分析名称。
- request_param：请求参数对象，键为工作流 io_schema.inputs 中各条目的 name。给出时会按工作流表单重新解析并重写分析目录下的 params.json，同时按 cache_type 重置节点状态；显式传空对象（{}）表示清空参数。
- cache_type：缓存策略。1=全量重跑；2=复用已有节点；3=脚本未变则复用；4=脚本与参数都未变才复用。

运行中的分析（job_status=running / stopping）不允许修改，需要先停止。要改完立即运行，改完再调用 run_analysis（或直接用 run_analysis 传 request_param 一步完成）。

返回：更新后的分析摘要（含 job_status / scheduler_mode / params_path / request_param / 节点状态分布）与 changed。`,
		tool.Schema("更新分析运行的入参", map[string]any{
			"analysis_id":   tool.StringProperty("要更新的分析运行主键 ID（必填，整数，字符串以保精度）。"),
			"analysis_name": tool.StringProperty("新的分析名称；不修改时留空。"),
			"request_param": map[string]any{
				"type":        "object",
				"description": "新的请求参数对象，键为工作流 io_schema.inputs 中各条目的 name。缺省表示不修改；显式传空对象（{}）表示清空参数。",
			},
			"cache_type": tool.IntegerProperty("缓存策略：1 全量重跑；2 复用已有节点；3 脚本未变则复用；4 脚本与参数都未变才复用。不修改时留空。"),
		}, "analysis_id"),
		func(ctx context.Context, in UpdateAnalysisInput) (UpdateAnalysisOutput, error) {
			projectID, err := requireProjectID(ctx, UpdateAnalysisToolName)
			if err != nil {
				return UpdateAnalysisOutput{}, err
			}
			analysisID, err := parseAnalysisID(in.AnalysisID)
			if err != nil {
				return UpdateAnalysisOutput{}, wrapToolNameError(UpdateAnalysisToolName, err)
			}
			if in.AnalysisName == nil && in.RequestParam == nil && in.CacheType == nil {
				return UpdateAnalysisOutput{}, fmt.Errorf("%s: at least one of analysis_name / request_param / cache_type must be provided", UpdateAnalysisToolName)
			}
			if updater == nil {
				return UpdateAnalysisOutput{}, fmt.Errorf("%s: analysis updater is not available in this runtime", UpdateAnalysisToolName)
			}

			result, updateErr := updater.UpdateAnalysis(ctx, runtimecontext.AnalysisUpdateInput{
				ProjectID:    projectID,
				AnalysisID:   analysisID,
				AnalysisName: in.AnalysisName,
				RequestParam: in.RequestParam,
				CacheType:    in.CacheType,
			})
			out := UpdateAnalysisOutput{AnalysisUpdateResult: result}
			if updateErr != nil {
				out.Error = updateErr.Error()
			}
			return out, nil
		},
	).WithPermissionPreview("write", "", "更新分析运行的名称 / 参数 / 缓存策略（写库并重写 params.json）", func(ctx context.Context, in UpdateAnalysisInput) tool.PermissionPreview {
		return previewAnalysisParams(ctx, browser, UpdateAnalysisToolName, in.AnalysisID, in.RequestParam)
	})
}

// previewAnalysisParams 生成批准前预览：params.json 中请求参数的旧值 → 新值 diff。
//
// 未给出 request_param 时没有参数改动可预览（仅改名 / 缓存策略不会展示 diff）；
// 读取不到分析当前参数时退化为「覆盖 params.json」的预览，不影响授权流程本身。
func previewAnalysisParams(ctx context.Context, browser runtimecontext.AnalysisBrowser, toolName, rawAnalysisID string, requestParam map[string]any) tool.PermissionPreview {
	if requestParam == nil {
		return tool.PermissionPreview{}
	}
	after := paramsJSONContent(requestParam)
	if after == "" {
		return tool.PermissionPreview{}
	}

	path := "params.json"
	before := ""
	if detail, ok := analysisForPreview(ctx, browser, toolName, rawAnalysisID); ok {
		if detail.ParamsPath != "" {
			path = detail.ParamsPath
		}
		before = paramsJSONContent(detail.RequestParam)
	}
	return textDiffPreview(path, before, after)
}

// analysisForPreview 读取分析运行当前状态，用于生成更新类工具的 diff 预览。
//
// 任何失败（上下文缺项目 / ID 非法 / 未注入浏览器 / 分析不存在）都返回 ok=false，
// 由调用方退化为「无旧值」的预览；真正的错误仍由工具主体返回给模型。
func analysisForPreview(ctx context.Context, browser runtimecontext.AnalysisBrowser, toolName, rawAnalysisID string) (runtimecontext.AnalysisDetail, bool) {
	projectID, err := requireProjectID(ctx, toolName)
	if err != nil {
		return runtimecontext.AnalysisDetail{}, false
	}
	analysisID, err := parseAnalysisID(rawAnalysisID)
	if err != nil {
		return runtimecontext.AnalysisDetail{}, false
	}
	if browser == nil {
		return runtimecontext.AnalysisDetail{}, false
	}
	detail, err := browser.GetAnalysisByID(ctx, projectID, analysisID)
	if err != nil {
		return runtimecontext.AnalysisDetail{}, false
	}
	return detail, true
}
