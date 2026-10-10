package compiler

import "testing"

// TestBuildRuntimeTasks_RequestParamFilteredByInputNames 验证节点 request_param 的
// 填充口径与 inputs_patterns 一致：只保留 analysis 请求里键与节点输入 name 相同的项，
// 其余（如 analysis_name、未声明为输入的字段）一律丢弃。
func TestBuildRuntimeTasks_RequestParamFilteredByInputNames(t *testing.T) {
	requestParam := map[string]any{
		"fastq1":        "/data/s1_1.fastq.gz",
		"reference":     "/data/ref.fa",
		"analysis_name": "abc",
		"threads":       4,
	}
	params := map[string]any{
		"fastq1":    "/data/s1_1.fastq.gz",
		"reference": "/data/ref.fa",
	}

	runtimeDAG, err := BuildRuntimeTasks(1, params, requestParam, map[string]any{
		"nodes": []any{
			map[string]any{
				"id":   "node-align",
				"name": "align",
				"inputs": map[string]any{
					"fastq1":    map[string]any{"type": "file", "required": true},
					"reference": map[string]any{"type": "file", "required": true},
				},
				"outputs": map[string]any{
					"bam": map[string]any{"type": "file", "pattern": "{sample}.bam"},
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
	if len(nodes) != 1 {
		t.Fatalf("expected 1 analysis node, got %d", len(nodes))
	}

	got, ok := nodes[0]["request_param"].(map[string]any)
	if !ok {
		t.Fatalf("request_param type mismatch: %T", nodes[0]["request_param"])
	}
	if len(got) != 2 {
		t.Fatalf("expected request_param to keep only input-named keys, got %#v", got)
	}
	if got["fastq1"] != requestParam["fastq1"] || got["reference"] != requestParam["reference"] {
		t.Fatalf("request_param values mismatch: got %#v", got)
	}
	if _, exists := got["analysis_name"]; exists {
		t.Fatalf("request_param must not carry non-input key analysis_name: %#v", got)
	}
	if _, exists := got["threads"]; exists {
		t.Fatalf("request_param must not carry non-input key threads: %#v", got)
	}
}
