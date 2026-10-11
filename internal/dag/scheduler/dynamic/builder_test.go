package dynamic

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/dag/nodebuild"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
)

// These tests pin the V2 side of the shared node construction contract: a node
// V2 materializes must carry the same workspace layout and the same instance
// identity a V3 node would, so the two schedulers can reuse each other's work.

func requireSnowflake(t *testing.T) {
	t.Helper()
	if err := utils.InitSnowflake(1); err != nil {
		t.Fatalf("init snowflake: %v", err)
	}
}

func TestBuildDynamicAnalysisNodeUsesSharedLayoutAndIdentity(t *testing.T) {
	requireSnowflake(t)

	orchestrator := &dynamicDagOrchestratorV2{}
	analysis := &types.Analysis{ID: 9, ProjectID: 2, WorkspaceDir: t.TempDir()}
	row := map[string]any{
		"node_id":         "align",
		"node_name":       "align",
		"script_id":       "42",
		"resolved_inputs": types.JSONMap{},
		"upstream_ids":    []any{},
		"downstream_ids":  []any{},
	}

	node, err := orchestrator.buildDynamicAnalysisNode(context.Background(), &types.Script{ID: 42}, analysis, "align", row, nil, nil, dagruntime.StatusReady, "")
	if err != nil {
		t.Fatalf("buildDynamicAnalysisNode failed: %v", err)
	}

	wantWorkspace := filepath.Join(analysis.WorkspaceDir, strconv.FormatInt(node.ID, 10))
	if node.WorkspaceDir != wantWorkspace {
		t.Fatalf("workspace dir = %q, want %q", node.WorkspaceDir, wantWorkspace)
	}
	if node.OutputDir != filepath.Join(wantWorkspace, "output") {
		t.Fatalf("output dir = %q", node.OutputDir)
	}
	if node.ProjectID != analysis.ProjectID {
		t.Fatalf("project scoping not copied: %d", node.ProjectID)
	}

	// The persisted identity must equal the shared algorithm's, otherwise V3
	// could never recognize this row as the same instance.
	wantHash := nodebuild.InstanceInputHash("align", types.JSONMap{})
	if node.InputHash != wantHash {
		t.Fatalf("input hash = %q, want %q", node.InputHash, wantHash)
	}
}

func TestBuildCacheProbeIsACopyWithFreshParams(t *testing.T) {
	orchestrator := &dynamicDagOrchestratorV2{}
	existing := &types.AnalysisNode{
		ID:             1,
		AnalysisNodeID: "node-align",
		NodeID:         "align",
		WorkspaceDir:   "/tmp/1",
		Status:         dagruntime.StatusDone,
		ResolvedInputs: types.JSONMap{"threads": 1},
	}
	row := map[string]any{
		"node_id":         "align",
		"script_id":       "42",
		"resolved_inputs": types.JSONMap{"threads": 4},
	}

	probe := orchestrator.buildCacheProbe(context.Background(), &types.Script{ID: 42}, existing, "align", row, nil, nil)
	if probe == nil {
		t.Fatal("probe must not be nil")
	}
	if probe == existing {
		t.Fatal("probe must be a copy, not the persisted node")
	}
	if probe.ID != existing.ID || probe.AnalysisNodeID != existing.AnalysisNodeID || probe.WorkspaceDir != existing.WorkspaceDir {
		t.Fatalf("probe must keep the node identity/workspace: %+v", probe)
	}
	if probe.ResolvedInputs["threads"] != 4 {
		t.Fatalf("probe should carry the freshly compiled inputs, got %#v", probe.ResolvedInputs)
	}
	if existing.ResolvedInputs["threads"] != 1 {
		t.Fatal("probing must not mutate the persisted node")
	}
}
