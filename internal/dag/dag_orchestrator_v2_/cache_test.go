package orchestratorv2

import (
	"testing"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
)

func TestCachePolicyRegistryResolvesEveryCacheType(t *testing.T) {
	registry := NewCachePolicyRegistry()

	cases := []struct {
		cacheType int
		want      string
	}{
		{types.CacheTypeReuseExistingNode, "reuse_existing"},
		{types.CacheTypeReuseWhenScriptUnchanged, "reuse_when_script_unchanged"},
		{types.CacheTypeReuseWhenScriptAndParamsUnchanged, "reuse_when_script_and_params_unchanged"},
		{types.CacheTypeRerunAll, "rerun_all"},
		{9999, "reuse_existing"},
	}
	for _, testCase := range cases {
		if got := registry.Resolve(testCase.cacheType).Name(); got != testCase.want {
			t.Fatalf("cache type %d resolved to %q, want %q", testCase.cacheType, got, testCase.want)
		}
	}
}

func TestScriptFingerprintPolicy(t *testing.T) {
	existing := &types.AnalysisNode{
		NodeID:     "a1",
		Status:     dagruntime.StatusDone,
		CommandMD5: "aaa",
		ParamsMD5:  "bbb",
	}

	policy := ScriptFingerprintPolicy{}
	if !policy.RequiresFingerprint() {
		t.Fatal("the script policy needs fingerprints")
	}
	if decision := policy.Decide(CacheFacts{Existing: existing, ProbeCommandMD5: "aaa"}); decision.Rerun {
		t.Fatalf("unchanged command must be reused, got %+v", decision)
	}
	decision := policy.Decide(CacheFacts{Existing: existing, ProbeCommandMD5: "zzz"})
	if !decision.Rerun || decision.Reason != "command changed" {
		t.Fatalf("changed command must rerun, got %+v", decision)
	}
	// Params are ignored by this policy.
	if decision := policy.Decide(CacheFacts{Existing: existing, ProbeCommandMD5: "aaa", ProbeParamsMD5: "zzz"}); decision.Rerun {
		t.Fatalf("params must be ignored, got %+v", decision)
	}
}

func TestScriptAndParamsFingerprintPolicy(t *testing.T) {
	existing := &types.AnalysisNode{NodeID: "a1", Status: dagruntime.StatusDone, CommandMD5: "aaa", ParamsMD5: "bbb"}
	policy := ScriptAndParamsFingerprintPolicy{}

	decision := policy.Decide(CacheFacts{Existing: existing, ProbeCommandMD5: "zzz", ProbeParamsMD5: "zzz"})
	if !decision.Rerun || decision.Reason != "command and params changed" {
		t.Fatalf("unexpected decision %+v", decision)
	}
	decision = policy.Decide(CacheFacts{Existing: existing, ProbeCommandMD5: "aaa", ProbeParamsMD5: "zzz"})
	if !decision.Rerun || decision.Reason != "params changed" {
		t.Fatalf("unexpected decision %+v", decision)
	}
	if decision = policy.Decide(CacheFacts{Existing: existing, ProbeCommandMD5: "aaa", ProbeParamsMD5: "bbb"}); decision.Rerun {
		t.Fatalf("identical fingerprints must reuse, got %+v", decision)
	}
}

func TestPoliciesRerunNodesWithoutReusableResult(t *testing.T) {
	statuses := []string{dagruntime.StatusFailed, dagruntime.StatusStopped, dagruntime.StatusSkipped, "", dagruntime.StatusPending}
	for _, status := range statuses {
		existing := &types.AnalysisNode{NodeID: "a1", Status: status, CommandMD5: "aaa", ParamsMD5: "bbb"}
		facts := CacheFacts{Existing: existing, ProbeCommandMD5: "aaa", ProbeParamsMD5: "bbb"}

		for _, policy := range []CachePolicy{ReuseExistingPolicy{}, ScriptFingerprintPolicy{}, ScriptAndParamsFingerprintPolicy{}} {
			if decision := policy.Decide(facts); !decision.Rerun {
				t.Fatalf("policy %s must rerun a %q node, got %+v", policy.Name(), status, decision)
			}
		}
	}
}

func TestReusePolicyKeepsSuccessfulNodes(t *testing.T) {
	existing := &types.AnalysisNode{NodeID: "a1", Status: dagruntime.StatusDone}
	if decision := (ReuseExistingPolicy{}).Decide(CacheFacts{Existing: existing}); decision.Rerun {
		t.Fatalf("successful node must be reused, got %+v", decision)
	}
	existing = &types.AnalysisNode{NodeID: "a1", Status: dagruntime.StatusReady, CacheHit: true}
	if decision := (ReuseExistingPolicy{}).Decide(CacheFacts{Existing: existing}); decision.Rerun {
		t.Fatalf("cache hit must be reused, got %+v", decision)
	}
}

func TestRetryPolicies(t *testing.T) {
	if (NoRetryPolicy{}).ShouldRetry(&types.AnalysisNode{Retry: 0, MaxRetry: 5}) {
		t.Fatal("the default policy must never retry")
	}
	maxRetry := MaxRetryPolicy{}
	if !maxRetry.ShouldRetry(&types.AnalysisNode{Retry: 0, MaxRetry: 2}) {
		t.Fatal("expected a first retry")
	}
	if maxRetry.ShouldRetry(&types.AnalysisNode{Retry: 2, MaxRetry: 2}) {
		t.Fatal("expected the retry budget to be exhausted")
	}
}

func TestMergeUpstreamInputsHonoursFanIn(t *testing.T) {
	spec := &NodeSpec{
		NodeID: "b1",
		InputsPatterns: types.JSONMap{
			"in": map[string]any{"multiple": true},
		},
		Params:         types.JSONMap{},
		ResolvedInputs: types.JSONMap{},
	}
	params := cloneJSONMap(spec.Params)
	resolved := cloneJSONMap(spec.ResolvedInputs)

	upstream := map[string]*types.AnalysisNode{
		"a1": {NodeID: "a1", Status: dagruntime.StatusDone, ResolvedOutputs: types.JSONMap{"out": "v1"}},
		"a2": {NodeID: "a2", Status: dagruntime.StatusDone, ResolvedOutputs: types.JSONMap{"out": "v2"}},
		"a3": {NodeID: "a3", Status: dagruntime.StatusFailed},
	}
	incoming := []*EdgeSpec{
		{SourceNode: "a1", SourceHandle: "out", TargetNode: "b1", TargetHandle: "in"},
		{SourceNode: "a2", SourceHandle: "out", TargetNode: "b1", TargetHandle: "in"},
		{SourceNode: "a3", SourceHandle: "out", TargetNode: "b1", TargetHandle: "in"},
	}

	mergeUpstreamInputs(spec, params, resolved, incoming, upstream)

	values, ok := params["in"].([]any)
	if !ok || len(values) != 2 {
		t.Fatalf("expected two fan-in values, got %#v", params["in"])
	}
	if values[0] != "v1" || values[1] != "v2" {
		t.Fatalf("unexpected fan-in payload %#v", values)
	}
	if _, exists := resolved["in"]; !exists {
		t.Fatal("resolved inputs must be populated as well")
	}
}
