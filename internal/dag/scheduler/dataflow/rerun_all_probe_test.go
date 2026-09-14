package dataflow

import (
	"testing"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
)

// TestProbeV3RerunAllResetsCompletedRow is a temporary diagnostic probe: it proves
// that rerun_all resets a completed row that carries the same instance identity -
// a row the current run itself could have created and finished.
func TestProbeV3RerunAllResetsCompletedRow(t *testing.T) {
	analysis, existing, repo := materializePersistedV3Node(t, types.CacheTypeRerunAll, "cmd:align", "params:align")
	existing.Status = dagruntime.StatusDone
	existing.CacheHit = true
	runtime := &persistentDataflowRuntime{
		repo:          repo,
		workflowRepo:  stubWorkflowRepoV3{script: &types.Script{ID: 42}},
		projectID:     analysis.ProjectID,
		cachePolicies: dagruntime.NewCachePolicyRegistry(),
	}

	node, rerun := persistV3Submit(t, runtime, analysis.ID)
	t.Logf("rerun=%v status=%s cache_hit=%v reason=%q updates=%v",
		rerun, node.Status, node.CacheHit, node.RerunReason, repo.updates != nil)
}
