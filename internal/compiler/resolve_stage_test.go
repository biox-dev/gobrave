package compiler

import "testing"

func TestBuildRuntimeTasks_ScatterEachMapsPluralFieldToSingularInput(t *testing.T) {
	runtimeDAG, err := BuildRuntimeTasks(1, map[string]any{
		"seuratObjects": []any{
			map[string]any{"file_name": "sample-1.rds", "sample_id": "s1"},
			map[string]any{"file_name": "sample-2.rds", "sample_id": "s2"},
		},
	}, map[string]any{}, map[string]any{
		"nodes": []any{
			map[string]any{
				"id":   "node-1",
				"name": "Seurat",
				"inputs": map[string]any{
					"seuratObject": map[string]any{
						"type":     "NestSelectSample",
						"required": true,
					},
				},
				"outputs": map[string]any{
					"summary": map[string]any{"pattern": "output.md", "required": true, "type": "file"},
				},
				"scatter": map[string]any{
					"mode":  "each",
					"field": "seuratObjects",
				},
			},
		},
		"edges": []any{},
	})
	if err != nil {
		t.Fatalf("BuildRuntimeTasks failed: %v", err)
	}

	nodesAny, ok := runtimeDAG["analysis_nodes"].([]map[string]any)
	if !ok {
		t.Fatalf("analysis_nodes type mismatch: %T", runtimeDAG["analysis_nodes"])
	}
	if len(nodesAny) != 2 {
		t.Fatalf("expected 2 analysis nodes, got %d", len(nodesAny))
	}

	for i, node := range nodesAny {
		resolvedInputs, ok := node["resolved_inputs"].(map[string]any)
		if !ok {
			t.Fatalf("node %d resolved_inputs type mismatch: %T", i, node["resolved_inputs"])
		}
		if _, exists := resolvedInputs["seuratObject"]; !exists {
			t.Fatalf("node %d missing resolved_inputs.seuratObject", i)
		}
	}
}

func TestBuildRuntimeTasks_DownstreamSampleNodeKeepsGlobalMetadata(t *testing.T) {
	metadata := map[string]any{
		"path":       "/tmp/metadata.tsv",
		"sample_col": "sample",
		"group_col":  "group",
	}

	runtimeDAG, err := BuildRuntimeTasks(1, map[string]any{
		"abundances": []any{
			map[string]any{
				"analysis_result_id": "a1",
				"file_name":          "abundance.tsv",
				"path":               "/tmp/abundance.tsv",
			},
		},
		"metadata": metadata,
	}, map[string]any{}, map[string]any{
		"nodes": []any{
			map[string]any{
				"id":   "node-lda",
				"name": "LDA",
				"inputs": map[string]any{
					"abundance": map[string]any{"required": true, "type": "BaseInput"},
				},
				"outputs": map[string]any{
					"sample_topic": map[string]any{"pattern": "{sample}.sample_topic.tsv", "required": true, "type": "file"},
				},
				"scatter": map[string]any{
					"mode":  "each",
					"field": "abundances",
				},
			},
			map[string]any{
				"id":   "node-roc",
				"name": "ROC",
				"inputs": map[string]any{
					"feature_table": map[string]any{"required": true, "type": "BaseInput"},
					"metadata":      map[string]any{"required": true, "type": "CollectedSampleSelect"},
				},
				"outputs": map[string]any{
					"summary": map[string]any{"pattern": "summary.md", "required": true, "type": "file"},
				},
			},
		},
		"edges": []any{
			map[string]any{
				"source":       "node-lda",
				"sourceHandle": "sample_topic",
				"target":       "node-roc",
				"targetHandle": "feature_table",
			},
		},
	})
	if err != nil {
		t.Fatalf("BuildRuntimeTasks failed: %v", err)
	}

	nodes, ok := runtimeDAG["analysis_nodes"].([]map[string]any)
	if !ok {
		t.Fatalf("analysis_nodes type mismatch: %T", runtimeDAG["analysis_nodes"])
	}

	var rocNode map[string]any
	for _, node := range nodes {
		if node["script_id"] == "node-roc" {
			rocNode = node
			break
		}
	}
	if rocNode == nil {
		t.Fatalf("expected ROC node in analysis_nodes")
	}

	resolvedInputs, ok := rocNode["resolved_inputs"].(map[string]any)
	if !ok {
		t.Fatalf("resolved_inputs type mismatch: %T", rocNode["resolved_inputs"])
	}

	resolvedMetadata, ok := resolvedInputs["metadata"].(map[string]any)
	if !ok {
		t.Fatalf("resolved metadata type mismatch: %T", resolvedInputs["metadata"])
	}

	if resolvedMetadata["path"] != metadata["path"] {
		t.Fatalf("metadata.path mismatch: got %v want %v", resolvedMetadata["path"], metadata["path"])
	}
	if resolvedMetadata["group_col"] != metadata["group_col"] {
		t.Fatalf("metadata.group_col mismatch: got %v want %v", resolvedMetadata["group_col"], metadata["group_col"])
	}
	if resolvedMetadata["analysis_result_id"] != nil {
		t.Fatalf("metadata should not be replaced by upstream sample payload: %+v", resolvedMetadata)
	}
}
