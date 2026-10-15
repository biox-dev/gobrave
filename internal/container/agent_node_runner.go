package container

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/dag"
	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// analysisNodeRunner 实现 tool.AnalysisNodeRunner：把「运行分析节点并等待完成、读取日志」
// 的业务能力暴露给内置工具 run_analysis_node。
//
// 它与 AnalysisHandler.RunAnalysisNodeWithID / LLMHandler.runAnalysisNodeByID 语义一致：
// 重置节点状态 → 提交异步执行 → **订阅节点完成事件**等待终态 → 读取 command.log 内容。
//
// 等待终态不再依赖高频轮询：NodeCompletionCoordinator 在完成节点后会通过共享 event.Bus
// 发布 dag.node.completed / dag.node.failed（见 publishNodeResult），这里经进程级唯一的
// EventRouter 订阅这些事件，事件到达即读取节点终态。为兜底「事件已发布但本 runner 尚未
// 注册 sink」以及订阅之外的异常路径，仍保留一个低频（见 reconcileInterval）状态兜底轮询。
type analysisNodeRunner struct {
	analysisSvc      interfaces.AnalysisService
	nodeOrchestrator interfaces.NodeOrchestrator
	// router 是进程级运行时事件路由器；订阅它即可收到 NodeCompletionCoordinator
	// publishNodeResult 发出的节点完成 / 失败事件。为 nil 时退化为纯状态轮询。
	router *dagruntime.EventRouter

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

// newAnalysisNodeRunner 构建分析节点运行器并注入内置工具体系。
func newAnalysisNodeRunner(
	analysisSvc interfaces.AnalysisService,
	nodeOrchestrator interfaces.NodeOrchestrator,
	router *dagruntime.EventRouter,
) tool.AnalysisNodeRunner {
	return &analysisNodeRunner{
		analysisSvc:      analysisSvc,
		nodeOrchestrator: nodeOrchestrator,
		router:           router,
		pollInterval:     reconcileInterval,
		defaultTimeout:   60 * time.Minute,
		maxLogBytes:      12000,
	}
}

// RunAnalysisNode 触发分析节点执行并阻塞等待其进入终态，返回执行日志内容。
func (r *analysisNodeRunner) RunAnalysisNode(ctx context.Context, nodeID int64, timeoutSeconds int) (tool.AnalysisNodeRunResult, error) {
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

	// 防重复提交：节点已在运行中时直接拒绝，避免并发触发。
	if node.Status == dag.StatusRunning || node.Status == dag.StatusSubmitted || node.Status == dag.StatusStopping {
		return tool.AnalysisNodeRunResult{
			NodeID: nodeID,
			Status: node.Status,
			Log:    readAnalysisNodeLog(node.LogPath, r.maxLogBytes),
		}, fmt.Errorf("analysis node is already %s", node.Status)
	}

	// 重置节点状态（与 RunAnalysisNodeWithID 一致），再提交异步执行。
	if err := r.analysisSvc.UpdateAnalysisNodeByID(ctx, node.ID, map[string]any{
		"status":                   dag.StatusReady,
		"server_status":            "",
		"cache_hit":                false,
		"rerun_reason":             "node rerun requested by agent tool",
		"error_message":            "",
		"exit_code":                0,
		"pid":                      0,
		"job_id":                   "",
		"started_at":               nil,
		"finished_at":              nil,
		"output_validation_errors": types.JSONSlice{},
		"updated_at":               time.Now().UTC(),
	}); err != nil {
		return tool.AnalysisNodeRunResult{NodeID: nodeID}, fmt.Errorf("failed to reset analysis node status: %w", err)
	}

	if err := r.nodeOrchestrator.StartAsync(ctx, nodeID); err != nil {
		return tool.AnalysisNodeRunResult{NodeID: nodeID}, fmt.Errorf("failed to submit analysis node: %w", err)
	}

	logPath := strings.TrimSpace(node.LogPath)
	return r.waitForCompletion(ctx, nodeID, logPath, timeout)
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
				NodeID: nodeID,
				Status: "canceled",
				Log:    readAnalysisNodeLog(logPath, r.maxLogBytes),
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
			status := strings.ToLower(strings.TrimSpace(node.Status))
			if isAnalysisNodeTerminal(status) {
				log := readAnalysisNodeLog(logPath, r.maxLogBytes)
				success := isAnalysisNodeSuccess(status)
				result := tool.AnalysisNodeRunResult{
					NodeID:  nodeID,
					Status:  node.Status,
					Log:     log,
					Success: success,
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
				NodeID: nodeID,
				Status: "timeout",
				Log:    readAnalysisNodeLog(logPath, r.maxLogBytes),
			}, fmt.Errorf("timeout after %s waiting for analysis node to complete", timeout)
		}
	}
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
