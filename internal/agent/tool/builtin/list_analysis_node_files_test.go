package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/types"
)

// TestListAnalysisNodeFiles 覆盖独立工具按 analysis_node_id 回传系统登记的产物文件。
//
// 该工具从 get_analysis_node 拆出：入参只有 analysis_node_id（兼容 @analysis_node:<id>
// 引用文本），浏览器返回的 go_file 登记记录应原样透传到 output_files。
func TestListAnalysisNodeFiles(t *testing.T) {
	browser := &fakeAnalysisNodeBrowser{files: []runtimecontext.AnalysisNodeFile{
		{FileID: 42, FileName: "result.csv", Path: "/ws/output/result.csv", Format: "csv", Role: "DEFAULT", FileKey: "result_csv"},
	}}
	ctx := tool.WithTaskFacts(context.Background(), types.TaskFacts{ProjectID: 8})

	raw, err := ListAnalysisNodeFiles(browser).Execute(ctx, json.RawMessage(`{"analysis_node_id":"@analysis_node:101"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, ok := raw.(ListAnalysisNodeFilesOutput)
	if !ok {
		t.Fatalf("unexpected output type %T", raw)
	}
	if out.Error != "" {
		t.Fatalf("unexpected error: %s", out.Error)
	}
	if out.AnalysisNodeID != 101 {
		t.Fatalf("analysis_node_id = %d, want 101", out.AnalysisNodeID)
	}
	if out.Total != 1 || len(out.OutputFiles) != 1 {
		t.Fatalf("total/output_files = %d/%#v, want 1 record", out.Total, out.OutputFiles)
	}
	if out.OutputFiles[0].FileID != 42 || out.OutputFiles[0].FileKey != "result_csv" {
		t.Fatalf("output_files = %#v", out.OutputFiles)
	}
}

// TestListAnalysisNodeFilesEmpty 覆盖没有登记产物时返回空数组与提示，而不是 null。
func TestListAnalysisNodeFilesEmpty(t *testing.T) {
	ctx := tool.WithTaskFacts(context.Background(), types.TaskFacts{ProjectID: 8})

	raw, err := ListAnalysisNodeFiles(&fakeAnalysisNodeBrowser{}).Execute(ctx, json.RawMessage(`{"analysis_node_id":"101"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, ok := raw.(ListAnalysisNodeFilesOutput)
	if !ok {
		t.Fatalf("unexpected output type %T", raw)
	}
	if out.Error != "" {
		t.Fatalf("unexpected error: %s", out.Error)
	}
	if out.Total != 0 || len(out.OutputFiles) != 0 {
		t.Fatalf("total/output_files = %d/%#v, want empty", out.Total, out.OutputFiles)
	}
	if out.OutputFiles == nil {
		t.Fatalf("output_files = nil, want empty slice")
	}
	if out.Message == "" {
		t.Fatalf("message is empty, want hint for empty output_files")
	}
}

// TestListAnalysisNodeFilesRequiresNodeID 覆盖必填入参校验（缺失时直接报错）。
func TestListAnalysisNodeFilesRequiresNodeID(t *testing.T) {
	ctx := tool.WithTaskFacts(context.Background(), types.TaskFacts{ProjectID: 8})

	if _, err := ListAnalysisNodeFiles(&fakeAnalysisNodeBrowser{}).Execute(ctx, json.RawMessage(`{}`)); err == nil {
		t.Fatalf("expected error when analysis_node_id is missing")
	}
}
