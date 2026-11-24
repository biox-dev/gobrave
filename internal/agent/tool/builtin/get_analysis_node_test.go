package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/types"
)

// TestParseAnalysisNodeID 覆盖分析节点 ID 的解析与引用前缀容错。
//
// 界面「Ask Agent」插入的引用 chip 文本是 "@analysis_node:<id>"，
// 模型可能原样传进来，因此解析必须剥离前缀后再转整数。
func TestParseAnalysisNodeID(t *testing.T) {
	if id, err := parseAnalysisNodeID(" 123 "); err != nil || id != 123 {
		t.Fatalf("parseAnalysisNodeID(123) = (%d, %v), want (123, nil)", id, err)
	}
	if id, err := parseAnalysisNodeID("@analysis_node:2109711590457610240"); err != nil || id != 2109711590457610240 {
		t.Fatalf("parseAnalysisNodeID(@analysis_node:...) = (%d, %v), want (2109711590457610240, nil)", id, err)
	}
	for _, raw := range []string{"", "abc", "0", "-1", "@analysis_node:"} {
		if _, err := parseAnalysisNodeID(raw); err == nil {
			t.Fatalf("parseAnalysisNodeID(%q) expected error", raw)
		}
	}
}

// TestGetAnalysisNodeReadsOutputBundle 覆盖 files=["output"]：作为 files 的一种资源类型，
// 由 agent 传参选择读取节点产物正文（浏览器侧封装 output.md + 已登记产物）。
func TestGetAnalysisNodeReadsOutputBundle(t *testing.T) {
	browser := &fakeAnalysisNodeBrowser{files: []runtimecontext.AnalysisNodeFile{
		{FileID: 7, FileName: "result.csv", Path: "/ws/output/result.csv"},
	}}
	ctx := tool.WithTaskFacts(context.Background(), types.TaskFacts{ProjectID: 8})

	raw, err := GetAnalysisNode(browser).Execute(ctx, json.RawMessage(`{"analysis_node_id":"101","files":["output"]}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, ok := raw.(GetAnalysisNodeOutput)
	if !ok {
		t.Fatalf("unexpected output type %T", raw)
	}
	if out.Error != "" {
		t.Fatalf("unexpected error: %s", out.Error)
	}
	content, ok := out.Files[string(runtimecontext.ResourceFileOutput)]
	if !ok {
		t.Fatalf("files = %#v, want key %q", out.Files, runtimecontext.ResourceFileOutput)
	}
	if content.File != runtimecontext.ResourceFileOutput {
		t.Fatalf("files[output].file = %q, want %q", content.File, runtimecontext.ResourceFileOutput)
	}
}

// TestGetAnalysisNodeReturnsOutputFiles 覆盖节点详情直接回传系统登记的产物文件记录
// （原 list_analysis_node_files 的能力已合并进 get_analysis_node）。
func TestGetAnalysisNodeReturnsOutputFiles(t *testing.T) {
	browser := &fakeAnalysisNodeBrowser{files: []runtimecontext.AnalysisNodeFile{
		{FileID: 42, FileName: "result.csv", Path: "/ws/output/result.csv", Format: "csv", Role: "DEFAULT", FileKey: "result_csv"},
	}}
	ctx := tool.WithTaskFacts(context.Background(), types.TaskFacts{ProjectID: 8})

	raw, err := GetAnalysisNode(browser).Execute(ctx, json.RawMessage(`{"analysis_node_id":"@analysis_node:101"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, ok := raw.(GetAnalysisNodeOutput)
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

// TestGetAnalysisNodeOutputFilesEmpty 覆盖没有登记产物时返回空数组与提示，而不是 null。
func TestGetAnalysisNodeOutputFilesEmpty(t *testing.T) {
	ctx := tool.WithTaskFacts(context.Background(), types.TaskFacts{ProjectID: 8})

	raw, err := GetAnalysisNode(&fakeAnalysisNodeBrowser{}).Execute(ctx, json.RawMessage(`{"analysis_node_id":"101"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, ok := raw.(GetAnalysisNodeOutput)
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

// TestGetAnalysisNodeRequiresNodeID 覆盖必填入参校验（缺失时直接报错）。
func TestGetAnalysisNodeRequiresNodeID(t *testing.T) {
	ctx := tool.WithTaskFacts(context.Background(), types.TaskFacts{ProjectID: 8})

	if _, err := GetAnalysisNode(&fakeAnalysisNodeBrowser{}).Execute(ctx, json.RawMessage(`{}`)); err == nil {
		t.Fatalf("expected error when analysis_node_id is missing")
	}
}
