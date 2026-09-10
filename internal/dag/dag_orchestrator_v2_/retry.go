package orchestratorv2

import (
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
)

// RetryPolicy is the Strategy implementation deciding whether a failed node is
// re-queued in place instead of failing the whole run.
type RetryPolicy interface {
	// ShouldRetry reports whether node should run again right now.
	ShouldRetry(node *types.AnalysisNode) bool
}

// NoRetryPolicy never retries: the first failure blocks the downstream subgraph.
// It is the default to preserve the historical scheduler semantics.
type NoRetryPolicy struct{}

// ShouldRetry implements RetryPolicy.
func (NoRetryPolicy) ShouldRetry(*types.AnalysisNode) bool { return false }

// MaxRetryPolicy retries a node until node.MaxRetry is reached.
type MaxRetryPolicy struct{}

// ShouldRetry implements RetryPolicy.
func (MaxRetryPolicy) ShouldRetry(node *types.AnalysisNode) bool {
	if node == nil {
		return false
	}
	if node.MaxRetry <= 0 {
		return false
	}
	return node.Retry < node.MaxRetry
}

// retryReason renders the human readable reason stored on the node.
func retryReason(attempt, limit int) string {
	return fmt.Sprintf("retry %d/%d", attempt, limit)
}

// isValidNodeIdentity guards DB writes against blank identities.
func isValidNodeIdentity(analysisNodeID string) bool {
	return strings.TrimSpace(analysisNodeID) != ""
}
