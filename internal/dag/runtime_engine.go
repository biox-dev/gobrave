package dag

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"gorm.io/gorm"
)

type RuntimeSnapshot struct {
	AnalysisID        int64          `json:"analysis_id,string"`
	TotalNodes        int            `json:"total_nodes"`
	StatusCount       map[string]int `json:"status_count"`
	CompletedCount    int            `json:"completed_count"`
	CompletionPercent int            `json:"completion_percent"`
	ReadyCount        int            `json:"ready_count"`
	RunningCount      int            `json:"running_count"`
	IsFinished        bool           `json:"is_finished"`
}

// NodeRequestParser 把节点的 request_param（原始输入，例如
// `{"handle": {"files": "<id>"}}`）依据脚本表单解析成结构化配方，与静态分析保存时
// 的解析结果一致（handler 侧复用 analysisform.BuildParseAnalysisResult 实现）。
//
// 由上层实现并通过 SetRequestParser 注入；未注入（nil）时跳过解析，resolved_inputs
// 保留原始值。
//
// consumer-side 接口：只在 runtime 包定义，实现放在 handler 包，避免 dag -> handler
// 的循环依赖。
type NodeRequestParser interface {
	ParseRequestParam(ctx context.Context, scriptID int64, requestParam map[string]any) (map[string]any, error)
}

type RuntimeEngine struct {
	repo interfaces.AnalysisRepository
	// requestParser 可选的 request_param 解析器，见 NodeRequestParser。
	requestParser NodeRequestParser
}

func NewRuntimeEngine(repo interfaces.AnalysisRepository) *RuntimeEngine {
	return &RuntimeEngine{repo: repo}
}

// SetRequestParser 注入可选的 request_param 解析器，用于把 propagateOutputs 重写后的
// 原始输入还原成结构化 resolved_inputs。为 nil 时表示不解析。
func (e *RuntimeEngine) SetRequestParser(parser NodeRequestParser) {
	if e == nil {
		return
	}
	e.requestParser = parser
}

func (e *RuntimeEngine) GetSnapshot(ctx context.Context, analysisID int64) (*RuntimeSnapshot, error) {
	nodes, err := e.repo.ListAnalysisNodesByAnalysisID(ctx, analysisID)
	if err != nil {
		return nil, err
	}

	statusCount := map[string]int{}
	completed := 0
	readyCount := 0
	runningCount := 0
	isFinished := true
	for _, node := range nodes {
		status := strings.TrimSpace(strings.ToLower(node.Status))
		if status == "" {
			status = StatusPending
		}
		statusCount[status]++
		if isTerminalNodeStatus(node) {
			completed++
		} else {
			isFinished = false
		}
		if status == StatusReady && !node.CacheHit {
			readyCount++
		}
		if status == StatusRunning || status == StatusSubmitted {
			runningCount++
		}
	}

	percent := 100
	if len(nodes) > 0 {
		percent = int(float64(completed) * 100.0 / float64(len(nodes)))
	}

	return &RuntimeSnapshot{
		AnalysisID:        analysisID,
		TotalNodes:        len(nodes),
		StatusCount:       statusCount,
		CompletedCount:    completed,
		CompletionPercent: percent,
		ReadyCount:        readyCount,
		RunningCount:      runningCount,
		IsFinished:        isFinished,
	}, nil
}

func (e *RuntimeEngine) RefreshReadyStatus(ctx context.Context, analysisID int64) error {
	nodes, err := e.repo.ListAnalysisNodesByAnalysisID(ctx, analysisID)
	if err != nil {
		return err
	}
	edges, err := e.repo.ListAnalysisEdgesByAnalysisID(ctx, analysisID)
	if err != nil {
		return err
	}

	nodeByID := make(map[string]*types.AnalysisNode, len(nodes))
	for _, node := range nodes {
		nodeByID[node.NodeID] = node
	}

	incoming := map[string][]*types.AnalysisEdge{}
	for _, edge := range edges {
		incoming[edge.TargetNode] = append(incoming[edge.TargetNode], edge)
	}

	for _, node := range nodes {
		status := strings.TrimSpace(strings.ToLower(node.Status))
		if status == "" {
			status = StatusPending
		}
		if status != StatusPending {
			continue
		}

		canRun := true
		for _, edge := range incoming[node.NodeID] {
			upstream := nodeByID[edge.SourceNode]
			if upstream == nil {
				continue
			}
			upstreamStatus := strings.TrimSpace(strings.ToLower(upstream.Status))
			if !isTerminalNodeStatus(upstream) {
				canRun = false
				break
			}
			if !isSuccessNodeStatus(upstream) {
				canRun = false
				_ = e.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{
					"status":        StatusSkipped,
					"finished_at":   time.Now().UTC(),
					"error_message": fmt.Sprintf("upstream node %s ended with status %s", upstream.NodeID, upstreamStatus),
				})
				break
			}
		}
		if canRun {
			if err := e.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{"status": StatusReady}); err != nil {
				return err
			}
		}
	}

	return nil
}

func (e *RuntimeEngine) ClaimNextReadyNode(ctx context.Context, analysisID int64) (*types.AnalysisNode, error) {
	node, err := e.repo.ClaimNextReadyNode(ctx, analysisID, StatusReady, StatusSubmitted)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return node, nil
}

func (e *RuntimeEngine) MarkNodeRunning(ctx context.Context, analysisNodeID int64) (*types.AnalysisNode, error) {
	node, err := e.repo.GetAnalysisNodeByID(ctx, analysisNodeID)
	if err != nil {
		return nil, err
	}
	if err := EnsureTransition(node.Status, StatusRunning); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if err := e.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{
		"status":     StatusRunning,
		"started_at": &now,
	}); err != nil {
		return nil, err
	}
	node.Status = StatusRunning
	node.StartedAt = &now
	return node, nil
}

func (e *RuntimeEngine) CompleteNode(
	ctx context.Context,
	analysisNodeID int64,
	status string,
	resolvedOutputs map[string]any,
	exitCode int,
	errorMessage string,
	outputValidationErrors []string,
	outputFileIDs map[string]string,
) (*types.AnalysisNode, error) {
	node, err := e.repo.GetAnalysisNodeByID(ctx, analysisNodeID)
	if err != nil {
		return nil, err
	}
	status = strings.TrimSpace(strings.ToLower(status))
	if status == "" {
		status = StatusDone
	}
	if err := EnsureTransition(node.Status, status); err != nil {
		return nil, err
	}

	updates := map[string]any{
		"status":        status,
		"exit_code":     exitCode,
		"finished_at":   time.Now().UTC(),
		"error_message": nil,
	}
	if status == StatusFailed {
		if strings.TrimSpace(errorMessage) == "" {
			errorMessage = "node execution failed"
		}
		updates["error_message"] = errorMessage
	} else if status == StatusStopped {
		if strings.TrimSpace(errorMessage) == "" {
			errorMessage = "node stopped by user"
		}
		updates["error_message"] = errorMessage
	}

	if IsSuccessStatus(status) {
		updates["resolved_outputs"] = types.JSONMap(resolvedOutputs)
		updates["output_validation_errors"] = types.JSONSlice{}
		// 记录本节点输出句柄到 go_file 主键的映射，供下游节点物化/执行时把上游文件
		// 还原成 SelectAssay 的原始输入 `{"files": "<id>"}`。这里只落库，不再向下游
		// 推送（推模式已移除，改由下游拉取）。
		if len(outputFileIDs) > 0 {
			ids := make(types.JSONMap, len(outputFileIDs))
			for handle, fileID := range outputFileIDs {
				ids[handle] = fileID
			}
			updates["output_file_ids"] = ids
		}
	} else if len(outputValidationErrors) > 0 {
		errs := make(types.JSONSlice, 0, len(outputValidationErrors))
		for _, item := range outputValidationErrors {
			errs = append(errs, item)
		}
		updates["output_validation_errors"] = errs
	}

	if err := e.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, updates); err != nil {
		return nil, err
	}

	if err := e.RefreshReadyStatus(ctx, node.AnalysisID); err != nil {
		return nil, err
	}
	return e.repo.GetAnalysisNodeByID(ctx, analysisNodeID)
}

// NOTE: 推模式（propagateOutputs / encodeOriginalRequestInput / parseNodeRequestParam /
// overwriteResolvedInputs）已移除。上游输入不再在上游完成时向下游推送，而是由下游在
// 物化/探测时拉取，见 upstream_binding.go。

func asString(v any) string {
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

func asMap(v any) map[string]any {
	if v == nil {
		return map[string]any{}
	}
	if m, ok := v.(types.JSONMap); ok {
		return map[string]any(m)
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Map && rv.Type().Key().Kind() == reflect.String {
		out := make(map[string]any, rv.Len())
		for _, key := range rv.MapKeys() {
			out[key.String()] = rv.MapIndex(key).Interface()
		}
		return out
	}
	return map[string]any{}
}

func isTerminalNodeStatus(node *types.AnalysisNode) bool {
	if node == nil {
		return false
	}
	status := strings.TrimSpace(strings.ToLower(node.Status))
	if status == StatusReady && node.CacheHit {
		return true
	}
	return IsTerminalStatus(status)
}

func isSuccessNodeStatus(node *types.AnalysisNode) bool {
	if node == nil {
		return false
	}
	status := strings.TrimSpace(strings.ToLower(node.Status))
	if status == StatusReady && node.CacheHit {
		return true
	}
	return IsSuccessStatus(status)
}

func SortNodesByStartedAt(nodes []*types.AnalysisNode) {
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].CreatedAt.Before(nodes[j].CreatedAt)
	})
}
