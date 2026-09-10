package orchestratorv2

import (
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/google/uuid"
)

// NodeSpec is one materializable process template produced by the runtime
// compiler. A spec is immutable for the duration of a run; every persisted
// analysis_node is derived from exactly one spec.
type NodeSpec struct {
	// AnalysisNodeID is the compiler assigned identity, stable across reruns.
	AnalysisNodeID string
	// NodeID is the runtime identity used by edges.
	NodeID string
	// NodeName is the human readable instance name.
	NodeName string
	// SampleID identifies the scattered sample, empty for singletons.
	SampleID string
	// ScriptID is the component id of the script to execute.
	ScriptID string
	// Executor selects the runtime backend (container, local, nextflow...).
	Executor string
	// InputsPatterns declares input handles and their schemas.
	InputsPatterns types.JSONMap
	// ResolvedInputs holds the statically resolved input payload.
	ResolvedInputs types.JSONMap
	// OutputPatterns declares output handles and their schemas.
	OutputPatterns types.JSONMap
	// ResolvedOutputs holds the statically predicted outputs.
	ResolvedOutputs types.JSONMap
	// Params are the process parameters merged from workflow defaults.
	Params types.JSONMap
	// UpstreamIDs lists declared upstream node identities.
	UpstreamIDs []string
	// DownstreamIDs lists declared downstream node identities.
	DownstreamIDs []string
	// InputValidationErrors lists compile-time input problems.
	InputValidationErrors types.JSONSlice
	// Retry and MaxRetry configure in-place retries.
	Retry    int
	MaxRetry int
	// RerunReason carries a compiler supplied rerun explanation.
	RerunReason string
}

// EdgeSpec is a directed data channel between two node template identities.
type EdgeSpec struct {
	AnalysisEdgeID string
	SourceNode     string
	TargetNode     string
	SourceHandle   string
	TargetHandle   string
}

// Graph is an immutable, indexed view over the compiled runtime DAG.
//
// Keeping a dedicated graph model (instead of passing raw map[string]any rows
// around) is what allows the dependency tracker, reconciler and node builder to
// stay small and free of defensive type assertions.
type Graph struct {
	// AnalysisID is the persisted analysis primary key.
	AnalysisID int64

	order    []string
	nodes    map[string]*NodeSpec
	edges    []*EdgeSpec
	incoming map[string][]*EdgeSpec
	outgoing map[string][]string
}

// NewGraph builds a Graph from the payload returned by compiler.BuildRuntimeTasks.
func NewGraph(analysisID int64, compiled map[string]any) (*Graph, error) {
	if compiled == nil {
		return nil, fmt.Errorf("compiled runtime dag is empty")
	}

	graph := &Graph{
		AnalysisID: analysisID,
		nodes:      make(map[string]*NodeSpec),
		incoming:   make(map[string][]*EdgeSpec),
		outgoing:   make(map[string][]string),
	}

	for _, row := range toMapSlice(compiled["analysis_nodes"]) {
		spec := newNodeSpec(row)
		if spec == nil {
			continue
		}
		if _, exists := graph.nodes[spec.NodeID]; exists {
			continue
		}
		graph.nodes[spec.NodeID] = spec
		graph.order = append(graph.order, spec.NodeID)
	}

	for _, row := range toMapSlice(compiled["analysis_edges"]) {
		edge := newEdgeSpec(row)
		if edge == nil {
			continue
		}
		if _, ok := graph.nodes[edge.SourceNode]; !ok {
			continue
		}
		if _, ok := graph.nodes[edge.TargetNode]; !ok {
			continue
		}
		graph.edges = append(graph.edges, edge)
		graph.incoming[edge.TargetNode] = append(graph.incoming[edge.TargetNode], edge)
		graph.outgoing[edge.SourceNode] = append(graph.outgoing[edge.SourceNode], edge.TargetNode)
	}

	return graph, nil
}

// newNodeSpec converts one compiler row into a NodeSpec. Rows without a node_id
// are not schedulable and are dropped.
func newNodeSpec(row map[string]any) *NodeSpec {
	nodeID := trimmed(row["node_id"])
	if nodeID == "" {
		return nil
	}
	return &NodeSpec{
		AnalysisNodeID:        trimmed(row["analysis_node_id"]),
		NodeID:                nodeID,
		NodeName:              trimmed(row["node_name"]),
		SampleID:              trimmed(row["sample_id"]),
		ScriptID:              trimmed(row["script_id"]),
		Executor:              trimmed(row["executor"]),
		InputsPatterns:        toJSONMap(row["inputs_patterns"]),
		ResolvedInputs:        toJSONMap(row["resolved_inputs"]),
		OutputPatterns:        toJSONMap(row["output_patterns"]),
		ResolvedOutputs:       toJSONMap(row["resolved_outputs"]),
		Params:                toJSONMap(row["params"]),
		UpstreamIDs:           toStringSlice(row["upstream_ids"]),
		DownstreamIDs:         toStringSlice(row["downstream_ids"]),
		InputValidationErrors: toJSONSlice(row["input_validation_errors"]),
		Retry:                 toInt(row["retry"], 0),
		MaxRetry:              toInt(row["max_retry"], 3),
		RerunReason:           trimmed(row["rerun_reason"]),
	}
}

// newEdgeSpec converts one compiler edge row into an EdgeSpec.
func newEdgeSpec(row map[string]any) *EdgeSpec {
	source := trimmed(row["source_node"])
	target := trimmed(row["target_node"])
	if source == "" || target == "" {
		return nil
	}
	return &EdgeSpec{
		AnalysisEdgeID: fallbackString(trimmed(row["analysis_edge_id"]), uuid.NewString()),
		SourceNode:     source,
		TargetNode:     target,
		SourceHandle:   trimmed(row["source_handle"]),
		TargetHandle:   trimmed(row["target_handle"]),
	}
}

// Len returns the number of schedulable templates.
func (g *Graph) Len() int { return len(g.order) }

// Order returns node identities in compiler (topological) order.
func (g *Graph) Order() []string {
	out := make([]string, len(g.order))
	copy(out, g.order)
	return out
}

// Node resolves a node template by identity.
func (g *Graph) Node(nodeID string) (*NodeSpec, bool) {
	spec, ok := g.nodes[strings.TrimSpace(nodeID)]
	return spec, ok
}

// Incoming returns the edges whose target is nodeID.
func (g *Graph) Incoming(nodeID string) []*EdgeSpec {
	return g.incoming[strings.TrimSpace(nodeID)]
}

// UpstreamDependencies returns the de-duplicated upstream identities of nodeID
// restricted to templates that actually exist in the graph.
func (g *Graph) UpstreamDependencies(nodeID string) []string {
	nodeID = strings.TrimSpace(nodeID)
	collected := make([]string, 0)
	for _, edge := range g.incoming[nodeID] {
		collected = append(collected, edge.SourceNode)
	}
	if spec, ok := g.nodes[nodeID]; ok {
		collected = append(collected, spec.UpstreamIDs...)
	}
	deps := compactStrings(collected)
	out := make([]string, 0, len(deps))
	for _, dep := range deps {
		if _, ok := g.nodes[dep]; ok {
			out = append(out, dep)
		}
	}
	return out
}

// DownstreamNodes returns the direct downstream identities of nodeID.
func (g *Graph) DownstreamNodes(nodeID string) []string {
	return compactStrings(g.outgoing[strings.TrimSpace(nodeID)])
}

// AnalysisEdges materialises the graph edges as persistence entities.
func (g *Graph) AnalysisEdges() []*types.AnalysisEdge {
	out := make([]*types.AnalysisEdge, 0, len(g.edges))
	for _, edge := range g.edges {
		out = append(out, &types.AnalysisEdge{
			AnalysisEdgeID: edge.AnalysisEdgeID,
			AnalysisID:     g.AnalysisID,
			SourceNode:     edge.SourceNode,
			TargetNode:     edge.TargetNode,
			SourceHandle:   edge.SourceHandle,
			TargetHandle:   edge.TargetHandle,
		})
	}
	return out
}

// indexNodesByNodeID builds a lookup table keyed by runtime node identity.
func indexNodesByNodeID(nodes []*types.AnalysisNode) map[string]*types.AnalysisNode {
	out := make(map[string]*types.AnalysisNode, len(nodes))
	for _, node := range nodes {
		if node == nil {
			continue
		}
		nodeID := strings.TrimSpace(node.NodeID)
		if nodeID == "" {
			continue
		}
		out[nodeID] = node
	}
	return out
}
