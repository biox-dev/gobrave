package dynamic

import (
	"context"
	"fmt"
	"testing"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// These tests pin the execution-order guarantee of a cache-invalidated rerun.
//
// When cache_type is CacheTypeReuseWhenScriptAndParamsUnchanged and the params
// change, every node of a previously finished analysis is a rerun candidate. Readiness
// is derived from persisted state, so once the root is flipped back to ready it stops
// being satisfied - transitively - and its dependants are simply not runnable. The
// reconcile pass therefore never needs to re-arm a subgraph or write a hold-back: it
// walks the compiled plan upstream-first and re-evaluates readiness on the fly.

// stubAnalysisRepo is an in-memory AnalysisRepository holding the analysis nodes the
// reconciler touches. Every other method is inherited from the embedded nil interface,
// which is fine because these tests never reach them.
type stubAnalysisRepo struct {
	interfaces.AnalysisRepository
	nodes []*types.AnalysisNode
}

func (r *stubAnalysisRepo) ListAnalysisNodesByAnalysisID(_ context.Context, _ int64) ([]*types.AnalysisNode, error) {
	return r.nodes, nil
}

func (r *stubAnalysisRepo) UpdateAnalysisNodeByAnalysisNodeID(_ context.Context, analysisNodeID string, values map[string]any) error {
	for _, node := range r.nodes {
		if node.AnalysisNodeID != analysisNodeID {
			continue
		}
		applyNodeUpdates(node, values)
		return nil
	}
	return fmt.Errorf("stubAnalysisRepo: analysis node %s not found", analysisNodeID)
}

func (r *stubAnalysisRepo) CreateAnalysisNodes(_ context.Context, items []*types.AnalysisNode) error {
	r.nodes = append(r.nodes, items...)
	return nil
}

func applyNodeUpdates(node *types.AnalysisNode, values map[string]any) {
	if value, ok := values["status"]; ok {
		node.Status, _ = value.(string)
	}
	if value, ok := values["cache_hit"]; ok {
		node.CacheHit, _ = value.(bool)
	}
	if value, ok := values["command_md5"]; ok {
		node.CommandMD5, _ = value.(string)
	}
	if value, ok := values["params_md5"]; ok {
		node.ParamsMD5, _ = value.(string)
	}
	if value, ok := values["rerun_reason"]; ok {
		node.RerunReason, _ = value.(string)
	}
	if value, ok := values["params"]; ok {
		if params, ok := value.(types.JSONMap); ok {
			node.Params = params
		}
	}
}

// stubWorkflowRepo resolves every script to the same row; the reconciler only needs a
// primary key to build its probe.
type stubWorkflowRepo struct {
	interfaces.WorkflowRepository
}

func (stubWorkflowRepo) GetScriptByScriptID(_ context.Context, _ int64, _ string) (*types.Script, error) {
	return &types.Script{ID: 42}, nil
}

// stubFingerprinter derives the digest from the payload, so changing a param really
// changes the params digest - which is what drives the cache_type=4 rerun decision.
type stubFingerprinter struct{}

func (stubFingerprinter) Fingerprint(_ context.Context, node *types.AnalysisNode) error {
	if node == nil {
		return fmt.Errorf("stubFingerprinter: node is nil")
	}
	node.CommandMD5 = "cmd:" + node.NodeID
	node.ParamsMD5 = fmt.Sprintf("params:%s:%v", node.NodeID, map[string]any(node.Params))
	return nil
}

// newRerunOrderingFixture builds a finished a -> b -> c analysis where every persisted
// node carries stale params digests, so the next run must rerun all three.
//
// mutate is applied to the persisted nodes before the plan is built; it lets a test
// reproduce a partially dispatched previous run.
func newRerunOrderingFixture(t *testing.T, mutate func(nodes []*types.AnalysisNode)) (*dynamicDagOrchestratorV2, *stubAnalysisRepo, *dynamicExecutionPlan) {
	t.Helper()

	nodeTemplates := []map[string]any{
		{
			"node_id":      "a",
			"script_id":    "script-a",
			"params":       types.JSONMap{"p": "new"},
			"upstream_ids": []any{},
		},
		{
			"node_id":      "b",
			"script_id":    "script-b",
			"params":       types.JSONMap{"p": "new"},
			"upstream_ids": []any{"a"},
		},
		{
			"node_id":      "c",
			"script_id":    "script-c",
			"params":       types.JSONMap{"p": "new"},
			"upstream_ids": []any{"b"},
		},
	}

	edges := []*types.AnalysisEdge{
		{AnalysisEdgeID: "a-b", AnalysisID: 7, SourceNode: "a", TargetNode: "b", SourceHandle: "out", TargetHandle: "in"},
		{AnalysisEdgeID: "b-c", AnalysisID: 7, SourceNode: "b", TargetNode: "c", SourceHandle: "out", TargetHandle: "in"},
	}

	repo := &stubAnalysisRepo{nodes: []*types.AnalysisNode{
		{AnalysisNodeID: "node-a", NodeID: "a", AnalysisID: 7, Status: dagruntime.StatusDone, CommandMD5: "cmd:a", ParamsMD5: "stale", Params: types.JSONMap{"p": "old"}},
		{AnalysisNodeID: "node-b", NodeID: "b", AnalysisID: 7, Status: dagruntime.StatusDone, CommandMD5: "cmd:b", ParamsMD5: "stale", Params: types.JSONMap{"p": "old"}},
		{AnalysisNodeID: "node-c", NodeID: "c", AnalysisID: 7, Status: dagruntime.StatusDone, CommandMD5: "cmd:c", ParamsMD5: "stale", Params: types.JSONMap{"p": "old"}},
	}}
	if mutate != nil {
		mutate(repo.nodes)
	}

	orchestrator := &dynamicDagOrchestratorV2{
		repo:          repo,
		workflowRepo:  stubWorkflowRepo{},
		fingerprinter: stubFingerprinter{},
		cachePolicies: dagruntime.NewCachePolicyRegistry(),
	}

	return orchestrator, repo, newDynamicExecutionPlan(nodeTemplates, edges)
}

// completeNode marks a persisted node as successfully finished, as the dispatcher
// would after a real execution.
func completeNode(repo *stubAnalysisRepo, nodeID string) {
	for _, node := range repo.nodes {
		if node.NodeID == nodeID {
			node.Status = dagruntime.StatusDone
			return
		}
	}
}

func nodeStatuses(repo *stubAnalysisRepo) map[string]string {
	statuses := make(map[string]string, len(repo.nodes))
	for _, node := range repo.nodes {
		statuses[node.NodeID] = node.Status
	}
	return statuses
}

// TestRerunHoldsBackDownstreamUntilUpstreamIsQueued is the core regression: a changed
// param invalidates all three nodes, but only the root may become ready, because the
// other two must wait for the rerun of the node they consume.
func TestRerunHoldsBackDownstreamUntilUpstreamIsQueued(t *testing.T) {
	orchestrator, repo, plan := newRerunOrderingFixture(t, nil)
	analysis := &types.Analysis{ID: 7, ProjectID: 1, CacheType: types.CacheTypeReuseWhenScriptAndParamsUnchanged}

	if err := orchestrator.reconcilePlan(context.Background(), analysis, plan); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	statuses := nodeStatuses(repo)
	if statuses["a"] != dagruntime.StatusReady {
		t.Fatalf("root node a should be queued for rerun, got %q", statuses["a"])
	}
	if statuses["b"] != dagruntime.StatusDone {
		t.Fatalf("b must still wait for the rerun of a, got %q", statuses["b"])
	}
	if statuses["c"] != dagruntime.StatusDone {
		t.Fatalf("c must still wait for the rerun of b, got %q", statuses["c"])
	}

	state := newDynamicState(plan, repo.nodes)
	if state.canRun("b") {
		t.Fatal("derived readiness must not report b runnable while a reruns")
	}
	if state.canRun("c") {
		t.Fatal("derived readiness must not report c runnable while its ancestor a reruns")
	}
}

// TestRerunUnlocksOneLayerAtATime walks the graph forward and asserts that each
// completion releases exactly the next layer.
func TestRerunUnlocksOneLayerAtATime(t *testing.T) {
	orchestrator, repo, plan := newRerunOrderingFixture(t, nil)
	analysis := &types.Analysis{ID: 7, ProjectID: 1, CacheType: types.CacheTypeReuseWhenScriptAndParamsUnchanged}
	ctx := context.Background()

	if err := orchestrator.reconcilePlan(ctx, analysis, plan); err != nil {
		t.Fatalf("initial reconcile failed: %v", err)
	}

	completeNode(repo, "a")
	if err := orchestrator.reconcilePlan(ctx, analysis, plan); err != nil {
		t.Fatalf("reconcile after a completed failed: %v", err)
	}
	statuses := nodeStatuses(repo)
	if statuses["b"] != dagruntime.StatusReady {
		t.Fatalf("b should be queued once a completed, got %q", statuses["b"])
	}
	if statuses["c"] != dagruntime.StatusDone {
		t.Fatalf("c must still wait for b, got %q", statuses["c"])
	}

	completeNode(repo, "b")
	if err := orchestrator.reconcilePlan(ctx, analysis, plan); err != nil {
		t.Fatalf("reconcile after b completed failed: %v", err)
	}
	if statuses = nodeStatuses(repo); statuses["c"] != dagruntime.StatusReady {
		t.Fatalf("c should be queued once b completed, got %q", statuses["c"])
	}
}

// TestRerunHoldsBackAlreadyClaimableDownstream covers a previous run that left a
// dependant ready but never dispatched it: the derived-readiness invariant must demote
// it to pending so it cannot be claimed ahead of the upstream that is about to rerun.
func TestRerunHoldsBackAlreadyClaimableDownstream(t *testing.T) {
	orchestrator, repo, plan := newRerunOrderingFixture(t, func(nodes []*types.AnalysisNode) {
		nodes[1].Status = dagruntime.StatusReady
		nodes[1].CacheHit = false
	})
	analysis := &types.Analysis{ID: 7, ProjectID: 1, CacheType: types.CacheTypeReuseWhenScriptAndParamsUnchanged}

	if err := orchestrator.reconcilePlan(context.Background(), analysis, plan); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	statuses := nodeStatuses(repo)
	if statuses["a"] != dagruntime.StatusReady {
		t.Fatalf("root node a should be queued for rerun, got %q", statuses["a"])
	}
	if statuses["b"] != dagruntime.StatusPending {
		t.Fatalf("claimable downstream b must be held back to pending, got %q", statuses["b"])
	}
}

// TestDerivedReadinessIsTransitive pins the property that replaced re-arming: a node is
// satisfied only when it and every ancestor are reusable, so queueing an ancestor for a
// rerun makes even a finished grandchild not runnable.
func TestDerivedReadinessIsTransitive(t *testing.T) {
	_, repo, plan := newRerunOrderingFixture(t, nil)

	// Precondition: with every node finished, the whole chain is runnable.
	state := newDynamicState(plan, repo.nodes)
	if !state.canRun("c") {
		t.Fatal("fixture precondition: c should be runnable while all ancestors are done")
	}

	// Queue the root for a rerun exactly as the reconcile pass would.
	repo.nodes[0].Status = dagruntime.StatusReady
	repo.nodes[0].CacheHit = false

	state = newDynamicState(plan, repo.nodes)
	if state.satisfied("b") {
		t.Fatal("b must not stay satisfied once its ancestor a is queued for rerun")
	}
	if state.canRun("b") {
		t.Fatal("b must not be runnable once its ancestor a is queued for rerun")
	}
	if state.canRun("c") {
		t.Fatal("c must not be runnable once its ancestor a is queued for rerun")
	}
}
