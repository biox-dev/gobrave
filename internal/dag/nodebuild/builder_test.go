package nodebuild

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
)

func init() {
	// Materialize allocates a snowflake primary key, so the generator must be
	// initialized before any test runs.
	_ = utils.InitSnowflake(1)
}

func testAnalysis(t *testing.T) *types.Analysis {
	t.Helper()
	return &types.Analysis{ID: 100, ProjectID: 7, OutputDir: t.TempDir()}
}

func TestMaterializePersistsCanonicalLayout(t *testing.T) {
	analysis := testAnalysis(t)
	spec := &Spec{
		NodeID:         "align",
		AnalysisNodeID: "node-align",
		ScriptID:       42,
		Executor:       "docker",
		MaxRetry:       3,
		Params:         types.JSONMap{"threads": 4},
	}

	node, err := Materialize(MaterializeRequest{
		Analysis: analysis,
		Spec:     spec,
		Status:   dagruntime.StatusReady,
	})
	if err != nil {
		t.Fatalf("Materialize failed: %v", err)
	}

	if node.ID == 0 {
		t.Fatal("expected a generated primary key")
	}
	wantDir := filepath.Join(analysis.OutputDir, itoa(node.ID))
	if node.WorkspaceDir != wantDir {
		t.Fatalf("workspace dir = %q, want %q", node.WorkspaceDir, wantDir)
	}
	if node.OutputDir != filepath.Join(wantDir, "output") {
		t.Fatalf("output dir = %q", node.OutputDir)
	}
	if node.ParamsPath != filepath.Join(wantDir, "params.json") || node.CommandPath != filepath.Join(wantDir, "run.sh") {
		t.Fatalf("artifact paths not aligned to the layout: %+v", node)
	}
	if _, err := os.Stat(node.OutputDir); err != nil {
		t.Fatalf("output dir should have been created: %v", err)
	}
	if node.AnalysisID != analysis.ID || node.ProjectID != analysis.ProjectID {
		t.Fatalf("analysis scoping not copied: %+v", node)
	}
	if node.CreationSource != CreationSourceScheduler {
		t.Fatalf("creation source = %q", node.CreationSource)
	}
	if node.InputHash == "" {
		t.Fatal("instance identity must be persisted")
	}
}

func TestMaterializeLeavesPathsEmptyWithoutAnalysisOutputDir(t *testing.T) {
	analysis := &types.Analysis{ID: 1, ProjectID: 1}
	node, err := Materialize(MaterializeRequest{Analysis: analysis, Spec: &Spec{NodeID: "x"}, Status: dagruntime.StatusReady})
	if err != nil {
		t.Fatalf("Materialize failed: %v", err)
	}
	if node.WorkspaceDir != "" {
		t.Fatalf("workspace dir should stay empty, got %q", node.WorkspaceDir)
	}
}

func TestMaterializeMergesUpstreamOutputs(t *testing.T) {
	analysis := testAnalysis(t)
	upstream := &types.AnalysisNode{
		NodeID:          "first",
		Status:          dagruntime.StatusDone,
		ResolvedOutputs: types.JSONMap{"bam": "a.bam"},
	}
	edges := []*types.AnalysisEdge{
		{SourceNode: "first", TargetNode: "second", SourceHandle: "bam", TargetHandle: "input_bam"},
	}
	spec := &Spec{
		NodeID:         "second",
		InputsPatterns: types.JSONMap{"input_bam": map[string]any{"multiple": false}},
		Params:         types.JSONMap{},
	}

	node, err := Materialize(MaterializeRequest{
		Analysis: analysis,
		Spec:     spec,
		Status:   dagruntime.StatusReady,
		Incoming: edges,
		Upstream: map[string]*types.AnalysisNode{"first": upstream},
	})
	if err != nil {
		t.Fatalf("Materialize failed: %v", err)
	}
	if node.Params["input_bam"] != "a.bam" {
		t.Fatalf("upstream output not merged into params: %+v", node.Params)
	}
	if node.ResolvedInputs["input_bam"] != "a.bam" {
		t.Fatalf("upstream output not merged into resolved inputs: %+v", node.ResolvedInputs)
	}
}

func TestMaterializeAccumulatesFanInInputs(t *testing.T) {
	analysis := testAnalysis(t)
	edges := []*types.AnalysisEdge{
		{SourceNode: "s1", TargetNode: "merge", SourceHandle: "out", TargetHandle: "reads"},
		{SourceNode: "s2", TargetNode: "merge", SourceHandle: "out", TargetHandle: "reads"},
	}
	upstream := map[string]*types.AnalysisNode{
		"s1": {NodeID: "s1", Status: dagruntime.StatusDone, ResolvedOutputs: types.JSONMap{"out": "r1"}},
		"s2": {NodeID: "s2", Status: dagruntime.StatusDone, ResolvedOutputs: types.JSONMap{"out": "r2"}},
	}
	spec := &Spec{
		NodeID:         "merge",
		InputsPatterns: types.JSONMap{"reads": map[string]any{"multiple": true}},
	}

	node, err := Materialize(MaterializeRequest{
		Analysis: analysis,
		Spec:     spec,
		Status:   dagruntime.StatusReady,
		Incoming: edges,
		Upstream: upstream,
	})
	if err != nil {
		t.Fatalf("Materialize failed: %v", err)
	}
	values, ok := node.Params["reads"].([]any)
	if !ok || len(values) != 2 {
		t.Fatalf("fan-in should accumulate both values, got %#v", node.Params["reads"])
	}
}

func TestMaterializeSkipsGetFinishedAt(t *testing.T) {
	node, err := Materialize(MaterializeRequest{
		Analysis:     testAnalysis(t),
		Spec:         &Spec{NodeID: "blocked"},
		Status:       dagruntime.StatusSkipped,
		ErrorMessage: "blocked by failed upstream dependency",
	})
	if err != nil {
		t.Fatalf("Materialize failed: %v", err)
	}
	if node.FinishedAt == nil {
		t.Fatal("skipped node should be finished at materialization")
	}
}

func TestProbeKeepsIdentityAndRefreshsTemplate(t *testing.T) {
	existing := &types.AnalysisNode{
		ID:             5,
		AnalysisNodeID: "node-a",
		NodeID:         "a",
		WorkspaceDir:   "/tmp/5",
		Params:         types.JSONMap{"p": "old"},
		Status:         dagruntime.StatusDone,
	}
	probe, err := Probe(ProbeRequest{
		Analysis: testAnalysis(t),
		Existing: existing,
		Spec:     &Spec{NodeID: "a", ScriptID: 9, Params: types.JSONMap{"p": "new"}},
	})
	if err != nil {
		t.Fatalf("Probe failed: %v", err)
	}
	if probe.ID != existing.ID || probe.AnalysisNodeID != existing.AnalysisNodeID || probe.WorkspaceDir != existing.WorkspaceDir {
		t.Fatalf("probe must keep the node identity/workspace: %+v", probe)
	}
	if probe.Params["p"] != "new" {
		t.Fatalf("probe should carry the freshly compiled params, got %#v", probe.Params)
	}
	// The probe must be a copy: mutating it must not touch the persisted row.
	if existing.Params["p"] != "old" {
		t.Fatal("probe mutated the existing node")
	}
}

func TestInstanceInputHashIsStableAndSensitive(t *testing.T) {
	inputs := types.JSONMap{"bam": "a.bam"}
	params := types.JSONMap{"threads": 4}

	first := InstanceInputHash("align", inputs, params)
	second := InstanceInputHash("align", inputs, params)
	if first != second {
		t.Fatalf("hash is not stable: %q vs %q", first, second)
	}
	if first == InstanceInputHash("align", inputs, types.JSONMap{"threads": 8}) {
		t.Fatal("hash must change when params change")
	}
	if first == InstanceInputHash("other", inputs, params) {
		t.Fatal("hash must change when the node id changes")
	}
	// Empty maps must hash deterministically (contract with historical rows).
	if InstanceInputHash("a", nil, nil) != InstanceInputHash("a", types.JSONMap{}, types.JSONMap{}) {
		t.Fatal("nil and empty maps must hash identically")
	}
}

// TestCrossSchedulerCacheMatch pins the property the whole package exists for:
// a node materialized by one scheduler is recognized by the other scheduler as
// the same instance, so it can be reused as a cache hit instead of rerun.
func TestCrossSchedulerCacheMatch(t *testing.T) {
	analysis := testAnalysis(t)
	inputs := types.JSONMap{"bam": "a.bam"}
	params := types.JSONMap{"threads": 4}

	// Scheduler A (V2) materializes and finishes the node.
	materialized, err := Materialize(MaterializeRequest{
		Analysis: analysis,
		Spec:     &Spec{NodeID: "align", ResolvedInputs: inputs, Params: params},
		Status:   dagruntime.StatusReady,
	})
	if err != nil {
		t.Fatalf("Materialize failed: %v", err)
	}
	materialized.Status = dagruntime.StatusDone

	// Scheduler B (V3) computes the instance identity for the same work.
	wantHash := InstanceInputHash("align", inputs, params)
	if materialized.InputHash != wantHash {
		t.Fatalf("schedulers disagree on instance identity: %q vs %q", materialized.InputHash, wantHash)
	}

	got := MatchInstance([]*types.AnalysisNode{materialized}, "align", wantHash)
	if got == nil {
		t.Fatal("cross-scheduler node should have been matched as a cache hit")
	}
	if got.ID != materialized.ID {
		t.Fatalf("matched the wrong node: %+v", got)
	}
}

// TestMatchInstanceAcceptsLegacyRowsWithoutHash keeps rows persisted before
// input_hash existed reusable, so the refactor does not invalidate old caches.
func TestMatchInstanceAcceptsLegacyRowsWithoutHash(t *testing.T) {
	legacy := &types.AnalysisNode{NodeID: "align", Status: dagruntime.StatusDone}
	got := MatchInstance([]*types.AnalysisNode{legacy}, "align", "some-new-hash")
	if got != legacy {
		t.Fatalf("legacy successful row should be reusable, got %+v", got)
	}

	failed := &types.AnalysisNode{NodeID: "align", Status: dagruntime.StatusFailed}
	if MatchInstance([]*types.AnalysisNode{failed}, "align", "some-new-hash") != nil {
		t.Fatal("a failed legacy row must not be reused")
	}
}

// TestMatchInstancePrefersExactHash covers scattered processes: several
// instances share a node id and only the instance hash tells them apart.
func TestMatchInstancePrefersExactHash(t *testing.T) {
	first := &types.AnalysisNode{NodeID: "scatter", InputHash: "h1", Status: dagruntime.StatusDone}
	second := &types.AnalysisNode{NodeID: "scatter", InputHash: "h2", Status: dagruntime.StatusDone}

	got := MatchInstance([]*types.AnalysisNode{first, second}, "scatter", "h2")
	if got != second {
		t.Fatalf("expected the exact-hash instance, got %+v", got)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
