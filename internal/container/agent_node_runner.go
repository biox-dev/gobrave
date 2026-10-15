package container

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// analysisNodeRunner 实现 tool.AnalysisNodeRunner：把「运行分析节点并等待完成、读取日志」
// 的业务能力暴露给内置工具 run_analysis_node。
//
// 它与 AnalysisHandler.RunAnalysisNodeWithID / LLMHandler.runAnalysisNodeByID 语义一致：
// 重置节点状态 → 提交异步执行 → 轮询等待终态 → 读取 command.log 内容。
type analysisNodeRunner struct {
	analysisSvc      interfaces.AnalysisService
	nodeOrchestrator interfaces.NodeOrchestrator

	// pollInterval 是轮询节点状态的间隔（默认 3s）。
	pollInterval time.Duration
	// defaultTimeout 是未显式指定超时时的默认等待时长（默认 10 分钟）。
	defaultTimeout time.Duration
	// maxLogBytes 是回传日志的最大字节数（超出时截断保留末尾，默认 12000）。
	maxLogBytes int
}

// newAnalysisNodeRunner 构建分析节点运行器并注入内置工具体系。
func newAnalysisNodeRunner(
	analysisSvc interfaces.AnalysisService,
	nodeOrchestrator interfaces.NodeOrchestrator,
) tool.AnalysisNodeRunner {
	return &analysisNodeRunner{
		analysisSvc:      analysisSvc,
		nodeOrchestrator: nodeOrchestrator,
		pollInterval:     3 * time.Second,
		defaultTimeout:   10 * time.Minute,
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

// waitForCompletion 轮询节点状态直至终态或超时，返回最终结果（含日志）。
func (r *analysisNodeRunner) waitForCompletion(ctx context.Context, nodeID int64, logPath string, timeout time.Duration) (tool.AnalysisNodeRunResult, error) {
	deadline := time.Now().Add(timeout)
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
		case <-ticker.C:
			node, err := r.analysisSvc.GetAnalysisNodeByID(ctx, nodeID)
			if err != nil {
				logger.Warnf(ctx, "[analysisNodeRunner] failed to get node %d: %v", nodeID, err)
				continue
			}
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

			if time.Now().After(deadline) {
				return tool.AnalysisNodeRunResult{
					NodeID: nodeID,
					Status: "timeout",
					Log:    readAnalysisNodeLog(logPath, r.maxLogBytes),
				}, fmt.Errorf("timeout after %s waiting for analysis node to complete", timeout)
			}
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
