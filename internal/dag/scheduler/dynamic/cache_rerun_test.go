package dynamic

import (
	"context"
	"testing"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// graphResetRepoStub augments the reconcile stub with the analysis-level calls
// prepareAnalysisForCacheRerun makes, recording whether the graph was cleared.
type graphResetRepoStub struct {
	stubAnalysisRepo
	analysis     *types.Analysis
	clearedNodes bool
	clearedEdges bool
}

func (r *graphResetRepoStub) GetAnalysisByID(context.Context, int64) (*types.Analysis, error) {
	return r.analysis, nil
}

func (r *graphResetRepoStub) WithTransaction(ctx context.Context, fn func(interfaces.AnalysisRepository) error) error {
	return fn(r)
}

func (r *graphResetRepoStub) DeleteAnalysisNodesByAnalysisID(context.Context, int64) error {
	r.clearedNodes = true
	return nil
}

func (r *graphResetRepoStub) DeleteAnalysisEdgesByAnalysisID(context.Context, int64) error {
	r.clearedEdges = true
	return nil
}

// newGraphResetOrchestrator wires only what the graph reset touches: the repository and
// the shared policy registry the reset decision is delegated to.
func newGraphResetOrchestrator(repo interfaces.AnalysisRepository) *dynamicDagOrchestratorV2 {
	return &dynamicDagOrchestratorV2{
		repo:          repo,
		cachePolicies: dagruntime.NewCachePolicyRegistry(),
	}
}

// TestPrepareAnalysisForCacheRerunFollowsThePolicyRegistry pins the call site: the graph
// reset must be driven by the shared registry, so only the cache type that declares a
// reset clears the persisted graph and every reuse type keeps it.
func TestPrepareAnalysisForCacheRerunFollowsThePolicyRegistry(t *testing.T) {
	cases := []struct {
		name      string
		cacheType int
		wantReset bool
	}{
		{"rerun_all", types.CacheTypeRerunAll, true},
		{"reuse_node", types.CacheTypeReuseExistingNode, false},
		{"reuse_code", types.CacheTypeReuseWhenScriptUnchanged, false},
		{"reuse_both", types.CacheTypeReuseWhenScriptAndParamsUnchanged, false},
		{"unknown", 9999, false},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			repo := &graphResetRepoStub{analysis: &types.Analysis{ID: 7, CacheType: testCase.cacheType}}
			orchestrator := newGraphResetOrchestrator(repo)

			if err := orchestrator.prepareAnalysisForCacheRerun(context.Background(), 7); err != nil {
				t.Fatalf("prepare failed: %v", err)
			}
			if repo.clearedNodes != testCase.wantReset {
				t.Fatalf("cache type %d cleared nodes = %v, want %v (clearedEdges=%v)",
					testCase.cacheType, repo.clearedNodes, testCase.wantReset, repo.clearedEdges)
			}
			if repo.clearedEdges != testCase.wantReset {
				t.Fatalf("cache type %d cleared edges = %v, want %v",
					testCase.cacheType, repo.clearedEdges, testCase.wantReset)
			}
		})
	}
}

// TestPrepareAnalysisForCacheRerunWithoutAnalysis is the defensive path: a missing
// analysis row must not clear anything.
func TestPrepareAnalysisForCacheRerunWithoutAnalysis(t *testing.T) {
	repo := &graphResetRepoStub{}
	orchestrator := newGraphResetOrchestrator(repo)

	if err := orchestrator.prepareAnalysisForCacheRerun(context.Background(), 7); err != nil {
		t.Fatalf("prepare failed: %v", err)
	}
	if repo.clearedNodes || repo.clearedEdges {
		t.Fatal("a missing analysis must not clear the persisted graph")
	}
}
