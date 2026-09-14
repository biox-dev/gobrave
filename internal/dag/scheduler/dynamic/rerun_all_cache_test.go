package dynamic

import (
	"context"
	"testing"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
)

// These tests pin what cache_type=rerun_all means once the run is underway.
//
// rerun_all is applied by resetting the persisted graph before the run, so the per-node
// policy must never turn a node this run already finished back into a rerun candidate.
// The reconciler re-decides every persisted node on every pass - the initial pass plus
// one per node event and watchdog tick - so an unconditional rerun would flip the root
// back to ready right after each success: IsFinished would stay false forever, nothing
// downstream would ever become runnable, and the root would be re-executed endlessly.

// TestRerunAllKeepsCompletedNodes is the livelock regression: a fully finished chain must
// stay finished across reconcile passes instead of re-queueing its root.
func TestRerunAllKeepsCompletedNodes(t *testing.T) {
	orchestrator, repo, plan := newRerunOrderingFixture(t, nil)
	analysis := &types.Analysis{ID: 7, ProjectID: 1, CacheType: types.CacheTypeRerunAll}
	ctx := context.Background()

	for cycle := 1; cycle <= 3; cycle++ {
		if err := orchestrator.reconcilePlan(ctx, analysis, plan); err != nil {
			t.Fatalf("cycle %d reconcile failed: %v", cycle, err)
		}
		statuses := nodeStatuses(repo)
		for _, nodeID := range []string{"a", "b", "c"} {
			if statuses[nodeID] != dagruntime.StatusDone {
				t.Fatalf("cycle %d: node %s must stay done, got %q (rerun_reason=%q)",
					cycle, nodeID, statuses[nodeID], rerunReasonOf(repo, nodeID))
			}
		}
		// The chain must also stay satisfied, otherwise the run can never converge.
		if !newDynamicState(plan, repo.nodes).canRun("c") {
			t.Fatalf("cycle %d: the finished chain must stay runnable", cycle)
		}
	}
}

// TestRerunAllStillPromotesNodesWithoutResult guards the other half of the contract:
// rerun_all only stops the cache policy from re-queueing reusable nodes. A node with no
// result to reuse - one the reconciler demoted to pending, or an in-flight node rolled
// back by a resume - must still be made runnable again, or the graph would deadlock.
func TestRerunAllStillPromotesNodesWithoutResult(t *testing.T) {
	orchestrator, repo, plan := newRerunOrderingFixture(t, func(nodes []*types.AnalysisNode) {
		nodes[1].Status = dagruntime.StatusPending
	})
	analysis := &types.Analysis{ID: 7, ProjectID: 1, CacheType: types.CacheTypeRerunAll}

	if err := orchestrator.reconcilePlan(context.Background(), analysis, plan); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	statuses := nodeStatuses(repo)
	if statuses["a"] != dagruntime.StatusDone {
		t.Fatalf("a reusable node must stay done, got %q", statuses["a"])
	}
	if statuses["b"] != dagruntime.StatusReady {
		t.Fatalf("a node without a result must be queued again, got %q", statuses["b"])
	}
	if statuses["c"] != dagruntime.StatusDone {
		t.Fatalf("c must keep waiting for the rerun of b, got %q", statuses["c"])
	}
}

// rerunReasonOf reports the persisted rerun reason of one stub node, for diagnostics.
func rerunReasonOf(repo *stubAnalysisRepo, nodeID string) string {
	for _, node := range repo.nodes {
		if node.NodeID == nodeID {
			return node.RerunReason
		}
	}
	return ""
}
