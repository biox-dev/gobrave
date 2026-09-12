package dynamic

import (
	"strings"
	"sync"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
)

// CacheFacts is the immutable input of a cache policy decision.
type CacheFacts struct {
	// CacheType is the analysis cache_type that selected the policy.
	CacheType int
	// Existing is the node persisted by a previous run.
	Existing *types.AnalysisNode
	// ProbeCommandMD5 is the fingerprint of the run script that would run now.
	// It is only populated when the policy asks for a fingerprint.
	ProbeCommandMD5 string
	// ProbeParamsMD5 is the fingerprint of the params payload that would be used.
	ProbeParamsMD5 string
}

// CacheDecision tells the reconciler whether the existing node must rerun.
type CacheDecision struct {
	// Rerun requests re-materialization of the node as ready.
	Rerun bool
	// Reason is persisted on the node for observability.
	Reason string
}

// CachePolicy is the Strategy implementation behind analysis.cache_type.
//
// Implementations must be stateless (they may be shared across runs) and must
// not perform I/O: the reconciler prepares fingerprints and hands them over.
type CachePolicy interface {
	// Name is a stable identifier used in logs and diagnostics.
	Name() string
	// RequiresFingerprint reports whether Decide needs the probe fingerprints.
	RequiresFingerprint() bool
	// Decide returns the rerun decision for an already persisted node.
	Decide(facts CacheFacts) CacheDecision
}

// CachePolicyRegistry resolves a CachePolicy per analysis cache type.
type CachePolicyRegistry struct {
	mu       sync.RWMutex
	policies map[int]CachePolicy
	fallback CachePolicy
}

// NewCachePolicyRegistry builds the default registry covering every cache type
// declared in types.Analysis.
func NewCachePolicyRegistry() *CachePolicyRegistry {
	registry := &CachePolicyRegistry{
		policies: make(map[int]CachePolicy),
		// Unknown cache types are treated as "reuse whatever exists".
		fallback: ReuseExistingPolicy{},
	}
	registry.Register(types.CacheTypeReuseExistingNode, ReuseExistingPolicy{})
	registry.Register(types.CacheTypeReuseWhenScriptUnchanged, ScriptFingerprintPolicy{})
	registry.Register(types.CacheTypeReuseWhenScriptAndParamsUnchanged, ScriptAndParamsFingerprintPolicy{})
	registry.Register(types.CacheTypeRerunAll, RerunAllPolicy{})
	return registry
}

// Register binds a policy to a cache type, replacing any previous entry.
func (r *CachePolicyRegistry) Register(cacheType int, policy CachePolicy) {
	if r == nil || policy == nil {
		return
	}
	r.mu.Lock()
	r.policies[cacheType] = policy
	r.mu.Unlock()
}

// Resolve returns the policy for cacheType, falling back to the safe default.
func (r *CachePolicyRegistry) Resolve(cacheType int) CachePolicy {
	if r == nil {
		return ReuseExistingPolicy{}
	}
	r.mu.RLock()
	policy, ok := r.policies[cacheType]
	r.mu.RUnlock()
	if ok && policy != nil {
		return policy
	}
	return r.fallback
}

// ReuseExistingPolicy keeps whatever is persisted (types.CacheTypeReuseExistingNode).
type ReuseExistingPolicy struct{}

// Name implements CachePolicy.
func (ReuseExistingPolicy) Name() string { return "reuse_existing" }

// RequiresFingerprint implements CachePolicy.
func (ReuseExistingPolicy) RequiresFingerprint() bool { return false }

// Decide implements CachePolicy.
func (ReuseExistingPolicy) Decide(facts CacheFacts) CacheDecision {
	if decision, rerun := rerunWhenNotReusable(facts.Existing); rerun {
		return decision
	}
	return CacheDecision{}
}

// RerunAllPolicy always reruns (types.CacheTypeRerunAll).
//
// The run preparation step already deletes persisted nodes for this cache type,
// so the policy is mostly a defensive fallback.
type RerunAllPolicy struct{}

// Name implements CachePolicy.
func (RerunAllPolicy) Name() string { return "rerun_all" }

// RequiresFingerprint implements CachePolicy.
func (RerunAllPolicy) RequiresFingerprint() bool { return false }

// Decide implements CachePolicy.
func (RerunAllPolicy) Decide(CacheFacts) CacheDecision {
	return CacheDecision{Rerun: true, Reason: "cache disabled for this analysis"}
}

// ScriptFingerprintPolicy reruns when the generated run script changed
// (types.CacheTypeReuseWhenScriptUnchanged).
type ScriptFingerprintPolicy struct{}

// Name implements CachePolicy.
func (ScriptFingerprintPolicy) Name() string { return "reuse_when_script_unchanged" }

// RequiresFingerprint implements CachePolicy.
func (ScriptFingerprintPolicy) RequiresFingerprint() bool { return true }

// Decide implements CachePolicy.
func (ScriptFingerprintPolicy) Decide(facts CacheFacts) CacheDecision {
	if decision, rerun := rerunWhenNotReusable(facts.Existing); rerun {
		return decision
	}
	if !sameFingerprint(facts.ProbeCommandMD5, commandMD5Of(facts.Existing)) {
		return CacheDecision{Rerun: true, Reason: "command changed"}
	}
	return CacheDecision{}
}

// ScriptAndParamsFingerprintPolicy reruns when either the generated run script
// or the params payload changed
// (types.CacheTypeReuseWhenScriptAndParamsUnchanged).
type ScriptAndParamsFingerprintPolicy struct{}

// Name implements CachePolicy.
func (ScriptAndParamsFingerprintPolicy) Name() string {
	return "reuse_when_script_and_params_unchanged"
}

// RequiresFingerprint implements CachePolicy.
func (ScriptAndParamsFingerprintPolicy) RequiresFingerprint() bool { return true }

// Decide implements CachePolicy.
func (ScriptAndParamsFingerprintPolicy) Decide(facts CacheFacts) CacheDecision {
	if decision, rerun := rerunWhenNotReusable(facts.Existing); rerun {
		return decision
	}

	commandChanged := !sameFingerprint(facts.ProbeCommandMD5, commandMD5Of(facts.Existing))
	paramsChanged := !sameFingerprint(facts.ProbeParamsMD5, paramsMD5Of(facts.Existing))

	switch {
	case commandChanged && paramsChanged:
		return CacheDecision{Rerun: true, Reason: "command and params changed"}
	case commandChanged:
		return CacheDecision{Rerun: true, Reason: "command changed"}
	case paramsChanged:
		return CacheDecision{Rerun: true, Reason: "params changed"}
	default:
		return CacheDecision{}
	}
}

// rerunWhenNotReusable forces a rerun for nodes that have no reusable result.
//
// Only successful nodes are eligible for cache reuse; a node left running, or
// terminated without success by a previous run, must be executed again.
func rerunWhenNotReusable(node *types.AnalysisNode) (CacheDecision, bool) {
	if node == nil {
		return CacheDecision{}, false
	}
	status := normaliseNodeStatus(node)
	if status == "" || status == dagruntime.StatusPending {
		return CacheDecision{Rerun: true, Reason: "node has no execution state"}, true
	}
	if dagruntime.IsTerminalStatus(status) && !isSuccessNode(node) {
		return CacheDecision{Rerun: true, Reason: "previous execution did not succeed"}, true
	}
	return CacheDecision{}, false
}

// normaliseNodeStatus lowercases and trims a persisted node status.
func normaliseNodeStatus(node *types.AnalysisNode) string {
	if node == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(node.Status))
}

// sameFingerprint compares two MD5 digests tolerantly (blank equals blank).
func sameFingerprint(left, right string) bool {
	return strings.TrimSpace(left) == strings.TrimSpace(right)
}

func commandMD5Of(node *types.AnalysisNode) string {
	if node == nil {
		return ""
	}
	return node.CommandMD5
}

func paramsMD5Of(node *types.AnalysisNode) string {
	if node == nil {
		return ""
	}
	return node.ParamsMD5
}
