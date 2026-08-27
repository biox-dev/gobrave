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

type RuntimeEngine struct {
	repo interfaces.AnalysisRepository
}

func NewRuntimeEngine(repo interfaces.AnalysisRepository) *RuntimeEngine {
	return &RuntimeEngine{repo: repo}
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
	}

	if err := e.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, updates); err != nil {
		return nil, err
	}

	if IsSuccessStatus(status) {
		if err := e.propagateOutputs(ctx, node.AnalysisID, node.NodeID, resolvedOutputs); err != nil {
			return nil, err
		}
	}
	if err := e.RefreshReadyStatus(ctx, node.AnalysisID); err != nil {
		return nil, err
	}
	return e.repo.GetAnalysisNodeByID(ctx, analysisNodeID)
}

func (e *RuntimeEngine) propagateOutputs(ctx context.Context, analysisID int64, sourceNodeID string, outputs map[string]any) error {
	if len(outputs) == 0 {
		return nil
	}
	sourceNodeID = strings.TrimSpace(sourceNodeID)
	if sourceNodeID == "" {
		return nil
	}

	edges, err := e.repo.ListAnalysisEdgesByAnalysisID(ctx, analysisID)
	if err != nil {
		return err
	}

	for _, edge := range edges {
		if strings.TrimSpace(edge.SourceNode) != sourceNodeID {
			continue
		}
		sourceHandle := strings.TrimSpace(edge.SourceHandle)
		targetHandle := strings.TrimSpace(edge.TargetHandle)
		targetNodeID := strings.TrimSpace(edge.TargetNode)
		if sourceHandle == "" || targetHandle == "" || targetNodeID == "" {
			continue
		}
		value, ok := outputs[sourceHandle]
		if !ok {
			continue
		}

		targetNode, err := e.repo.GetAnalysisNodeByNodeID(ctx, analysisID, targetNodeID)
		if err != nil {
			if err == gorm.ErrRecordNotFound {
				continue
			}
			return err
		}

		params := cloneAnyMap(targetNode.Params)
		resolvedInputs := cloneAnyMap(targetNode.ResolvedInputs)
		inputPatterns := map[string]any(targetNode.InputsPatterns)
		inputCfg := asMap(inputPatterns[targetHandle])
		multiple := asBool(inputCfg["multiple"])

		if multiple || isListValue(params[targetHandle]) || isListValue(resolvedInputs[targetHandle]) {
			params[targetHandle] = appendToList(params[targetHandle], value)
			resolvedInputs[targetHandle] = appendToList(resolvedInputs[targetHandle], value)
		} else {
			params[targetHandle] = value
			resolvedInputs[targetHandle] = value
		}

		if err := e.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, targetNode.AnalysisNodeID, map[string]any{
			"params":          types.JSONMap(params),
			"resolved_inputs": types.JSONMap(resolvedInputs),
		}); err != nil {
			return err
		}
	}

	return nil
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

func asBool(v any) bool {
	switch val := v.(type) {
	case bool:
		return val
	case string:
		normalized := strings.TrimSpace(strings.ToLower(val))
		return normalized == "true" || normalized == "1" || normalized == "yes" || normalized == "y"
	case int:
		return val != 0
	case int8:
		return val != 0
	case int16:
		return val != 0
	case int32:
		return val != 0
	case int64:
		return val != 0
	case uint:
		return val != 0
	case uint8:
		return val != 0
	case uint16:
		return val != 0
	case uint32:
		return val != 0
	case uint64:
		return val != 0
	case float32:
		return val != 0
	case float64:
		return val != 0
	default:
		return false
	}
}

func isListValue(v any) bool {
	if v == nil {
		return false
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array
}

func appendToList(current any, value any) []any {
	if current == nil {
		return []any{value}
	}
	if arr, ok := current.([]any); ok {
		return append(arr, value)
	}
	if arr, ok := current.([]interface{}); ok {
		return append(arr, value)
	}
	rv := reflect.ValueOf(current)
	if rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
		out := make([]any, 0, rv.Len()+1)
		for i := 0; i < rv.Len(); i++ {
			out = append(out, rv.Index(i).Interface())
		}
		out = append(out, value)
		return out
	}
	return []any{current, value}
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(src))
	for k, v := range src {
		out[k] = v
	}
	return out
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
