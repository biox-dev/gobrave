package types

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestAnalysisNodePathMethods pins the single source of a node's derived paths:
// WorkspaceDir. Every path is computed on demand, so a node built by any caller
// (and never "hydrated") still reports the same layout.
func TestAnalysisNodePathMethods(t *testing.T) {
	ws := filepath.Join(t.TempDir(), "node")
	node := &AnalysisNode{WorkspaceDir: ws}

	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{name: "output", got: node.OutputDirectory(), want: filepath.Join(ws, "output")},
		{name: "cache", got: node.CacheDirectory(), want: filepath.Join(ws, "cached")},
		{name: "params", got: node.ParamsFile(), want: filepath.Join(ws, "params.json")},
		{name: "command", got: node.CommandFile(), want: filepath.Join(ws, "run.sh")},
		{name: "log", got: node.LogFile(), want: filepath.Join(ws, "command.log")},
		{name: "outputs", got: node.OutputsFile(), want: filepath.Join(ws, "output", AnalysisNodeOutputsFileName)},
		{name: "output_markdown", got: node.OutputMarkdownFile(), want: filepath.Join(ws, "output", AnalysisNodeOutputMarkdownFileName)},
	} {
		if tc.got != tc.want {
			t.Fatalf("%s = %q, want %q", tc.name, tc.got, tc.want)
		}
	}

	// WorkspaceDir 为空时不产生悬空路径。
	empty := &AnalysisNode{}
	for name, got := range map[string]string{
		"output":          empty.OutputDirectory(),
		"cache":           empty.CacheDirectory(),
		"params":          empty.ParamsFile(),
		"command":         empty.CommandFile(),
		"log":             empty.LogFile(),
		"outputs":         empty.OutputsFile(),
		"output_markdown": empty.OutputMarkdownFile(),
	} {
		if got != "" {
			t.Fatalf("%s of a workspace-less node = %q, want empty", name, got)
		}
	}

	var nilNode *AnalysisNode
	if got := nilNode.OutputsFile(); got != "" {
		t.Fatalf("nil node outputs = %q, want empty", got)
	}
	if got := nilNode.OutputMarkdownFile(); got != "" {
		t.Fatalf("nil node output markdown = %q, want empty", got)
	}
}

// TestAnalysisNodeJSONKeepsDerivedKeys pins the API contract: the derived paths
// are no longer struct fields, but they must still be serialized under the same
// JSON keys the frontend reads.
func TestAnalysisNodeJSONKeepsDerivedKeys(t *testing.T) {
	ws := filepath.Join("/data", "34", "analysis_node", "100", "200")
	raw, err := json.Marshal(&AnalysisNode{ID: 1, NodeID: "align", WorkspaceDir: ws})
	if err != nil {
		t.Fatalf("marshal node: %v", err)
	}

	payload := map[string]any{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal node json: %v", err)
	}

	for key, want := range map[string]string{
		"log_path":     filepath.Join(ws, "command.log"),
		"output_dir":   filepath.Join(ws, "output"),
		"cache_dir":    filepath.Join(ws, "cached"),
		"command_path": filepath.Join(ws, "run.sh"),
		"params_path":  filepath.Join(ws, "params.json"),
	} {
		got, ok := payload[key]
		if !ok {
			t.Fatalf("node json is missing %q: %s", key, string(raw))
		}
		if got != want {
			t.Fatalf("node json %q = %v, want %q", key, got, want)
		}
	}

	// 落库字段照常输出。
	if payload["node_id"] != "align" {
		t.Fatalf("node json node_id = %v, want align", payload["node_id"])
	}
	if payload["workspace_dir"] != ws {
		t.Fatalf("node json workspace_dir = %v, want %q", payload["workspace_dir"], ws)
	}

	// nil 指针仍是 null（MarshalJSON 不应改变这一点）。
	nullRaw, err := json.Marshal((*AnalysisNode)(nil))
	if err != nil {
		t.Fatalf("marshal nil node: %v", err)
	}
	if string(nullRaw) != "null" {
		t.Fatalf("nil node json = %s, want null", string(nullRaw))
	}
}

// TestAnalysisWorkflowJSONKeepsDerivedKeys is the workflow counterpart of the
// node contract: the same JSON keys, all derived from WorkspaceDir.
func TestAnalysisWorkflowJSONKeepsDerivedKeys(t *testing.T) {
	ws := filepath.Join("/data", "34", "analysis", "run")
	raw, err := json.Marshal(&AnalysisWorkflow{ID: 1, AnalysisID: "a-1", WorkspaceDir: ws})
	if err != nil {
		t.Fatalf("marshal analysis: %v", err)
	}

	payload := map[string]any{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal analysis json: %v", err)
	}

	for key, want := range map[string]string{
		"params_path":       filepath.Join(ws, "params.json"),
		"command_path":      filepath.Join(ws, "run.sh"),
		"command_log_path":  filepath.Join(ws, "run.log"),
		"trace_file":        filepath.Join(ws, "trace.log"),
		"workflow_log_file": filepath.Join(ws, "workflow.log"),
		"executor_log_file": filepath.Join(ws, ".nextflow.log"),
	} {
		got, ok := payload[key]
		if !ok {
			t.Fatalf("analysis json is missing %q: %s", key, string(raw))
		}
		if got != want {
			t.Fatalf("analysis json %q = %v, want %q", key, got, want)
		}
	}

	if payload["analysis_id"] != "a-1" {
		t.Fatalf("analysis json analysis_id = %v, want a-1", payload["analysis_id"])
	}

	// WorkspaceDir 为空时派生路径为空串而不是悬空文件名。
	workspaceLess, err := json.Marshal(&AnalysisWorkflow{ID: 2})
	if err != nil {
		t.Fatalf("marshal workspace-less analysis: %v", err)
	}
	empty := map[string]any{}
	if err := json.Unmarshal(workspaceLess, &empty); err != nil {
		t.Fatalf("unmarshal workspace-less analysis json: %v", err)
	}
	if empty["params_path"] != "" || empty["trace_file"] != "" {
		t.Fatalf("workspace-less analysis must serialize empty paths, got %v", empty)
	}
}
