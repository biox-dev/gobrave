package agentanalysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/analysisform"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/dag/prepare"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

// analysisNodeParamsBuilder 实现 tool.AnalysisNodeParamsBuilder：把「按脚本 io_schema
// 解析请求参数 → 刷新节点记录 → 生成 params.json」的业务能力暴露给内置工具
// build_analysis_node_params。
//
// 它复用与保存 / 运行节点完全相同的两段逻辑，避免参数解析口径出现第二份实现：
//   - analysisform.BuildParseAnalysisResult：把 request_param 依据脚本表单
//     （io_schema.json 的 inputs）解析成结构化配方，正是 handler 保存分析节点时用的函数；
//   - prepare.NodeRuntimePreparer.Prepare：把配方写成 params.json / run.sh / env.sh
//     并软链脚本，正是 run_analysis_node 运行时读取的输入。
//
// 因此模型在此工具里通过校验的参数，就是 run_analysis_node 实际运行使用的参数。
type analysisNodeParamsBuilder struct {
	analysisSvc  interfaces.AnalysisService
	analysisRepo interfaces.AnalysisRepository
	workflowSvc  interfaces.WorkflowService
	dataSvc      interfaces.DataService
	preparer     prepare.NodeRuntimePreparer
	cfg          *config.Config
}

// NewParamsBuilder 构建「生成分析节点参数」执行器并注入内置工具体系。
func NewParamsBuilder(
	analysisSvc interfaces.AnalysisService,
	analysisRepo interfaces.AnalysisRepository,
	workflowSvc interfaces.WorkflowService,
	dataSvc interfaces.DataService,
	preparer prepare.NodeRuntimePreparer,
	cfg *config.Config,
) tool.AnalysisNodeParamsBuilder {
	return &analysisNodeParamsBuilder{
		analysisSvc:  analysisSvc,
		analysisRepo: analysisRepo,
		workflowSvc:  workflowSvc,
		dataSvc:      dataSvc,
		preparer:     preparer,
		cfg:          cfg,
	}
}

// BuildAnalysisNodeParams 为指定分析节点生成运行期参数文件并落库。
//
// 步骤：
//  1. 读取节点（校验归属项目，避免跨项目写入）；
//  2. 读取脚本表单（io_schema.inputs），按表单解析 request_param；
//  3. 刷新节点的 inputs_patterns / output_patterns / resolved_inputs / request_param，
//     并清空上一次运行的校验错误，保证「参数已变更 → 可直接重新运行」；
//  4. 调用 NodeRuntimePreparer.Prepare 写出 params.json / run.sh / env.sh；
//  5. 回读 params.json，把路径与实际内容一起回传给模型。
//
// request_param 必须非空。
func (b *analysisNodeParamsBuilder) BuildAnalysisNodeParams(ctx context.Context, in tool.AnalysisNodeParamsInput) (tool.AnalysisNodeParamsResult, error) {
	result := tool.AnalysisNodeParamsResult{AnalysisNodeID: in.AnalysisNodeID}

	if in.AnalysisNodeID <= 0 {
		return result, fmt.Errorf("analysis node id is required")
	}
	if len(in.RequestParam) == 0 {
		return result, fmt.Errorf("request_param is required and must be a non-empty object")
	}
	if b.analysisRepo == nil || b.workflowSvc == nil {
		return result, fmt.Errorf("required services are not configured")
	}

	node, err := b.analysisRepo.GetAnalysisNodeByID(ctx, in.AnalysisNodeID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return result, fmt.Errorf("analysis node %d not found", in.AnalysisNodeID)
		}
		return result, fmt.Errorf("failed to get analysis node %d: %w", in.AnalysisNodeID, err)
	}
	if node == nil {
		return result, fmt.Errorf("analysis node %d not found", in.AnalysisNodeID)
	}
	result.ScriptID = node.ScriptID

	// 项目归属校验：模型不应把参数写到其它项目的节点上。
	if in.ProjectID > 0 && node.ProjectID > 0 && in.ProjectID != node.ProjectID {
		return result, fmt.Errorf("analysis node %d belongs to project %d, not the active project %d", node.ID, node.ProjectID, in.ProjectID)
	}

	if node.ScriptID <= 0 {
		return result, fmt.Errorf("analysis node %d has no script bound", node.ID)
	}

	formJSONWrap, scriptIOSchema, err := b.workflowSvc.GetScriptFormJSONByID(ctx, node.ScriptID)
	if err != nil {
		return result, fmt.Errorf("failed to load script form (io_schema.json): %w", err)
	}
	if len(formJSONWrap) == 0 {
		return result, fmt.Errorf("script %d has no io_schema inputs, nothing to build", node.ScriptID)
	}

	// 与 handler 保存 / 运行节点一致：按脚本表单解析 request_param。
	resolvedInputs, err := analysisform.BuildParseAnalysisResult(ctx, b.dataSvc, in.RequestParam, formJSONWrap)
	if err != nil {
		return result, fmt.Errorf("failed to parse request_param with script io_schema: %w", err)
	}
	if resolvedInputs == nil {
		resolvedInputs = map[string]any{}
	}

	result.Warnings = missingIOSchemaWarnings(formJSONWrap, in.RequestParam)
	if extra := unknownIOSchemaWarnings(formJSONWrap, in.RequestParam); len(extra) > 0 {
		result.Warnings = append(result.Warnings, extra...)
	}

	// 刷新节点的输入 / 输出模式与解析结果，并记下本次 request_param：
	// run_analysis_node 运行时按这些字段重新派生 params.json 的公共参数。
	requestParamBytes, err := json.Marshal(in.RequestParam)
	if err != nil {
		return result, fmt.Errorf("failed to serialize request_param: %w", err)
	}
	update := map[string]any{
		"inputs_patterns":          types.JSONMap(utils.FormatIOSchemaItems(scriptIOSchema["inputs"])),
		"output_patterns":          types.JSONMap(utils.FormatIOSchemaItems(scriptIOSchema["outputs"])),
		"resolved_inputs":          types.JSONMap(cloneParamsMap(resolvedInputs)),
		"request_param":            string(requestParamBytes),
		"input_validation_errors":  types.JSONSlice{},
		"output_validation_errors": types.JSONSlice{},
	}
	if err := b.analysisRepo.UpdateAnalysisNodeByID(ctx, node.ID, update); err != nil {
		return result, fmt.Errorf("failed to update analysis node %d: %w", node.ID, err)
	}

	// 让内存中的节点与刚写入的字段一致，Prepare 会依据它们构建 params.json。
	node.InputsPatterns = types.JSONMap(utils.FormatIOSchemaItems(scriptIOSchema["inputs"]))
	node.OutputPatterns = types.JSONMap(utils.FormatIOSchemaItems(scriptIOSchema["outputs"]))
	node.ResolvedInputs = types.JSONMap(cloneParamsMap(resolvedInputs))
	node.RequestParam = string(requestParamBytes)

	if b.preparer == nil {
		return result, fmt.Errorf("node runtime preparer is not configured")
	}
	// 派生路径由 WorkspaceDir 推导；Prepare 内部还会按需补齐缺失的路径。
	node.HydrateDerivedPaths()
	if err := b.preparer.Prepare(prepare.WithSkipCleanOutput(ctx), node); err != nil {
		return result, fmt.Errorf("failed to prepare node runtime files: %w", err)
	}

	result.WorkspaceDir = node.WorkspaceDir
	result.ParamsPath = node.ParamsPath
	result.CommandPath = node.CommandPath
	result.ResolvedInputs = resolvedInputs

	// 回读刚写出的 params.json：模型据此确认参数映射结果，无需再读盘。
	raw, err := os.ReadFile(node.ParamsPath)
	if err != nil {
		return result, fmt.Errorf("failed to read generated params.json %s: %w", node.ParamsPath, err)
	}
	result.ParamsJSON = string(raw)

	return result, nil
}

// unknownIOSchemaWarnings 汇总「request_param 中给出、但 io_schema inputs 里没有」的键名。//
// 这些键会在解析时被忽略（不会进入 params.json），脚本运行后按该名取参数会取不到值，
// 因此必须以 warning 形式明确告知模型，而不是静默丢弃。
func unknownIOSchemaWarnings(formJSONWrap []any, requestParam map[string]any) []string {
	if len(requestParam) == 0 {
		return nil
	}
	known := make(map[string]struct{}, len(formJSONWrap))
	for _, itemAny := range formJSONWrap {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		if itemType, _ := item["type"].(string); strings.EqualFold(strings.TrimSpace(itemType), "Divider") {
			continue
		}
		name := strings.TrimSpace(fmt.Sprintf("%v", item["name"]))
		if name == "" || name == "<nil>" {
			continue
		}
		known[name] = struct{}{}
	}

	unknown := make([]string, 0)
	for key := range requestParam {
		if _, ok := known[key]; ok {
			continue
		}
		unknown = append(unknown, key)
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)
	return []string{fmt.Sprintf("request_param contains key(s) not declared in script io_schema inputs: %s; they are ignored", strings.Join(unknown, ", "))}
}

// cloneParamsMap 浅拷贝一层 map，避免把解析结果与节点字段共享同一个底层 map。
func cloneParamsMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// missingIOSchemaWarnings 汇总「io_schema 里声明为必填、但 request_param 未提供」的输入名。//
// 只做提示不做拦截：部分输入依赖上游解析（如 SelectAssay 的 files 句柄），
// 也可能由分析级 params.json 兜底，因此缺失不一定是错误。
func missingIOSchemaWarnings(formJSONWrap []any, requestParam map[string]any) []string {
	missing := make([]string, 0)
	for _, itemAny := range formJSONWrap {
		item, ok := itemAny.(map[string]any)
		if !ok {
			continue
		}
		if itemType, _ := item["type"].(string); strings.EqualFold(strings.TrimSpace(itemType), "Divider") {
			continue
		}
		name := strings.TrimSpace(fmt.Sprintf("%v", item["name"]))
		if name == "" || name == "<nil>" {
			continue
		}
		required, _ := item["required"].(bool)
		if !required {
			continue
		}
		if _, exists := requestParam[name]; exists {
			continue
		}
		missing = append(missing, name)
	}
	if len(missing) == 0 {
		return nil
	}
	sort.Strings(missing)
	return []string{fmt.Sprintf("request_param is missing required io_schema input(s): %s", strings.Join(missing, ", "))}
}
