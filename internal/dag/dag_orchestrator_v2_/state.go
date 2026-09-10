package orchestratorv2

import (
	"strings"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
)

// normaliseStatus lowercases and trims a persisted node status.
func normaliseStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

// isSuccessNode reports whether the node holds a reusable successful result.
//
// A node that is still "ready" but flagged as a cache hit represents a reused
// result from a previous run, so it counts as successful for input propagation.
func isSuccessNode(node *types.AnalysisNode) bool {
	if node == nil {
		return false
	}
	status := normaliseStatus(node.Status)
	if status == dagruntime.StatusReady && node.CacheHit {
		return true
	}
	return dagruntime.IsSuccessStatus(status)
}

// isTerminalNode reports whether the node can no longer change on its own.
func isTerminalNode(node *types.AnalysisNode) bool {
	if node == nil {
		return false
	}
	if isSuccessNode(node) {
		return true
	}
	return dagruntime.IsTerminalStatus(normaliseStatus(node.Status))
}
