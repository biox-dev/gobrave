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

// TestBuildRuntimeTasks_ScatterEachFillsRequestParamByHandleName 验证散列(each)节点
// 把 parse_analysis_result 的 item 按句柄名命中的原始值填进 request_param，而不再直接
// 写进 resolved_inputs —— 结构化 resolved_inputs 由落库时（nodebuild.Materialize）
// 依据 request_param 解析得到。
func TestBuildRuntimeTasks_ScatterEachFillsRequestParamByHandleName(t *testing.T) {
	runtimeDAG, err := BuildRuntimeTasks(1, map[string]any{
		"sample_list": []any{
			map[string]any{"node_name": "OL-1", "sample": map[string]any{"files": "aaaa"}},
			map[string]any{"node_name": "OL-2", "sample": map[string]any{"files": "bbbb"}},
		},
	}, map[string]any{}, map[string]any{
		"nodes": []any{
			map[string]any{
				"id":   "node-assay",
				"name": "Assay",
				"inputs": map[string]any{
					"sample": map[string]any{"type": "selectassay", "required": true},
				},
				"outputs": map[string]any{
					"out": map[string]any{"pattern": "out.txt", "required": true, "type": "file"},
				},
				"scatter": map[string]any{
					"mode":  "each",
					"field": "sample_list",
				},
			},
		},
		"edges": []any{},
	})
	if err != nil {
		t.Fatalf("BuildRuntimeTasks failed: %v", err)
	}

	nodes, ok := runtimeDAG["analysis_nodes"].([]map[string]any)
	if !ok {
		t.Fatalf("analysis_nodes type mismatch: %T", runtimeDAG["analysis_nodes"])
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 analysis nodes, got %d", len(nodes))
	}

	wantFiles := []string{"aaaa", "bbbb"}
	for i, node := range nodes {
		requestParam, ok := node["request_param"].(map[string]any)
		if !ok {
			t.Fatalf("node %d request_param type mismatch: %T", i, node["request_param"])
		}
		sample, ok := requestParam["sample"].(map[string]any)
		if !ok {
			t.Fatalf("node %d request_param.sample type mismatch: %T", i, requestParam["sample"])
		}
		if sample["files"] != wantFiles[i] {
			t.Fatalf("node %d request_param.sample.files = %v, want %v", i, sample["files"], wantFiles[i])
		}

		resolvedInputs, ok := node["resolved_inputs"].(map[string]any)
		if !ok {
			t.Fatalf("node %d resolved_inputs type mismatch: %T", i, node["resolved_inputs"])
		}
		if _, exists := resolvedInputs["sample"]; exists {
			t.Fatalf("node %d must not carry the raw scatter value in resolved_inputs: %#v", i, resolvedInputs["sample"])
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
