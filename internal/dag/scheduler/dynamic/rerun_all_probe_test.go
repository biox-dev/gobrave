package dynamic

import (
	"context"
	"fmt"
	"testing"

	"github.com/biox-dev/gobrave/internal/types"
)

// TestProbeWhichCacheTypesRequeueFreshlyCompletedNodes is a temporary diagnostic.
//
// It asks, per cache type, what the reconciler does with a node that *this run* just
// finished: persisted digests are made to match exactly what the probe regenerates
// from the still-unchanged plan.
func TestProbeWhichCacheTypesRequeueFreshlyCompletedNodes(t *testing.T) {
	cases := []struct {
		name      string
		cacheType int
	}{
		{"rerun_all", types.CacheTypeRerunAll},
		{"reuse_node", types.CacheTypeReuseExistingNode},
		{"reuse_code", types.CacheTypeReuseWhenScriptUnchanged},
		{"reuse_both", types.CacheTypeReuseWhenScriptAndParamsUnchanged},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			orchestrator, repo, plan := newRerunOrderingFixture(t, func(nodes []*types.AnalysisNode) {
				for _, node := range nodes {
					node.Params = types.JSONMap{"p": "new"}
					node.CommandMD5 = "cmd:" + node.NodeID
					node.ParamsMD5 = fmt.Sprintf("params:%s:%v", node.NodeID, map[string]any(node.Params))
				}
			})
			analysis := &types.Analysis{ID: 7, ProjectID: 1, CacheType: testCase.cacheType}

			if err := orchestrator.reconcilePlan(context.Background(), analysis, plan); err != nil {
				t.Fatalf("reconcile failed: %v", err)
			}
			statuses := nodeStatuses(repo)
			t.Logf("cache_type=%d %-11s -> a=%s rerun_reason=%q",
				testCase.cacheType, testCase.name, statuses["a"], repo.nodes[0].RerunReason)
		})
	}
}
