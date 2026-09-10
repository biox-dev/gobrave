package orchestratorv2

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/biox-dev/gobrave/internal/compiler"
)

// compiledFixture mirrors the payload produced by compiler.BuildRuntimeTasks:
// a single producer node (a1) feeding a single consumer node (b1).
func compiledFixture() map[string]any {
	return map[string]any{
		"analysis_nodes": []any{
			map[string]any{
				"analysis_node_id": "template-a1",
				"node_id":          "a1",
				"node_name":        "Alpha",
				"script_id":        "script-a",
				"executor":         "container",
				"params":           map[string]any{"alpha": 1},
				"inputs_patterns":  map[string]any{},
				"output_patterns":  map[string]any{"out": map[string]any{"type": "file"}},
				"resolved_inputs":  map[string]any{},
				"resolved_outputs": map[string]any{},
				"upstream_ids":     []any{},
				"downstream_ids":   []any{"b1"},
				"max_retry":        2,
			},
			map[string]any{
				"analysis_node_id": "template-b1",
				"node_id":          "b1",
				"node_name":        "Beta",
				"script_id":        "script-b",
				"executor":         "container",
				"params":           map[string]any{},
				"inputs_patterns": map[string]any{
					"in": map[string]any{"type": "file", "multiple": true},
				},
				"output_patterns":  map[string]any{},
				"resolved_inputs":  map[string]any{},
				"resolved_outputs": map[string]any{},
				"upstream_ids":     []any{"a1"},
				"downstream_ids":   []any{},
			},
		},
		"analysis_edges": []any{
			map[string]any{
				"source_node":   "a1",
				"target_node":   "b1",
				"source_handle": "out",
				"target_handle": "in",
			},
		},
	}
}

func TestNewGraphBuildsIndexAndDropsDanglingEdges(t *testing.T) {
	compiled := compiledFixture()
	// An edge pointing at a node that does not exist must be ignored.
	compiled["analysis_edges"] = append(
		compiled["analysis_edges"].([]any),
		map[string]any{"source_node": "b1", "target_node": "ghost", "source_handle": "o", "target_handle": "i"},
	)

	graph, err := NewGraph(42, compiled)
	if err != nil {
		t.Fatalf("NewGraph returned error: %v", err)
	}
	if graph.Len() != 2 {
		t.Fatalf("expected 2 nodes, got %d", graph.Len())
	}
	if order := graph.Order(); order[0] != "a1" || order[1] != "b1" {
		t.Fatalf("unexpected order %v", order)
	}

	spec, ok := graph.Node("b1")
	if !ok {
		t.Fatal("expected node b1 to exist")
	}
	if spec.InputsPatterns["in"] == nil {
		t.Fatal("expected inputs_patterns to be preserved")
	}
	if spec.UpstreamIDs[0] != "a1" {
		t.Fatalf("unexpected upstream ids %v", spec.UpstreamIDs)
	}

	if deps := graph.UpstreamDependencies("b1"); len(deps) != 1 || deps[0] != "a1" {
		t.Fatalf("unexpected upstream dependencies %v", deps)
	}
	if deps := graph.UpstreamDependencies("a1"); len(deps) != 0 {
		t.Fatalf("expected no dependencies for a1, got %v", deps)
	}

	edges := graph.AnalysisEdges()
	if len(edges) != 1 {
		t.Fatalf("expected 1 persisted edge, got %d", len(edges))
	}
	if edges[0].AnalysisID != 42 || edges[0].AnalysisEdgeID == "" {
		t.Fatalf("edge not fully materialized: %+v", edges[0])
	}
}

func TestNewGraphAcceptsRealCompilerOutput(t *testing.T) {
	// The debug payload written by AnalysisHandler is used as a realistic
	// integration fixture when it is present on the machine running the tests.
	const fixturePath = "/data2/brave_analysis_workspace/debug/876/V2/input.json"
	raw, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Skipf("fixture %s not available: %v", fixturePath, err)
	}

	payload := struct {
		AnalysisID          json.Number    `json:"analysis_id"`
		ParseAnalysisResult map[string]any `json:"parse_analysis_result"`
		DagDefinition       map[string]any `json:"dag_definition"`
	}{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode fixture failed: %v", err)
	}

	analysisID, err := payload.AnalysisID.Int64()
	if err != nil {
		t.Fatalf("invalid analysis id: %v", err)
	}

	compiled, err := compiler.BuildRuntimeTasks(analysisID, payload.ParseAnalysisResult, payload.DagDefinition)
	if err != nil {
		t.Fatalf("compile fixture failed: %v", err)
	}

	graph, err := NewGraph(analysisID, compiled)
	if err != nil {
		t.Fatalf("NewGraph returned error: %v", err)
	}
	if graph.Len() == 0 {
		t.Fatal("expected the fixture to produce at least one node")
	}

	tracker := NewDependencyTracker(graph)
	// Every node must be reachable from the tracker so no run can stall.
	for _, nodeID := range graph.Order() {
		if tracker.PendingUpstreams(nodeID) != len(graph.UpstreamDependencies(nodeID)) {
			t.Fatalf("tracker did not seed node %s", nodeID)
		}
	}
}
