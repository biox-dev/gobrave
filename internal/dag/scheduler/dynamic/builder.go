package dynamic

import (
	"github.com/biox-dev/gobrave/internal/dag/nodebuild"
	"github.com/biox-dev/gobrave/internal/types"
)

// This file is the V2 side of the shared node construction.
//
// The actual "compiled template -> analysis_node row" translation lives in
// internal/dag/nodebuild, together with the workspace layout and the instance
// identity. V2 only contributes its own row dialect (the loosely typed maps the
// runtime compiler emits) and translates it into a nodebuild.Spec.
//
// Sharing the builder is what makes the cache bidirectional: a node V2 persists
// carries the same layout, the same input_hash and therefore the same identity
// as one V3 would persist, so either scheduler can reuse the other's work.

// dynamicNodeSpecFromRow translates one compiler row into the shared builder
// spec. nodeID is the plan key and wins over the row's own node_id, because the
// plan already normalized edge references to that key.
func dynamicNodeSpecFromRow(nodeID string, scriptID int64, row map[string]any) *nodebuild.Spec {
	return &nodebuild.Spec{
		// The compiler-assigned identity is carried through verbatim so a rerun
		// keeps matching its previous row.
		AnalysisNodeID:        dynamicToString(row["analysis_node_id"]),
		NodeID:                nodeID,
		NodeName:              dynamicToString(row["node_name"]),
		SampleID:              dynamicToString(row["sample_id"]),
		ScriptID:              scriptID,
		Executor:              dynamicToString(row["executor"]),
		InputsPatterns:        dynamicToJSONMap(row["inputs_patterns"]),
		ResolvedInputs:        dynamicToJSONMap(row["resolved_inputs"]),
		OutputPatterns:        dynamicToJSONMap(row["output_patterns"]),
		ResolvedOutputs:       dynamicToJSONMap(row["resolved_outputs"]),
		Params:                dynamicToJSONMap(row["params"]),
		UpstreamIDs:           dynamicToStringSlice(row["upstream_ids"]),
		DownstreamIDs:         dynamicToStringSlice(row["downstream_ids"]),
		InputValidationErrors: dynamicToJSONSlice(row["input_validation_errors"]),
		Retry:                 dynamicIntFromAny(row["retry"], 0),
		MaxRetry:              dynamicIntFromAny(row["max_retry"], 3),
		RerunReason:           dynamicToString(row["rerun_reason"]),
	}
}

// scriptPrimaryKey guards the nil script the fingerprint path can carry.
func scriptPrimaryKey(script *types.Script) int64 {
	if script == nil {
		return 0
	}
	return script.ID
}

// buildDynamicAnalysisNode materializes one compiled template into a persisted
// analysis_node row through the shared builder.
func (o *dynamicDagOrchestratorV2) buildDynamicAnalysisNode(
	script *types.Script,
	analysis *types.Analysis,
	nodeID string,
	row map[string]any,
	incomingEdges []*types.AnalysisEdge,
	existingByNodeID map[string]*types.AnalysisNode,
	status string,
	errorMessage string,
) (*types.AnalysisNode, error) {
	return nodebuild.Materialize(nodebuild.MaterializeRequest{
		Analysis:     analysis,
		Spec:         dynamicNodeSpecFromRow(nodeID, scriptPrimaryKey(script), row),
		Status:       status,
		ErrorMessage: errorMessage,
		Incoming:     incomingEdges,
		Upstream:     existingByNodeID,
	})
}

// buildCacheProbe rebuilds the node payload from the freshly compiled template so
// the cache policies can compare what would run now against what ran last time.
func (o *dynamicDagOrchestratorV2) buildCacheProbe(
	script *types.Script,
	existingNode *types.AnalysisNode,
	nodeID string,
	row map[string]any,
	incomingEdges []*types.AnalysisEdge,
	existingByNodeID map[string]*types.AnalysisNode,
) *types.AnalysisNode {
	if existingNode == nil {
		return nil
	}
	probe, err := nodebuild.Probe(nodebuild.ProbeRequest{
		Spec:     dynamicNodeSpecFromRow(nodeID, scriptPrimaryKey(script), row),
		Existing: existingNode,
		Incoming: incomingEdges,
		Upstream: existingByNodeID,
	})
	if err != nil {
		// Probing must never fail the run: fall back to the persisted node, which
		// makes the cache policy decide from the stored row alone.
		return existingNode
	}
	return probe
}
