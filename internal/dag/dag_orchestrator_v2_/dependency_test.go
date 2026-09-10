package orchestratorv2

import (
	"testing"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
)

func newTestGraph(t *testing.T) *Graph {
	t.Helper()
	graph, err := NewGraph(1, compiledFixture())
	if err != nil {
		t.Fatalf("NewGraph returned error: %v", err)
	}
	return graph
}

func TestDependencyTrackerReadiness(t *testing.T) {
	tracker := NewDependencyTracker(newTestGraph(t))

	if !tracker.IsReady("a1") {
		t.Fatal("root node should be ready")
	}
	if tracker.IsReady("b1") {
		t.Fatal("dependent node must wait for its upstream")
	}
	if tracker.IsBlocked("b1") {
		t.Fatal("dependent node must not be blocked while upstream is pending")
	}

	candidates := tracker.OnSuccess("a1")
	if len(candidates) != 1 || candidates[0] != "b1" {
		t.Fatalf("expected b1 to be released, got %v", candidates)
	}
	if !tracker.IsReady("b1") {
		t.Fatal("b1 should be ready after its upstream succeeded")
	}
}

func TestDependencyTrackerBlocksAndRecovers(t *testing.T) {
	tracker := NewDependencyTracker(newTestGraph(t))

	candidates := tracker.OnFailure("a1")
	if len(candidates) != 1 || candidates[0] != "b1" {
		t.Fatalf("expected b1 to be reported as affected, got %v", candidates)
	}
	if !tracker.IsBlocked("b1") {
		t.Fatal("b1 should be blocked after its upstream failed")
	}
	if tracker.IsReady("b1") {
		t.Fatal("blocked node must not be ready")
	}

	// A retried upstream that finally succeeds must clear the block again.
	tracker.OnSuccess("a1")
	if tracker.IsBlocked("b1") {
		t.Fatal("b1 should be unblocked after the upstream succeeded")
	}
	if !tracker.IsReady("b1") {
		t.Fatal("b1 should be ready after the upstream succeeded")
	}
}

func TestDependencyTrackerSeedFromExistingNodes(t *testing.T) {
	graph := newTestGraph(t)
	tracker := NewDependencyTracker(graph)

	tracker.Seed(map[string]*types.AnalysisNode{
		"a1": {NodeID: "a1", Status: dagruntime.StatusDone, ResolvedOutputs: types.JSONMap{"out": "value"}},
	})

	if !tracker.IsReady("b1") {
		t.Fatal("b1 should be ready when its upstream is already done")
	}

	// Cache hits are treated as successful results.
	tracker2 := NewDependencyTracker(graph)
	tracker2.Seed(map[string]*types.AnalysisNode{
		"a1": {NodeID: "a1", Status: dagruntime.StatusReady, CacheHit: true},
	})
	if !tracker2.IsReady("b1") {
		t.Fatal("b1 should be ready when its upstream is a cache hit")
	}

	// Nodes left running by a previous run are not considered successful.
	tracker3 := NewDependencyTracker(graph)
	tracker3.Seed(map[string]*types.AnalysisNode{
		"a1": {NodeID: "a1", Status: dagruntime.StatusRunning},
	})
	if tracker3.IsReady("b1") || tracker3.IsBlocked("b1") {
		t.Fatal("b1 must stay pending while its upstream is still running")
	}
}

func TestDependencyTrackerRunnable(t *testing.T) {
	tracker := NewDependencyTracker(newTestGraph(t))
	runnable := tracker.Runnable()
	if len(runnable) != 1 || runnable[0] != "a1" {
		t.Fatalf("only the root node should be runnable, got %v", runnable)
	}

	tracker.OnFailure("a1")
	runnable = tracker.Runnable()
	if len(runnable) != 2 {
		t.Fatalf("expected both nodes to be actionable after a failure, got %v", runnable)
	}
}
