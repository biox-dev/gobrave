package nodebuild

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
)

// InstanceInputHash is the stable identity of one materialized node instance.
//
// It hashes the runtime node id together with the resolved inputs and the
// effective params, which is exactly the payload that decides what a node runs.
// Two schedulers that build the same node therefore compute the same hash, which
// is what allows them to recognize each other's persisted rows as the same
// instance (cache hit) instead of materializing a duplicate.
//
// Hashes are persisted on analysis_nodes.input_hash. The payload shape (and the
// "{}" encoding of empty maps) is part of the contract and must not change
// without a migration, otherwise historical rows stop matching.
func InstanceInputHash(nodeID string, resolvedInputs types.JSONMap, params types.JSONMap) string {
	payload := map[string]any{
		"node_id":         strings.TrimSpace(nodeID),
		"resolved_inputs": cloneJSONMap(resolvedInputs),
		"params":          cloneJSONMap(params),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		// Marshaling a JSON-compatible payload cannot fail; keep a deterministic
		// fallback so a caller never sees an empty identity.
		fallback := sha256.Sum256([]byte(strings.TrimSpace(nodeID)))
		return hex.EncodeToString(fallback[:])
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// SpecInstanceHash computes the instance identity for a spec after its upstream
// inputs have been merged, using the same payload the builder persists.
func SpecInstanceHash(spec *Spec, resolvedInputs types.JSONMap, params types.JSONMap) string {
	if spec == nil {
		return ""
	}
	return InstanceInputHash(spec.NodeID, resolvedInputs, params)
}

// MatchInstance finds the persisted node that represents the same instance as
// the probe.
//
// Matching is by node id first and then by instance hash. Rows persisted before
// input_hash existed carry an empty hash; for those, a successful (or cache-hit)
// node with the same node id is accepted, so legacy rows stay reusable. When
// several instances share a node id (scatter), the exact hash wins.
func MatchInstance(existing []*types.AnalysisNode, nodeID string, inputHash string) *types.AnalysisNode {
	nodeID = strings.TrimSpace(nodeID)
	inputHash = strings.TrimSpace(inputHash)
	if nodeID == "" {
		return nil
	}

	var legacy *types.AnalysisNode
	for _, item := range existing {
		if item == nil {
			continue
		}
		if strings.TrimSpace(item.NodeID) != nodeID {
			continue
		}
		storedHash := strings.TrimSpace(item.InputHash)
		if inputHash != "" && storedHash == inputHash {
			return item
		}
		if storedHash == "" && isReusable(item) {
			if legacy == nil {
				legacy = item
			}
		}
	}
	return legacy
}

// isReusable reports whether a persisted node already holds a result a later run
// may treat as the same instance.
func isReusable(node *types.AnalysisNode) bool {
	if node == nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(node.Status))
	if status == dagruntime.StatusReady && node.CacheHit {
		return true
	}
	return dagruntime.IsSuccessStatus(status)
}
