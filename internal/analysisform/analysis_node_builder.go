package analysisform

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/google/uuid"
)

// NodeCreationSource 是分析节点的来源标记（analysis_nodes.creation_source）。
//
// 节点类型只有三种，统一引用 types 中的规范取值，避免各处再写字面量：
//   - agent：Agent 会话直接创建的命令节点（无脚本）；
//   - script：由脚本契约创建的节点（前端独立节点 / create_script_analysis）；
//   - scheduler：由 DAG 调度器按流程物化的节点。
const (
	// NodeCreationSourceAgent 是 Agent 会话（create_agent_analysis）直接创建的命令节点。
	NodeCreationSourceAgent = types.AnalysisNodeCreationSourceAgent
	// NodeCreationSourceScript 是由脚本契约创建的节点（前端独立节点 / create_script_analysis）。
	NodeCreationSourceScript = types.AnalysisNodeCreationSourceScript
	// NodeCreationSourceScheduler 是 DAG 调度器物化的节点。
	NodeCreationSourceScheduler = types.AnalysisNodeCreationSourceScheduler
)

// nodeExecutionStatusReady 与 dagruntime.StatusReady 同名同值。
//
// analysisform 被 dag 的下游（prepare 等）引用，直接 import dag 会形成循环，故在此复用字面量。
const nodeExecutionStatusReady = "ready"

// AnalysisNodeSpec 描述「由脚本契约构建一个独立运行的分析节点」所需的事实。
//
// 只承载入参事实，路径 / 主键 / 状态等派生字段由 NewAnalysisNodeFromScript 统一生成，
// 保证前端保存、Agent 创建与工具运行三处口径一致。
type AnalysisNodeSpec struct {
	// ProjectID 是节点所属项目（必须为正数）。
	ProjectID int64
	// Script 是节点绑定的脚本；只有 ID / ProjectID / ScriptID / NodeName 会被读取。
	Script *types.Script
	// RequestParam 是请求参数；为空时只按脚本 io_schema 刷新 patterns，不解析 resolved_inputs。
	RequestParam map[string]any
	// NodeName 为空时回落到脚本名称。
	NodeName string
	// Executor 为空时为 local。
	Executor string
	// CreationSource 是节点来源（agent / script / scheduler），为空时按 script。
	CreationSource string
	// BaseDir 是存储根目录，用于推导节点工作目录（为空时用 "."）。
	BaseDir string
}

// AnalysisNodeBuildResult 是「由脚本构建分析节点」的产物。
//
// 除节点本身外，把构建时用到的脚本表单一并回传：调用方通常还要用它做参数校验提示
// （如缺必填项 / 出现 io_schema 未声明的键），避免再查一次脚本表单。
type AnalysisNodeBuildResult struct {
	Node *types.AnalysisNode
	// ScriptFormJSON 是脚本表单（io_schema.json 的 inputs），用于 BuildParseAnalysisResult。
	ScriptFormJSON []any
	// ScriptIOSchema 是完整的 io_schema.json。
	ScriptIOSchema map[string]any
}

// NewAnalysisNodeFromScript 由脚本对象 + 请求参数构建一个尚未落库的独立分析节点。
//
// 与「保存独立节点」同一口径：
//   - 节点工作目录为 <base_dir>/data/<project>/analysis_node/<script_id>/<node_id>，其余路径由它派生；
//   - inputs_patterns / output_patterns 取自脚本最新 io_schema.json；
//   - request_param 非空时按脚本表单解析出 resolved_inputs，并写入 request_param（含 analysis_node_id），
//     使得节点落库后即可被 NodeRuntimePreparer 生成 params.json。
//
// 脚本表单由内部调用 workflowService.GetScriptFormJSONByScript 读取（调用方手上已有脚本对象，
// 无需再按 ID 查一次库）。
func NewAnalysisNodeFromScript(
	ctx context.Context,
	dataService interfaces.DataService,
	workflowService interfaces.WorkflowService,
	script *types.Script,
	spec AnalysisNodeSpec,
) (AnalysisNodeBuildResult, error) {
	if script == nil || script.ID <= 0 {
		return AnalysisNodeBuildResult{}, fmt.Errorf("script is required to build an analysis node")
	}
	if spec.ProjectID <= 0 {
		// 调用方未显式指定项目时按脚本归属项目处理。
		spec.ProjectID = script.ProjectID
	}

	scriptFormJSON, scriptIOSchema, err := workflowService.GetScriptFormJSONByScript(ctx, script)
	if err != nil {
		return AnalysisNodeBuildResult{}, fmt.Errorf("failed to read script io_schema.json: %w", err)
	}
	result := AnalysisNodeBuildResult{ScriptFormJSON: scriptFormJSON, ScriptIOSchema: scriptIOSchema}

	requestParam := spec.RequestParam
	if requestParam == nil {
		requestParam = map[string]any{}
	}

	var resolvedInputs map[string]any
	if len(requestParam) > 0 {
		built, err := BuildParseAnalysisResult(ctx, dataService, requestParam, scriptFormJSON)
		if err != nil {
			return result, fmt.Errorf("failed to parse request_param with script io_schema: %w", err)
		}
		resolvedInputs = built
	}

	nodeName := strings.TrimSpace(spec.NodeName)
	if nodeName == "" {
		nodeName = strings.TrimSpace(script.Name)
	}
	executor := strings.TrimSpace(spec.Executor)
	if executor == "" {
		executor = "local"
	}
	creationSource := strings.TrimSpace(spec.CreationSource)
	if creationSource == "" {
		// 由脚本构建的节点默认归为 script 类型。
		creationSource = NodeCreationSourceScript
	}

	node := &types.AnalysisNode{
		ID:                     int64(utils.GenerateID()),
		AnalysisNodeID:         "node-" + uuid.NewString(),
		ProjectID:              spec.ProjectID,
		NodeID:                 "node-" + uuid.NewString(),
		NodeName:               nodeName,
		ScriptID:               script.ID,
		InputsPatterns:         types.JSONMap(utils.FormatIOSchemaItems(scriptIOSchema["inputs"])),
		OutputPatterns:         types.JSONMap(utils.FormatIOSchemaItems(scriptIOSchema["outputs"])),
		ResolvedInputs:         types.JSONMap{},
		ResolvedOutputs:        types.JSONMap{},
		RequestParam:           "{}",
		Status:                 nodeExecutionStatusReady,
		Executor:               executor,
		Retry:                  0,
		MaxRetry:               3,
		UpstreamIDs:            types.JSONSlice{},
		DownstreamIDs:          types.JSONSlice{},
		InputValidationErrors:  types.JSONSlice{},
		OutputValidationErrors: types.JSONSlice{},
		CreationSource:         creationSource,
	}
	baseDir := strings.TrimSpace(spec.BaseDir)
	if baseDir == "" {
		baseDir = "."
	}
	node.WorkspaceDir = joinNodeWorkspace(baseDir, spec.ProjectID, script.ID, node.ID)
	node.HydrateDerivedPaths()

	if resolvedInputs != nil {
		node.ResolvedInputs = types.JSONMap(cloneAnyMap(resolvedInputs))
		// 与保存独立节点一致：把节点主键写回 request_param，脚本侧可据此定位节点。
		requestParam["analysis_node_id"] = node.ID
	}
	if len(requestParam) > 0 {
		raw, err := json.Marshal(requestParam)
		if err != nil {
			return result, fmt.Errorf("failed to serialize request_param: %w", err)
		}
		node.RequestParam = string(raw)
	}
	result.Node = node
	return result, nil
}

// CommandNodeSpec 描述「由 Agent 会话构建一个命令型分析节点」所需的事实。
//
// 命令节点没有脚本：容器模板与 run.sh 正文都由节点自身承载，因此不需要 io_schema。
// 注意：Command 不再落库（AnalysisNode 没有 Command 字段），这里只做非空校验；
// 调用方拿到节点后需自行调用 prepare.WriteCommandRunScript 写入 run.sh。
type CommandNodeSpec struct {
	// ProjectID 是节点所属项目（必须为正数）。
	ProjectID int64
	// ContainerTemplateID 是该节点使用的容器模板（go_container_template_definition 主键）。
	ContainerTemplateID int64
	// Command 是待写入 run.sh 的 shell 命令（仅用于校验）。
	Command string
	// NodeName 是节点名称，为空时回落到 "agent-analysis"。
	NodeName string
	// BaseDir 是存储根目录（为空时用 "."）。
	BaseDir string
}

// NewAnalysisNodeFromCommand 由 Agent 会话的「容器模板 + 命令」构建一个尚未落库的命令节点。
//
// 与脚本节点共用同一套派生口径：工作目录仍为 joinNodeWorkspace(...)，其余路径由它派生，
// 状态 ready、executor container，运行 / 停止完全复用通用节点链路。
func NewAnalysisNodeFromCommand(spec CommandNodeSpec) (*types.AnalysisNode, error) {
	if spec.ProjectID <= 0 {
		return nil, fmt.Errorf("project id is required")
	}
	if spec.ContainerTemplateID <= 0 {
		return nil, fmt.Errorf("container_template_id is required")
	}
	command := strings.TrimSpace(spec.Command)
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}
	nodeName := strings.TrimSpace(spec.NodeName)
	if nodeName == "" {
		nodeName = "agent-analysis"
	}

	node := &types.AnalysisNode{
		ID:                     int64(utils.GenerateID()),
		AnalysisNodeID:         "node-" + uuid.NewString(),
		ProjectID:              spec.ProjectID,
		NodeID:                 "node-" + uuid.NewString(),
		NodeName:               nodeName,
		ContainerTemplateID:    spec.ContainerTemplateID,
		InputsPatterns:         types.JSONMap{},
		OutputPatterns:         types.JSONMap{},
		ResolvedInputs:         types.JSONMap{},
		ResolvedOutputs:        types.JSONMap{},
		RequestParam:           "{}",
		Status:                 nodeExecutionStatusReady,
		Executor:               "container",
		Retry:                  0,
		MaxRetry:               3,
		UpstreamIDs:            types.JSONSlice{},
		DownstreamIDs:          types.JSONSlice{},
		InputValidationErrors:  types.JSONSlice{},
		OutputValidationErrors: types.JSONSlice{},
		// 命令节点由 Agent 会话直接创建，类型固定为 agent。
		CreationSource: NodeCreationSourceAgent,
	}
	baseDir := strings.TrimSpace(spec.BaseDir)
	if baseDir == "" {
		baseDir = "."
	}
	node.WorkspaceDir = utils.GetAgentNodeDir(baseDir, spec.ProjectID, node.ID)
	node.HydrateDerivedPaths()
	return node, nil
}

// joinNodeWorkspace 返回 <base_dir>/data/<project>/analysis_node/<script_id>/<node_id>。
func joinNodeWorkspace(baseDir string, projectID int64, scriptID int64, nodeID int64) string {
	return filepath.Join(utils.GetAnalysisNodeDir(baseDir, projectID, strconv.FormatInt(scriptID, 10)), strconv.FormatInt(nodeID, 10))
}

// func joinAgentNodeWorkspace(baseDir string, projectID int64, , nodeID int64) string {
// 	return filepath.Join(utils.GetAnalysisNodeDir(baseDir, projectID, strconv.FormatInt(scriptID, 10)), strconv.FormatInt(nodeID, 10))
// }

// cloneAnyMap 复制一层 map，避免节点记录与解析结果共享同一个底层映射。
func cloneAnyMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
