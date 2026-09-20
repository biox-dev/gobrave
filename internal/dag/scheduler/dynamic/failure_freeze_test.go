package dynamic

import (
	"context"
	"testing"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
)

// These tests pin the failure freeze: once a node this run dispatched comes back
// failed, the run launches nothing else, converges everything that has not started into
// skipped, and lets the completion gate report the failure.
//
// Before the freeze existed the cache policy won: it answers "rerun" for every terminal
// non-success node (that is how a retry of a previous run starts), and the reconciler
// re-decides every persisted node on every pass - so the failed node was flipped back to
// ready each time and re-dispatched forever, and the analysis never reached a terminal
// state.

// newFailureFreezeFixture builds the a -> b -> c plan with exactly the persisted rows a
// test asks for, so a case can cover a template that was never materialized as well as
// one that was materialized but never started.
func newFailureFreezeFixture(persisted ...*types.AnalysisNode) (*dynamicDagOrchestratorV2, *stubAnalysisRepo, *dynamicExecutionPlan) {
	nodeTemplates := []map[string]any{
		{"node_id": "a", "script_id": "script-a", "upstream_ids": []any{}},
		{"node_id": "b", "script_id": "script-b", "upstream_ids": []any{"a"}},
		{"node_id": "c", "script_id": "script-c", "upstream_ids": []any{"b"}},
	}
	edges := []*types.AnalysisEdge{
		{AnalysisEdgeID: "a-b", AnalysisID: 7, SourceNode: "a", TargetNode: "b", SourceHandle: "out", TargetHandle: "in"},
		{AnalysisEdgeID: "b-c", AnalysisID: 7, SourceNode: "b", TargetNode: "c", SourceHandle: "out", TargetHandle: "in"},
	}

	repo := &stubAnalysisRepo{nodes: persisted}
	orchestrator := &dynamicDagOrchestratorV2{
		repo:          repo,
		workflowRepo:  stubWorkflowRepo{},
		fingerprinter: stubFingerprinter{},
		cachePolicies: dagruntime.NewCachePolicyRegistry(),
	}
	return orchestrator, repo, newDynamicExecutionPlan(nodeTemplates, edges)
}

// failedThisRun is the row the dispatcher leaves behind after a failed execution.
func failedThisRun(nodeID string) *types.AnalysisNode {
	return &types.AnalysisNode{
		AnalysisNodeID: "node-" + nodeID,
		NodeID:         nodeID,
		AnalysisID:     7,
		Status:         dagruntime.StatusFailed,
		ErrorMessage:   "container exited with non-zero code (1)",
	}
}

func freezeAnalysis(t *testing.T) *types.Analysis {
	t.Helper()
	return &types.Analysis{ID: 7, ProjectID: 1, WorkspaceDir: t.TempDir(), CacheType: types.CacheTypeReuseExistingNode}
}

// TestFailureFreezeConvergesUnstartedNodesToSkipped covers the three shapes a
// not-yet-started node can have when the freeze hits: never materialized, materialized
// but claimable, and a ready cache hit that must survive as a reusable result.
func TestFailureFreezeConvergesUnstartedNodesToSkipped(t *testing.T) {
	requireSnowflake(t)

	cases := []struct {
		name      string
		persisted []*types.AnalysisNode
		want      map[string]string
	}{
		{
			name:      "never materialized templates become skipped",
			persisted: []*types.AnalysisNode{failedThisRun("a")},
			want: map[string]string{
				"a": dagruntime.StatusFailed,
				"b": dagruntime.StatusSkipped,
				"c": dagruntime.StatusSkipped,
			},
		},
		{
			name: "materialized but unstarted nodes become skipped",
			persisted: []*types.AnalysisNode{
				failedThisRun("a"),
				{AnalysisNodeID: "node-b", NodeID: "b", AnalysisID: 7, Status: dagruntime.StatusReady},
				{AnalysisNodeID: "node-c", NodeID: "c", AnalysisID: 7, Status: dagruntime.StatusPending},
			},
			want: map[string]string{
				"a": dagruntime.StatusFailed,
				"b": dagruntime.StatusSkipped,
				"c": dagruntime.StatusSkipped,
			},
		},
		{
			name: "a ready cache hit keeps its reusable result",
			persisted: []*types.AnalysisNode{
				failedThisRun("a"),
				{AnalysisNodeID: "node-b", NodeID: "b", AnalysisID: 7, Status: dagruntime.StatusReady, CacheHit: true},
			},
			want: map[string]string{
				"a": dagruntime.StatusFailed,
				"b": dagruntime.StatusReady,
				"c": dagruntime.StatusSkipped,
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			orchestrator, repo, plan := newFailureFreezeFixture(testCase.persisted...)
			plan.markAttempted("a")

			if err := orchestrator.reconcilePlan(context.Background(), freezeAnalysis(t), plan); err != nil {
				t.Fatalf("reconcile failed: %v", err)
			}
			if !plan.aborted {
				t.Fatal("the run should have latched the freeze")
			}

			statuses := nodeStatuses(repo)
			for nodeID, want := range testCase.want {
				if statuses[nodeID] != want {
					t.Fatalf("node %s status = %q, want %q (all=%v)", nodeID, statuses[nodeID], want, statuses)
				}
			}
			for _, node := range repo.nodes {
				if node.Status != dagruntime.StatusSkipped {
					continue
				}
				if node.ErrorMessage != "aborted after node failure" {
					t.Fatalf("skipped node %s error_message = %q, want the freeze reason", node.NodeID, node.ErrorMessage)
				}
			}
		})
	}
}

// TestFailureFreezeLetsCompletionGateReportFailure is the end of the freeze: every node
// is terminal, so the run ends right away - as a failure, not as a hang.
func TestFailureFreezeLetsCompletionGateReportFailure(t *testing.T) {
	requireSnowflake(t)

	orchestrator, repo, plan := newFailureFreezeFixture(failedThisRun("a"))
	plan.markAttempted("a")
	analysis := freezeAnalysis(t)
	ctx := context.Background()

	if err := orchestrator.reconcilePlan(ctx, analysis, plan); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	runtimeEngine := dagruntime.NewRuntimeEngine(repo)
	pool := dagruntime.NewWorkerPool(nil, 1, 4)
	finished, err := orchestrator.checkDynamicCompletion(ctx, analysis.ID, len(plan.order), runtimeEngine, pool)
	if !finished {
		t.Fatalf("a frozen run must be finished, pending nodes left=%v", nodeStatuses(repo))
	}
	if err == nil {
		t.Fatal("the completion gate must report the failure that froze the run")
	}
}

// TestFailureFreezeIsStickyAcrossPasses is the livelock regression: the pass after a
// failure must not resurrect the failed node, no matter how many times the reconciler
// runs while the in-flight nodes drain.
func TestFailureFreezeIsStickyAcrossPasses(t *testing.T) {
	requireSnowflake(t)

	orchestrator, repo, plan := newFailureFreezeFixture(failedThisRun("a"))
	plan.markAttempted("a")
	analysis := freezeAnalysis(t)
	ctx := context.Background()

	for pass := 1; pass <= 3; pass++ {
		if err := orchestrator.reconcilePlan(ctx, analysis, plan); err != nil {
			t.Fatalf("pass %d reconcile failed: %v", pass, err)
		}
		statuses := nodeStatuses(repo)
		if statuses["a"] != dagruntime.StatusFailed {
			t.Fatalf("pass %d re-queued the failed node: %v", pass, statuses)
		}
		if statuses["b"] != dagruntime.StatusSkipped || statuses["c"] != dagruntime.StatusSkipped {
			t.Fatalf("pass %d lost the skipped convergence: %v", pass, statuses)
		}
	}
}

// TestInheritedFailureIsStillRevivable keeps the other half of the contract intact: a
// failure this run did NOT produce belongs to a previous run, so the cache policy must
// still be allowed to rerun it - that is how a retry of a failed analysis starts.
func TestInheritedFailureIsStillRevivable(t *testing.T) {
	orchestrator, repo, plan := newFailureFreezeFixture(failedThisRun("a"))
	analysis := freezeAnalysis(t)

	if err := orchestrator.reconcilePlan(context.Background(), analysis, plan); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if plan.aborted {
		t.Fatal("an inherited failure must not freeze the run")
	}
	statuses := nodeStatuses(repo)
	if statuses["a"] != dagruntime.StatusReady {
		t.Fatalf("inherited failure should be queued for rerun, got %v", statuses)
	}
	if len(repo.nodes) != 1 {
		t.Fatalf("dependants must wait for the rerun, but %d nodes are persisted: %v", len(repo.nodes), statuses)
	}
}

// claimCountingRepo counts the claim query, which is the only way a frozen pump could
// still hand work to a worker.
type claimCountingRepo struct {
	stubAnalysisRepo
	claims int
}

func (r *claimCountingRepo) ClaimNextReadyNode(context.Context, int64, string, string) (*types.AnalysisNode, error) {
	r.claims++
	return nil, nil
}

// TestPumpReadyQueueDoesNotClaimAfterFreeze pins the dispatch half of the freeze.
func TestPumpReadyQueueDoesNotClaimAfterFreeze(t *testing.T) {
	ctx := context.Background()
	repo := &claimCountingRepo{}
	orchestrator := &dynamicDagOrchestratorV2{repo: repo}
	runtimeEngine := dagruntime.NewRuntimeEngine(repo)
	pool := dagruntime.NewWorkerPool(nil, 1, 4)

	if err := orchestrator.pumpReadyQueue(ctx, runtimeEngine, 7, &dynamicExecutionPlan{aborted: true}, pool); err != nil {
		t.Fatalf("frozen pump failed: %v", err)
	}
	if repo.claims != 0 {
		t.Fatalf("a frozen run must not claim, got %d claims", repo.claims)
	}

	// Control: the same call without the freeze does reach the claim query, so the
	// assertion above is about the freeze and not about a pump that never claims.
	if err := orchestrator.pumpReadyQueue(ctx, runtimeEngine, 7, &dynamicExecutionPlan{}, pool); err != nil {
		t.Fatalf("control pump failed: %v", err)
	}
	if repo.claims != 1 {
		t.Fatalf("control pump should claim once, got %d", repo.claims)
	}
}
