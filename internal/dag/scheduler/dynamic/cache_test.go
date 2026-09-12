package dynamic

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
