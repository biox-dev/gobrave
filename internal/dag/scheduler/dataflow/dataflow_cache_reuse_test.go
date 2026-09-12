package dataflow

import (
	"context"
	"errors"
	"testing"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/dag/nodebuild"
	"github.com/biox-dev/gobrave/internal/types"
)

// These tests pin the per-node, fingerprint-aware reuse decision of the dataflow
// scheduler: an instance matched by identity is no longer reused unconditionally,
// cache types 3/4 compare the freshly generated run.sh / params.json digests, and a
// node that fails the comparison is reset to ready and dispatched again.

// stubFingerprinterV3 derives the digests from the node identity, so a test can make
// the freshly probed artifacts differ from (or match) the persisted ones purely by
// choosing the persisted digests.
type stubFingerprinterV3 struct{}

func (stubFingerprinterV3) Fingerprint(_ context.Context, node *types.AnalysisNode) error {
	if node == nil {
		return errors.New("stubFingerprinterV3: node is nil")
	}
	node.CommandMD5 = "cmd:" + node.NodeID
	node.ParamsMD5 = "params:" + node.NodeID
	return nil
}

// stubCacheRepoV3 extends the shared V3 repo stub with the reset write path.
type stubCacheRepoV3 struct {
	stubAnalysisRepoV3
	updates map[string]map[string]any
}

func (r *stubCacheRepoV3) UpdateAnalysisNodeByAnalysisNodeID(_ context.Context, analysisNodeID string, values map[string]any) error {
	if r.updates == nil {
		r.updates = map[string]map[string]any{}
	}
	r.updates[analysisNodeID] = values
	for _, node := range r.nodes {
		if node.AnalysisNodeID == analysisNodeID {
			applyNodeUpdatesV3(node, values)
		}
	}
	return nil
}

func applyNodeUpdatesV3(node *types.AnalysisNode, values map[string]any) {
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
}

// materializePersistedV3Node builds a finished node plus the analysis that owns it,
// exactly the way a previous run would have persisted them.
func materializePersistedV3Node(t *testing.T, cacheType int, commandMD5, paramsMD5 string) (*types.Analysis, *types.AnalysisNode, *stubCacheRepoV3) {
	t.Helper()
	requireSnowflakeV3(t)

	analysis := &types.Analysis{ID: 9, ProjectID: 2, OutputDir: t.TempDir(), CacheType: cacheType}
	params := types.JSONMap{"threads": 4}
	resolved := types.JSONMap{"bam": "a.bam"}

	node, err := nodebuild.Materialize(nodebuild.MaterializeRequest{
		Analysis: analysis,
		Spec:     &nodebuild.Spec{NodeID: "align", Params: params, ResolvedInputs: resolved},
		Status:   dagruntime.StatusReady,
	})
	if err != nil {
		t.Fatalf("materialize persisted node failed: %v", err)
	}
	node.Status = dagruntime.StatusDone
	node.CommandMD5 = commandMD5
	node.ParamsMD5 = paramsMD5

	repo := &stubCacheRepoV3{stubAnalysisRepoV3: stubAnalysisRepoV3{
		analysis: analysis,
		nodes:    []*types.AnalysisNode{node},
	}}
	return analysis, node, repo
}

func persistV3Submit(t *testing.T, runtime *persistentDataflowRuntime, analysisID int64) (*types.AnalysisNode, bool) {
	t.Helper()
	node, rerun, err := runtime.persistAnalysisNode(context.Background(), &DataflowAnalysisNodePersistParams{
		AnalysisID:     analysisID,
		NodeID:         "align",
		ScriptID:       "42",
		Status:         "ready",
		Params:         map[string]any{"threads": 4},
		ResolvedInputs: map[string]any{"bam": "a.bam"},
	})
	if err != nil {
		t.Fatalf("persistAnalysisNode failed: %v", err)
	}
	return node, rerun
}

func TestV3FingerprintPolicyRerunsWhenScriptChanged(t *testing.T) {
	analysis, existing, repo := materializePersistedV3Node(t, types.CacheTypeReuseWhenScriptUnchanged, "cmd:stale", "params:align")
	runtime := &persistentDataflowRuntime{
		repo:          repo,
		workflowRepo:  stubWorkflowRepoV3{script: &types.Script{ID: 42}},
		projectID:     analysis.ProjectID,
		cachePolicies: dagruntime.NewCachePolicyRegistry(),
		fingerprinter: stubFingerprinterV3{},
	}

	node, rerun := persistV3Submit(t, runtime, analysis.ID)
	if !rerun {
		t.Fatal("a changed run.sh must trigger a rerun instead of reusing the stale node")
	}
	if node.AnalysisNodeID != existing.AnalysisNodeID {
		t.Fatalf("rerun must reset the persisted node in place, got %q want %q", node.AnalysisNodeID, existing.AnalysisNodeID)
	}
	if node.Status != dagruntime.StatusReady {
		t.Fatalf("rerun node status = %q, want %q", node.Status, dagruntime.StatusReady)
	}
	if node.RerunReason != "command changed" {
		t.Fatalf("rerun reason = %q, want %q", node.RerunReason, "command changed")
	}
	if node.CacheHit {
		t.Fatal("a rerun node must not stay a cache hit")
	}
	if node.CommandMD5 != "cmd:align" {
		t.Fatalf("rerun must store the freshly probed digest, got %q", node.CommandMD5)
	}
	if repo.updates[existing.AnalysisNodeID] == nil {
		t.Fatal("the rerun decision must persist the reset")
	}
}

func TestV3FingerprintPolicyReusesWhenScriptUnchanged(t *testing.T) {
	analysis, _, repo := materializePersistedV3Node(t, types.CacheTypeReuseWhenScriptUnchanged, "cmd:align", "params:align")
	runtime := &persistentDataflowRuntime{
		repo:          repo,
		workflowRepo:  stubWorkflowRepoV3{script: &types.Script{ID: 42}},
		projectID:     analysis.ProjectID,
		cachePolicies: dagruntime.NewCachePolicyRegistry(),
		fingerprinter: stubFingerprinterV3{},
	}

	_, rerun := persistV3Submit(t, runtime, analysis.ID)
	if rerun {
		t.Fatal("an unchanged run.sh must be reused")
	}
	if len(repo.updates) != 0 {
		t.Fatalf("reuse must not write to the persisted node, got %v", repo.updates)
	}
}

func TestV3ReuseExistingPolicyIgnoresFingerprint(t *testing.T) {
	// reuse_existing never asks for a fingerprint, so even a stale digest is reused.
	analysis, _, repo := materializePersistedV3Node(t, types.CacheTypeReuseExistingNode, "cmd:stale", "params:stale")
	runtime := &persistentDataflowRuntime{
		repo:          repo,
		workflowRepo:  stubWorkflowRepoV3{script: &types.Script{ID: 42}},
		projectID:     analysis.ProjectID,
		cachePolicies: dagruntime.NewCachePolicyRegistry(),
	}

	_, rerun := persistV3Submit(t, runtime, analysis.ID)
	if rerun {
		t.Fatal("reuse_existing must reuse a successful node regardless of fingerprints")
	}
	if repo.updates != nil {
		t.Fatal("reuse_existing must not reset the node")
	}
}

func TestV3DoesNotResetInFlightNode(t *testing.T) {
	analysis, existing, repo := materializePersistedV3Node(t, types.CacheTypeReuseWhenScriptAndParamsUnchanged, "cmd:stale", "params:stale")
	existing.Status = dagruntime.StatusRunning
	runtime := &persistentDataflowRuntime{
		repo:          repo,
		workflowRepo:  stubWorkflowRepoV3{script: &types.Script{ID: 42}},
		projectID:     analysis.ProjectID,
		cachePolicies: dagruntime.NewCachePolicyRegistry(),
		fingerprinter: stubFingerprinterV3{},
	}

	_, rerun := persistV3Submit(t, runtime, analysis.ID)
	if rerun {
		t.Fatal("a node owned by an in-flight dispatch must not be reset")
	}
	if repo.updates != nil {
		t.Fatal("an in-flight node must not be written to")
	}
}

func TestV3ReusePolicyRerunsFailedNode(t *testing.T) {
	analysis, existing, repo := materializePersistedV3Node(t, types.CacheTypeReuseExistingNode, "", "")
	existing.Status = dagruntime.StatusFailed
	runtime := &persistentDataflowRuntime{
		repo:          repo,
		workflowRepo:  stubWorkflowRepoV3{script: &types.Script{ID: 42}},
		projectID:     analysis.ProjectID,
		cachePolicies: dagruntime.NewCachePolicyRegistry(),
	}

	_, rerun := persistV3Submit(t, runtime, analysis.ID)
	if !rerun {
		t.Fatal("a node whose previous execution did not succeed must be rerun")
	}
	if repo.updates[existing.AnalysisNodeID] == nil {
		t.Fatal("the failed node reset must be persisted")
	}
}
