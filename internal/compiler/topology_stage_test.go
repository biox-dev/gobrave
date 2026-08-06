package compiler

import "testing"

func TestTopologyStage_NormalizesEdgeNodeAliases(t *testing.T) {
	ctx := NewCompileContext(1, map[string]any{}, map[string]any{
		"nodes": []any{
			map[string]any{
				"id":      "A",
				"node_id": "A_1",
				"name":    "node-a",
			},
			map[string]any{
				"id":      "B",
				"node_id": "B_1",
				"name":    "node-b",
			},
		},
		"edges": []any{
			map[string]any{
				"source":       "A_1",
				"target":       "B_1",
				"sourceHandle": "tsv",
				"targetHandle": "tsv",
			},
		},
	})

	stage := &TopologyStage{}
	if err := stage.Run(ctx); err != nil {
		t.Fatalf("TopologyStage.Run failed: %v", err)
	}

	if len(ctx.Outgoing["A_1"]) != 1 {
		t.Fatalf("expected 1 outgoing edge for A_1, got %d", len(ctx.Outgoing["A_1"]))
	}
	if len(ctx.Incoming["B_1"]) != 1 {
		t.Fatalf("expected 1 incoming edge for B_1, got %d", len(ctx.Incoming["B_1"]))
	}

	edge := ctx.Outgoing["A_1"][0]
	if got := edge["source"]; got != "A_1" {
		t.Fatalf("expected normalized source=A_1, got %v", got)
	}
	if got := edge["target"]; got != "B_1" {
		t.Fatalf("expected normalized target=B_1, got %v", got)
	}
}
