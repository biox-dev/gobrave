package orchestratorv3

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/dag/nodebuild"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

// These tests pin the V3 side of the shared node construction contract and, more
// importantly, the cross-scheduler cache: a node V2 materialized is recognized by
// the V3 persistence path as the same instance and reused instead of duplicated.

type stubAnalysisRepoV3 struct {
	interfaces.AnalysisRepository
	analysis *types.Analysis
	nodes    []*types.AnalysisNode
	created  int
}

func (r *stubAnalysisRepoV3) ListAnalysisNodesByAnalysisID(_ context.Context, _ int64) ([]*types.AnalysisNode, error) {
	return r.nodes, nil
}

func (r *stubAnalysisRepoV3) CreateAnalysisNodes(_ context.Context, items []*types.AnalysisNode) error {
	r.nodes = append(r.nodes, items...)
	r.created += len(items)
	return nil
}

func (r *stubAnalysisRepoV3) GetAnalysisByID(_ context.Context, _ int64) (*types.Analysis, error) {
	return r.analysis, nil
}

type stubWorkflowRepoV3 struct {
	interfaces.WorkflowRepository
	script *types.Script
}

func (r stubWorkflowRepoV3) GetScriptByScriptID(_ context.Context, _ int64, _ string) (*types.Script, error) {
	return r.script, nil
}

func requireSnowflakeV3(t *testing.T) {
	t.Helper()
	if err := utils.InitSnowflake(1); err != nil {
		t.Fatalf("init snowflake: %v", err)
	}
}

// TestV3PersistBuildsNodeWithSharedLayout asserts V3 nodes are materialized
// through the shared builder: canonical workspace layout plus a persisted
// instance identity equal to the shared algorithm's.
func TestV3PersistBuildsNodeWithSharedLayout(t *testing.T) {
	requireSnowflakeV3(t)

	analysis := &types.Analysis{ID: 9, ProjectID: 2, OutputDir: t.TempDir()}
	repo := &stubAnalysisRepoV3{analysis: analysis}
	runtime := &persistentDataflowRuntime{
		repo:         repo,
		workflowRepo: stubWorkflowRepoV3{script: &types.Script{ID: 42}},
		projectID:    analysis.ProjectID,
	}

	node, created, err := runtime.persistAnalysisNode(context.Background(), &DataflowAnalysisNodePersistParams{
		AnalysisID: analysis.ID,
		NodeID:     "align",
		ScriptID:   "42",
		Status:     "ready",
		Params:     map[string]any{"threads": 4},
	})
	if err != nil {
		t.Fatalf("persistAnalysisNode failed: %v", err)
	}
	if !created {
		t.Fatal("a missing node must be created")
	}
	if repo.created != 1 {
		t.Fatalf("expected exactly one created row, got %d", repo.created)
	}

	wantWorkspace := filepath.Join(analysis.OutputDir, strconv.FormatInt(node.ID, 10))
	if node.WorkspaceDir != wantWorkspace {
		t.Fatalf("workspace dir = %q, want %q", node.WorkspaceDir, wantWorkspace)
	}
	if node.OutputDir != filepath.Join(wantWorkspace, "output") {
		t.Fatalf("output dir = %q", node.OutputDir)
	}
	if node.ParamsPath != filepath.Join(wantWorkspace, "params.json") {
		t.Fatalf("params path = %q", node.ParamsPath)
	}
	if node.ProjectID != analysis.ProjectID {
		t.Fatalf("project scoping not copied: %d", node.ProjectID)
	}

	wantHash := nodebuild.InstanceInputHash("align", node.ResolvedInputs, node.Params)
	if node.InputHash != wantHash {
		t.Fatalf("input hash = %q, want %q", node.InputHash, wantHash)
	}
}

// TestV3PersistReusesV2MaterializedNode is the cross-scheduler cache regression:
// a finished node produced by the V2 builder is returned as-is (created=false)
// instead of being duplicated, because both schedulers derive the same identity.
func TestV3PersistReusesV2MaterializedNode(t *testing.T) {
	requireSnowflakeV3(t)

	analysis := &types.Analysis{ID: 9, ProjectID: 2, OutputDir: t.TempDir()}
	params := types.JSONMap{"threads": 4}
	resolved := types.JSONMap{"bam": "a.bam"}

	// Simulate a previous run that persisted this node through the V2 path.
	v2Node, err := nodebuild.Materialize(nodebuild.MaterializeRequest{
		Analysis: analysis,
		Spec:     &nodebuild.Spec{NodeID: "align", Params: params, ResolvedInputs: resolved},
		Status:   dagruntime.StatusReady,
	})
	if err != nil {
		t.Fatalf("V2-style materialize failed: %v", err)
	}
	v2Node.Status = dagruntime.StatusDone

	repo := &stubAnalysisRepoV3{analysis: analysis, nodes: []*types.AnalysisNode{v2Node}}
	runtime := &persistentDataflowRuntime{
		repo:         repo,
		workflowRepo: stubWorkflowRepoV3{script: &types.Script{ID: 42}},
		projectID:    analysis.ProjectID,
	}

	node, created, err := runtime.persistAnalysisNode(context.Background(), &DataflowAnalysisNodePersistParams{
		AnalysisID:     analysis.ID,
		NodeID:         "align",
		ScriptID:       "42",
		Status:         "ready",
		Params:         map[string]any(params),
		ResolvedInputs: map[string]any(resolved),
	})
	if err != nil {
		t.Fatalf("persistAnalysisNode failed: %v", err)
	}
	if created {
		t.Fatal("V3 must reuse the V2 node instead of creating a duplicate")
	}
	if repo.created != 0 {
		t.Fatalf("no row should have been created, got %d", repo.created)
	}
	if node.ID != v2Node.ID {
		t.Fatalf("expected the V2 node, got id=%d want=%d", node.ID, v2Node.ID)
	}
}
