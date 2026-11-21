package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/types"
)

// fakeAnalysisBrowser 实现 AnalysisBrowser 的只读方法，供 list_project_analysis_workflow 测试使用。
type fakeAnalysisBrowser struct {
	analyses  []runtimecontext.AnalysisBrief
	filters   runtimecontext.AnalysisListFilters
	projectID int64
	called    bool
}

func (f *fakeAnalysisBrowser) ListAnalysesByProject(_ context.Context, projectID int64, filters runtimecontext.AnalysisListFilters) ([]runtimecontext.AnalysisBrief, error) {
	f.called = true
	f.projectID = projectID
	f.filters = filters
	return f.analyses, nil
}

func (f *fakeAnalysisBrowser) GetAnalysisByID(_ context.Context, _ int64, analysisID int64) (runtimecontext.AnalysisDetail, error) {
	return runtimecontext.AnalysisDetail{AnalysisBrief: runtimecontext.AnalysisBrief{AnalysisID: analysisID}}, nil
}

func (f *fakeAnalysisBrowser) ReadAnalysisFile(_ context.Context, _ int64, _ int64, file runtimecontext.ResourceFile) (runtimecontext.ResourceFileContent, error) {
	return runtimecontext.ResourceFileContent{File: file}, nil
}

// fakeAnalysisNodeBrowser 实现 AnalysisNodeBrowser 的只读方法，供 list_project_analysis_node 测试使用。
type fakeAnalysisNodeBrowser struct {
	nodes   []runtimecontext.AnalysisNodeSummary
	filters runtimecontext.AnalysisNodeListFilters
	called  bool
}

func (f *fakeAnalysisNodeBrowser) ListAnalysisNodesByProject(_ context.Context, _ int64, filters runtimecontext.AnalysisNodeListFilters) ([]runtimecontext.AnalysisNodeSummary, error) {
	f.called = true
	f.filters = filters
	return f.nodes, nil
}

func (f *fakeAnalysisNodeBrowser) GetAnalysisNodeByID(_ context.Context, _ int64, nodeID int64) (runtimecontext.AnalysisNodeBrief, error) {
	return runtimecontext.AnalysisNodeBrief{AnalysisNodeID: nodeID}, nil
}

func (f *fakeAnalysisNodeBrowser) ReadAnalysisNodeFile(_ context.Context, _ int64, _ int64, file runtimecontext.ResourceFile) (runtimecontext.ResourceFileContent, error) {
	return runtimecontext.ResourceFileContent{File: file}, nil
}

// TestOptionalPositiveID 覆盖可选 ID 的解析（空值 / 引用前缀 / 非法值）。
func TestOptionalPositiveID(t *testing.T) {
	if id, err := optionalPositiveID("  ", "tool", "id"); err != nil || id != "" {
		t.Fatalf("empty = (%q, %v), want (\"\", nil)", id, err)
	}
	if id, err := optionalPositiveID("@analysis:42", "tool", "id"); err != nil || id != "42" {
		t.Fatalf("reference = (%q, %v), want (\"42\", nil)", id, err)
	}
	if id, err := optionalPositiveID("2121123876845916160", "tool", "id"); err != nil || id != "2121123876845916160" {
		t.Fatalf("big id = (%q, %v)", id, err)
	}
	for _, raw := range []string{"abc", "0", "-3"} {
		if _, err := optionalPositiveID(raw, "tool", "id"); err == nil {
			t.Fatalf("optionalPositiveID(%q) expected error", raw)
		}
	}
}

// TestNormalizeListLimit 覆盖条数上限的默认值与截断。
func TestNormalizeListLimit(t *testing.T) {
	if got, err := normalizeListLimit(0, "tool"); err != nil || got != defaultListLimit {
		t.Fatalf("0 = (%d, %v), want (%d, nil)", got, err, defaultListLimit)
	}
	if got, err := normalizeListLimit(5000, "tool"); err != nil || got != maxListLimit {
		t.Fatalf("5000 = (%d, %v), want (%d, nil)", got, err, maxListLimit)
	}
	if got, err := normalizeListLimit(7, "tool"); err != nil || got != 7 {
		t.Fatalf("7 = (%d, %v), want (7, nil)", got, err)
	}
	if _, err := normalizeListLimit(-1, "tool"); err == nil {
		t.Fatal("negative limit expected error")
	}
}

// TestListProjectAnalysesWorkflowTool 覆盖项目事实注入、过滤条件透传与截断提示。
func TestListProjectAnalysesWorkflowTool(t *testing.T) {
	browser := &fakeAnalysisBrowser{analyses: []runtimecontext.AnalysisBrief{
		{AnalysisID: 11, AnalysisKey: "run-key", AnalysisName: "rnaseq-1", JobStatus: "finished", NodeCount: 3},
	}}
	ctx := tool.WithTaskFacts(context.Background(), types.TaskFacts{ProjectID: 8})

	raw, err := ListProjectAnalysesWorkflow(browser).Execute(ctx, json.RawMessage(`{"keyword":"rnaseq","workflow_id":"21","job_status":"finished","cache_type":2,"limit":3}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, ok := raw.(ListProjectAnalysesWorkflowOutput)
	if !ok {
		t.Fatalf("unexpected output type %T", raw)
	}
	if out.Error != "" {
		t.Fatalf("unexpected error: %s", out.Error)
	}
	if out.ProjectID != 8 || out.Total != 1 {
		t.Fatalf("project/total = (%d, %d), want (8, 1)", out.ProjectID, out.Total)
	}
	if out.Analyses[0].AnalysisID != 11 || out.Analyses[0].JobStatus != "finished" {
		t.Fatalf("first analysis = %#v", out.Analyses[0])
	}

	// 过滤条件应按工具层归一化后透传给浏览器（ID 统一为字符串）。
	if !browser.called || browser.projectID != 8 {
		t.Fatalf("browser called=%v projectID=%d", browser.called, browser.projectID)
	}
	if browser.filters.Keyword != "rnaseq" || browser.filters.WorkflowID != "21" ||
		browser.filters.JobStatus != "finished" || browser.filters.CacheType != 2 || browser.filters.Limit != 3 {
		t.Fatalf("filters = %#v", browser.filters)
	}

	// 空列表：给出可操作提示（create_analysis_workflow）。
	emptyRaw, err := ListProjectAnalysesWorkflow(&fakeAnalysisBrowser{}).Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute empty: %v", err)
	}
	emptyOut := emptyRaw.(ListProjectAnalysesWorkflowOutput)
	if emptyOut.Total != 0 || emptyOut.Message == "" || emptyOut.Analyses == nil {
		t.Fatalf("empty list should carry a message and empty slice, got %#v", emptyOut)
	}

	// 缺少项目事实：直接报错，不调用浏览器。
	if _, err := ListProjectAnalysesWorkflow(browser).Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing project id expected error")
	}

	// 非法 ID：报错。
	if _, err := ListProjectAnalysesWorkflow(browser).Execute(ctx, json.RawMessage(`{"workflow_id":"abc"}`)); err == nil {
		t.Fatal("invalid workflow_id expected error")
	}
}

// TestListProjectAnalysisNodesTool 覆盖项目事实注入、过滤条件透传与截断提示。
func TestListProjectAnalysisNodesTool(t *testing.T) {
	browser := &fakeAnalysisNodeBrowser{nodes: []runtimecontext.AnalysisNodeSummary{
		{
			AnalysisNodeBrief: runtimecontext.AnalysisNodeBrief{
				AnalysisNodeID: 31,
				NodeName:       "fastqc",
				NodeType:       "script",
				ScriptID:       7,
				WorkspaceDir:   "/data/node",
			},
			AnalysisNodeKey: "node-key",
			ScriptName:      "fastqc",
			AnalysisRunID:   11,
			AnalysisName:    "rnaseq-1",
			JobStatus:       "finished",
			Status:          "success",
		},
	}}
	ctx := tool.WithTaskFacts(context.Background(), types.TaskFacts{ProjectID: 8})

	raw, err := ListProjectAnalysisNodes(browser).Execute(ctx, json.RawMessage(`{"keyword":"fastqc","analysis_id":"11","script_id":"7","status":"success","limit":5}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, ok := raw.(ListProjectAnalysisNodesOutput)
	if !ok {
		t.Fatalf("unexpected output type %T", raw)
	}
	if out.Error != "" {
		t.Fatalf("unexpected error: %s", out.Error)
	}
	if out.ProjectID != 8 || out.Total != 1 {
		t.Fatalf("project/total = (%d, %d), want (8, 1)", out.ProjectID, out.Total)
	}
	if out.Nodes[0].AnalysisNodeID != 31 || out.Nodes[0].AnalysisRunID != 11 {
		t.Fatalf("first node = %#v", out.Nodes[0])
	}

	if !browser.called {
		t.Fatal("browser should be called")
	}
	if browser.filters.Keyword != "fastqc" || browser.filters.AnalysisID != "11" ||
		browser.filters.ScriptID != "7" || browser.filters.Status != "success" || browser.filters.Limit != 5 {
		t.Fatalf("filters = %#v", browser.filters)
	}

	// 空列表：给出可操作提示（create_analysis_node_*）。
	emptyRaw, err := ListProjectAnalysisNodes(&fakeAnalysisNodeBrowser{}).Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute empty: %v", err)
	}
	emptyOut := emptyRaw.(ListProjectAnalysisNodesOutput)
	if emptyOut.Total != 0 || emptyOut.Message == "" || emptyOut.Nodes == nil {
		t.Fatalf("empty list should carry a message and empty slice, got %#v", emptyOut)
	}

	// 缺少项目事实：直接报错。
	if _, err := ListProjectAnalysisNodes(browser).Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("missing project id expected error")
	}

	// 非法 ID：报错。
	if _, err := ListProjectAnalysisNodes(browser).Execute(ctx, json.RawMessage(`{"script_id":"abc"}`)); err == nil {
		t.Fatal("invalid script_id expected error")
	}
}
