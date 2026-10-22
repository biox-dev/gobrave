package agentanalysis

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/analysisform"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/dag"
	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/dag/prepare"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// analysisNodeRunner 实现 tool.AnalysisNodeRunner：把「运行分析节点并等待完成、读取日志」
// 的业务能力暴露给内置工具 run_analysis_node。
//
// 与 AnalysisHandler.RunAnalysisNodeWithID / LLMHandler.runAnalysisNodeByID 语义一致：
// 按最新 io_schema 刷新 patterns → 重置节点状态 → 提交异步执行 → **订阅节点完成事件**
// 等待终态 → 读取 command.log 内容。
//
// 等待终态不再依赖高频轮询：NodeCompletionCoordinator 在完成节点后会通过共享 event.Bus
// 发布 dag.node.completed / dag.node.failed（见 publishNodeResult），这里经进程级唯一的
// EventRouter 订阅这些事件，事件到达即读取节点终态。为兜底「事件已发布但本 runner 尚未
// 注册 sink」以及订阅之外的异常路径，仍保留一个低频（见 reconcileInterval）状态兜底轮询。
type analysisNodeRunner struct {
	analysisSvc      interfaces.AnalysisService
	nodeOrchestrator interfaces.NodeOrchestrator
	// workflowSvc 用于读取脚本 latest io_schema.json（解析 request_param 与刷新 patterns）。
	workflowSvc interfaces.WorkflowService
	// analysisRepo / dataSvc / preparer 用于复用 applyAnalysisNodeParams 解析参数并生成
	// params.json（与 build_analysis_node_params 工具同一实现，不经过 paramsBuilder 工具层）。
	analysisRepo interfaces.AnalysisRepository
	dataSvc      interfaces.DataService
	preparer     prepare.NodeRuntimePreparer
	// router 是进程级运行时事件路由器；订阅它即可收到 NodeCompletionCoordinator
	// publishNodeResult 发出的节点完成 / 失败事件。为 nil 时退化为纯状态轮询。
	router *dagruntime.EventRouter
	// cfg 用于推导节点工作目录（与保存独立节点同口径）。
	cfg *config.Config

	// pollInterval 是状态兜底轮询的间隔；正常路径由事件唤醒，轮询仅用于兜底。
	pollInterval time.Duration
	// defaultTimeout 是未显式指定超时时的默认等待时长（默认 10 分钟）。
	defaultTimeout time.Duration
	// maxLogBytes 是回传日志的最大字节数（超出时截断保留末尾，默认 12000）。
	maxLogBytes int
}

// reconcileInterval 是订阅到事件之外的兜底状态轮询间隔。事件路径负责及时唤醒，
// 这个间隔只用于事件丢失 / 订阅注册与运行存在竞态时的兜底，因此可以放得很长。
const reconcileInterval = 5 * time.Minute

// NewNodeRunner 构建分析节点运行器并注入内置工具体系。
func NewNodeRunner(
	analysisSvc interfaces.AnalysisService,
	nodeOrchestrator interfaces.NodeOrchestrator,
	workflowSvc interfaces.WorkflowService,
	analysisRepo interfaces.AnalysisRepository,
	dataSvc interfaces.DataService,
	preparer prepare.NodeRuntimePreparer,
	router *dagruntime.EventRouter,
	cfg *config.Config,
) tool.AnalysisNodeRunner {
	return &analysisNodeRunner{
		analysisSvc:      analysisSvc,
		nodeOrchestrator: nodeOrchestrator,
		workflowSvc:      workflowSvc,
		analysisRepo:     analysisRepo,
		dataSvc:          dataSvc,
		preparer:         preparer,
		router:           router,
		cfg:              cfg,
		pollInterval:     reconcileInterval,
		defaultTimeout:   60 * time.Minute,
		maxLogBytes:      12000,
	}
}

// RunAnalysisNode 触发分析节点执行并阻塞等待其进入终态，返回执行日志内容。
//
// 运行前解析本次参数：requestParam 非空时以它为准，否则回退到节点已保存的
// request_param；两者都为空则直接报错。随后按脚本最新的 io_schema.json 解析，并把
// request_param / resolved_inputs / inputs_patterns / output_patterns 一次性回写节点、
// 清空上一轮的 input_validation_errors / output_validation_errors，再重新生成
// params.json 并清空节点输出目录。解析失败则不提交运行（避免用过期 / 空参数跑出误导性结果）。
func (r *analysisNodeRunner) RunAnalysisNode(ctx context.Context, nodeID int64, requestParam map[string]any, timeoutSeconds int) (result tool.AnalysisNodeRunResult, runErr error) {
	if nodeID <= 0 {
		return tool.AnalysisNodeRunResult{NodeID: nodeID}, fmt.Errorf("invalid analysis node id")
	}
	if r.nodeOrchestrator == nil {
		return tool.AnalysisNodeRunResult{NodeID: nodeID}, fmt.Errorf("node orchestrator is not configured")
	}

	timeout := r.defaultTimeout
	if timeoutSeconds > 0 {
		timeout = time.Duration(timeoutSeconds) * time.Second
	}

	node, err := r.analysisSvc.GetAnalysisNodeByID(ctx, nodeID)
	if err != nil {
		return tool.AnalysisNodeRunResult{NodeID: nodeID}, fmt.Errorf("failed to get analysis node: %w", err)
	}

	// 节点已取到：把绑定的 script_id 统一回填到所有返回分支（含错误 / 超时分支），
	// 避免调用方只拿到日志却不知道对应哪个脚本。
	scriptID := node.ScriptID
	defer func() {
		if result.ScriptID == 0 {
			result.ScriptID = scriptID
		}
	}()

	// 防重复提交：节点已在运行中时直接拒绝，避免并发触发。
	if node.Status == dag.StatusRunning || node.Status == dag.StatusSubmitted || node.Status == dag.StatusStopping {
		return tool.AnalysisNodeRunResult{
			NodeID:       nodeID,
			Status:       node.Status,
			Log:          readAnalysisNodeLog(node.LogPath, r.maxLogBytes),
			WorkspaceDir: node.WorkspaceDir,
			CommandPath:  node.CommandPath,
			ParamsPath:   node.ParamsPath,
			LogPath:      node.LogPath,
			ParamsJSON:   readParamsJSON(node.ParamsPath),
			RequestParam: DecodeRequestParam(node.RequestParam),
		}, fmt.Errorf("analysis node is already %s", node.Status)
	}

	// 命令节点（Agent 创建、无脚本）没有 io_schema / request_param：run.sh 已由创建 /
	// 更新入口写入节点目录，这里只重建目录 / params.json / env.sh，然后走通用节点链路运行。
	if node.IsCommandNode() {
		if len(requestParam) > 0 {
			return tool.AnalysisNodeRunResult{NodeID: nodeID}, fmt.Errorf("analysis node %d is a command node and does not accept request_param", nodeID)
		}
		if r.preparer == nil {
			return tool.AnalysisNodeRunResult{NodeID: nodeID}, fmt.Errorf("node runtime preparer is not configured")
		}
		node.HydrateDerivedPaths()
		// 不携带 prepare.WithSkipCleanOutput：运行前清空 output，避免上一轮残留文件
		// 被当作本轮结果（尤其是命令节点输出校验 / 后续节点读取）。
		if err := r.preparer.Prepare(ctx, node); err != nil {
			return tool.AnalysisNodeRunResult{
				NodeID:       nodeID,
				Status:       "params_failed",
				WorkspaceDir: node.WorkspaceDir,
				CommandPath:  node.CommandPath,
				ParamsPath:   node.ParamsPath,
			}, fmt.Errorf("failed to prepare command node: %w", err)
		}
		return r.resetAndRun(ctx, node, timeout)
	}

	// 1) 确定本次运行的 request_param：入参优先，为空时回退到节点已保存的；两者都空则
	// 直接报错（最终 request_param 不能为空，避免用空参数跑出误导性结果）。
	effectiveRequestParam := requestParam
	if len(effectiveRequestParam) == 0 {
		effectiveRequestParam = DecodeRequestParam(node.RequestParam)
	}
	if len(effectiveRequestParam) == 0 {
		return tool.AnalysisNodeRunResult{
			NodeID:       nodeID,
			Status:       "params_failed",
			WorkspaceDir: node.WorkspaceDir,
			CommandPath:  node.CommandPath,
			ParamsPath:   node.ParamsPath,
			LogPath:      node.LogPath,
		}, fmt.Errorf("request_param is required: call build_analysis_node_params to save node params before running")
	}

	// 2) 读取脚本，按表单把 request_param 解析成节点记录（patterns / resolved_inputs / request_param）。
	if r.analysisRepo == nil || r.workflowSvc == nil {
		return tool.AnalysisNodeRunResult{NodeID: nodeID}, fmt.Errorf("required services are not configured")
	}

	script, err := r.workflowSvc.GetScriptByID(ctx, node.ScriptID)
	if err != nil {
		return tool.AnalysisNodeRunResult{NodeID: nodeID}, fmt.Errorf("failed to load script %d: %w", node.ScriptID, err)
	}

	// 与 create_script_analysis / 保存独立节点同一构建口径（含读取脚本表单）：只取其中的 patterns /
	// resolved_inputs / request_param 刷新到本节点，状态、工作目录等仍以节点已有记录为准。
	built, err := analysisform.NewAnalysisNodeFromScript(ctx, r.dataSvc, r.workflowSvc, script, analysisform.AnalysisNodeSpec{
		ProjectID:    node.ProjectID,
		RequestParam: effectiveRequestParam,
		NodeName:     node.NodeName,
		Executor:     node.Executor,
		BaseDir:      r.storageBaseDir(),
	})
	if err != nil {
		return tool.AnalysisNodeRunResult{
			NodeID:       nodeID,
			Status:       "params_failed",
			RequestParam: effectiveRequestParam,
		}, fmt.Errorf("failed to build params.json: %w", err)
	}
	formJSONWrap := built.ScriptFormJSON
	if len(formJSONWrap) == 0 {
		return tool.AnalysisNodeRunResult{NodeID: nodeID}, fmt.Errorf("script %d has no io_schema inputs, nothing to build", node.ScriptID)
	}
	requestParamBytes := built.Node.RequestParam
	resolvedInputs := built.Node.ResolvedInputs
	inputsPatterns := built.Node.InputsPatterns
	outputPatterns := built.Node.OutputPatterns

	// 3) 刷新节点的 patterns / resolved_inputs / request_param，并清空上一轮校验错误。
	if err := r.analysisSvc.UpdateAnalysisNodeByID(ctx, node.ID, map[string]any{
		"inputs_patterns":          inputsPatterns,
		"output_patterns":          outputPatterns,
		"resolved_inputs":          resolvedInputs,
		"request_param":            requestParamBytes,
		"input_validation_errors":  types.JSONSlice{},
		"output_validation_errors": types.JSONSlice{},
	}); err != nil {
		return tool.AnalysisNodeRunResult{NodeID: nodeID}, fmt.Errorf("failed to update analysis node %d: %w", node.ID, err)
	}

	// 4) 用刚写入的字段重新生成 params.json / run.sh / env.sh。
	if r.preparer == nil {
		return tool.AnalysisNodeRunResult{NodeID: nodeID}, fmt.Errorf("node runtime preparer is not configured")
	}
	node.InputsPatterns = inputsPatterns
	node.OutputPatterns = outputPatterns
	node.ResolvedInputs = resolvedInputs
	node.RequestParam = requestParamBytes
	node.HydrateDerivedPaths()
	// 不携带 prepare.WithSkipCleanOutput：运行前清空 output，保证本次结果不复用上一轮残留文件。
	if err := r.preparer.Prepare(ctx, node); err != nil {
		return tool.AnalysisNodeRunResult{
			NodeID:         nodeID,
			Status:         "params_failed",
			WorkspaceDir:   node.WorkspaceDir,
			CommandPath:    node.CommandPath,
			ParamsPath:     node.ParamsPath,
			RequestParam:   effectiveRequestParam,
			ResolvedInputs: resolvedInputs,
		}, fmt.Errorf("failed to build params.json: %w", err)
	}
	warnings := missingIOSchemaWarnings(formJSONWrap, effectiveRequestParam)
	if extra := unknownIOSchemaWarnings(formJSONWrap, effectiveRequestParam); len(extra) > 0 {
		warnings = append(warnings, extra...)
	}

	// 5) 重置节点状态（与 RunAnalysisNodeWithID 一致），再提交异步执行。
	result, runErr = r.resetAndRun(ctx, node, timeout)
	// 参数与提示信息以本次解析结果为准（无论成功 / 失败 / 超时都回传本次生效的参数）。
	if result.RequestParam == nil {
		result.RequestParam = effectiveRequestParam
	}
	if result.ResolvedInputs == nil {
		result.ResolvedInputs = resolvedInputs
	}
	if len(result.ParamsWarnings) == 0 {
		result.ParamsWarnings = warnings
	}
	// 参数解析阶段已清空上一轮的校验错误；这里回传本次运行前落库的快照。
	if result.InputValidationErrors == nil && len(node.InputValidationErrors) > 0 {
		result.InputValidationErrors = []any(node.InputValidationErrors)
	}
	if result.OutputValidationErrors == nil && len(node.OutputValidationErrors) > 0 {
		result.OutputValidationErrors = []any(node.OutputValidationErrors)
	}
	return result, runErr
}

// resetAndRun 把节点重置为 ready 并提交异步执行，然后等待终态。
//
// 校验错误由调用方在参数解析阶段清空，这里只负责运行态字段与状态。
func (r *analysisNodeRunner) resetAndRun(ctx context.Context, node *types.AnalysisNode, timeout time.Duration) (tool.AnalysisNodeRunResult, error) {
	if err := r.analysisSvc.UpdateAnalysisNodeByID(ctx, node.ID, map[string]any{
		"status":        dag.StatusReady,
		"server_status": "",
		"cache_hit":     false,
		"rerun_reason":  "node rerun requested by agent tool",
		"error_message": "",
		"exit_code":     0,
		"pid":           0,
		"job_id":        "",
		"started_at":    nil,
		"finished_at":   nil,
		"updated_at":    time.Now().UTC(),
	}); err != nil {
		return tool.AnalysisNodeRunResult{NodeID: node.ID}, fmt.Errorf("failed to reset analysis node status: %w", err)
	}

	if err := r.nodeOrchestrator.StartAsync(ctx, node.ID); err != nil {
		return tool.AnalysisNodeRunResult{NodeID: node.ID}, fmt.Errorf("failed to submit analysis node: %w", err)
	}

	return r.waitForCompletion(ctx, node.ID, strings.TrimSpace(node.LogPath), timeout)
}

// readParamsJSON 读取节点 params.json 正文；读取失败或为空时返回空字符串（不视为错误）。
func readParamsJSON(paramsPath string) string {
	paramsPath = strings.TrimSpace(paramsPath)
	if paramsPath == "" {
		return ""
	}
	data, err := os.ReadFile(paramsPath)
	if err != nil {
		return ""
	}
	return string(data)
}

// waitForCompletion 等待节点进入终态或超时，返回最终结果（含日志）。
//
// 终态判定有两条通道，谁先到谁先返回：
//  1. 事件（主路径）：订阅 EventRouter 上针对本节点 analysis_node_id 的
//     dag.node.completed / dag.node.failed 事件（即 NodeCompletionCoordinator.
//     publishNodeResult 发出的那两条），收到后立即读取节点并判定终态。
//  2. 状态兜底轮询（次路径）：按 pollInterval 读取节点状态，仅在事件丢失 /
//     订阅注册与运行存在竞态时兜底。
//
// 每次被唤醒都会重新读取节点并检查终态，因此事件只是「提前唤醒信号」，最终是否结束
// 仍以节点持久化状态为准，避免把事件与状态不一致的情形误判为已完成。
func (r *analysisNodeRunner) waitForCompletion(ctx context.Context, nodeID int64, logPath string, timeout time.Duration) (tool.AnalysisNodeRunResult, error) {
	deadline := time.Now().Add(timeout)

	node, err := r.analysisSvc.GetAnalysisNodeByID(ctx, nodeID)
	if err != nil {
		return tool.AnalysisNodeRunResult{NodeID: nodeID}, fmt.Errorf("failed to get analysis node: %w", err)
	}
	analysisID := node.AnalysisID
	scriptID := node.ScriptID
	// 运行期路径全部由节点的 WorkspaceDir 派生，这里先取一份，保证取消 / 超时等
	// 非终态返回也能告诉模型文件在哪。
	workspaceDir := strings.TrimSpace(node.WorkspaceDir)
	commandPath := strings.TrimSpace(node.CommandPath)
	paramsPath := strings.TrimSpace(node.ParamsPath)
	// 注意：AnalysisNode.ID 是主键（int64），而 AnalysisNode.AnalysisNodeID 是业务
	// 字符串 ID；RuntimeEvent.AnalysisNodeID 的类型是 int64，NodeCompletionCoordinator.
	// publishNodeResult 填入的正是 node.ID，因此这里也按主键匹配。
	primaryKey := node.ID

	// 订阅 NodeCompletionCoordinator.publishNodeResult 发出的节点完成 / 失败事件。
	// 过滤器只放行本节点的这两类事件，避免无关节点 / 事件唤醒本 runner。
	var (
		nodeEvents <-chan dagruntime.RuntimeEvent
		unsub      func()
	)
	if r.router != nil {
		sink := r.router.RegisterWithFilter(analysisID, func(evt dagruntime.RuntimeEvent) bool {
			if evt.AnalysisNodeID != primaryKey {
				return false
			}
			switch strings.TrimSpace(evt.Name) {
			case dagruntime.EventNodeCompleted, dagruntime.EventNodeFailed:
				return true
			default:
				return false
			}
		})
		nodeEvents = sink.Events()
		unsub = func() { r.router.Unregister(analysisID) }
		defer unsub()
	}

	ticker := time.NewTicker(r.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return tool.AnalysisNodeRunResult{
				NodeID:       nodeID,
				ScriptID:     scriptID,
				Status:       "canceled",
				Log:          readAnalysisNodeLog(logPath, r.maxLogBytes),
				WorkspaceDir: workspaceDir,
				CommandPath:  commandPath,
				ParamsPath:   paramsPath,
				LogPath:      logPath,
				ParamsJSON:   readParamsJSON(paramsPath),
			}, ctx.Err()
		case <-nodeEvents:
			// 节点完成事件到达：读取最新状态并判定终态（见下方 done 分支的兜底）。
		case <-ticker.C:
			// 兜底轮询：仅用于事件未送达时的最终判定。
		}

		node, err := r.analysisSvc.GetAnalysisNodeByID(ctx, nodeID)
		if err != nil {
			// 读取失败不应直接放弃：事件路径可能已把节点推进到终态，交由下一轮
			// 事件 / 兜底轮询重试；超时兜底仍在循环尾部生效。
			logger.Warnf(ctx, "[analysisNodeRunner] failed to get node %d: %v", nodeID, err)
		} else {
			if lp := strings.TrimSpace(node.LogPath); lp != "" {
				logPath = lp
			}
			// 节点详情里带的是已还原的绝对路径，优先以它为准（repository 读取时会 hydrate）。
			if v := strings.TrimSpace(node.WorkspaceDir); v != "" {
				workspaceDir = v
			}
			if v := strings.TrimSpace(node.CommandPath); v != "" {
				commandPath = v
			}
			if v := strings.TrimSpace(node.ParamsPath); v != "" {
				paramsPath = v
			}
			status := strings.ToLower(strings.TrimSpace(node.Status))
			if isAnalysisNodeTerminal(status) {
				log := readAnalysisNodeLog(logPath, r.maxLogBytes)
				success := isAnalysisNodeSuccess(status)
				result := tool.AnalysisNodeRunResult{
					NodeID:       nodeID,
					ScriptID:     node.ScriptID,
					Status:       node.Status,
					Log:          log,
					Success:      success,
					WorkspaceDir: node.WorkspaceDir,
					CommandPath:  node.CommandPath,
					ParamsPath:   node.ParamsPath,
					LogPath:      logPath,
					ParamsJSON:   readParamsJSON(node.ParamsPath),
				}
				if !success {
					msg := strings.TrimSpace(node.ErrorMessage)
					if msg == "" {
						msg = fmt.Sprintf("analysis node finished with status %s", node.Status)
					}
					return result, fmt.Errorf("%s", msg)
				}
				return result, nil
			}
		}

		if time.Now().After(deadline) {
			return tool.AnalysisNodeRunResult{
				NodeID:       nodeID,
				ScriptID:     scriptID,
				Status:       "timeout",
				Log:          readAnalysisNodeLog(logPath, r.maxLogBytes),
				WorkspaceDir: workspaceDir,
				CommandPath:  commandPath,
				ParamsPath:   paramsPath,
				LogPath:      logPath,
				ParamsJSON:   readParamsJSON(paramsPath),
			}, fmt.Errorf("timeout after %s waiting for analysis node to complete", timeout)
		}
	}
}

// storageBaseDir 返回存储根目录（容忍 cfg/Storage 为空）。
func (r *analysisNodeRunner) storageBaseDir() string {
	if r == nil || r.cfg == nil || r.cfg.Storage == nil {
		return ""
	}
	return strings.TrimSpace(r.cfg.Storage.BaseDir)
}

// isAnalysisNodeTerminal 判断节点状态是否为终态。
func isAnalysisNodeTerminal(status string) bool {

	switch status {
	case "done", "failed", "skipped", "cached", "stopped":
		return true
	}
	return false
}

// isAnalysisNodeSuccess 判断节点终态是否为成功。
func isAnalysisNodeSuccess(status string) bool {
	switch status {
	case "done", "cached", "skipped":
		return true
	}
	return false
}

// readAnalysisNodeLog 读取节点日志文件内容；超出 maxBytes 时截断保留末尾。
func readAnalysisNodeLog(logPath string, maxBytes int) string {
	logPath = strings.TrimSpace(logPath)
	if logPath == "" {
		return ""
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return fmt.Sprintf("[could not read log: %v]", err)
	}
	if maxBytes > 0 && len(data) > maxBytes {
		return "...(truncated)\n" + string(data[len(data)-maxBytes:])
	}
	return string(data)
}
