package dag

import (
	"testing"

	"github.com/biox-dev/gobrave/internal/types"
)

func TestIsTerminalNodeStatus_WithCacheHitReady(t *testing.T) {
	node := &types.AnalysisNode{Status: StatusReady, CacheHit: true}
	if !isTerminalNodeStatus(node) {
		t.Fatalf("expected cache-hit ready node to be terminal")
	}
}

func TestIsSuccessNodeStatus_WithCacheHitReady(t *testing.T) {
	node := &types.AnalysisNode{Status: StatusReady, CacheHit: true}
	if !isSuccessNodeStatus(node) {
		t.Fatalf("expected cache-hit ready node to be success")
	}
}

func TestIsTerminalNodeStatus_ReadyWithoutCacheHit(t *testing.T) {
	node := &types.AnalysisNode{Status: StatusReady, CacheHit: false}
	if isTerminalNodeStatus(node) {
		t.Fatalf("expected ready node without cache hit to be non-terminal")
	}
}

func TestIsSuccessNodeStatus_ReadyWithoutCacheHit(t *testing.T) {
	node := &types.AnalysisNode{Status: StatusReady, CacheHit: false}
	if isSuccessNodeStatus(node) {
		t.Fatalf("expected ready node without cache hit to be non-success")
	}
}
