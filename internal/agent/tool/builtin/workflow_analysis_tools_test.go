package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/agent/types"
)

// TestParseWorkflowAndAnalysisID 覆盖工作流 ID / 分析运行 ID 的解析与引用前缀容错。
func TestParseWorkflowAndAnalysisID(t *testing.T) {
	if id, err := parseWorkflowID(" 42 "); err != nil || id != 42 {
		t.Fatalf("parseWorkflowID(42) = (%d, %v), want (42, nil)", id, err)
	}
	if id, err := parseWorkflowID("@workflow:2109711590457610240"); err != nil || id != 2109711590457610240 {
		t.Fatalf("parseWorkflowID(@workflow:...) = (%d, %v)", id, err)
	}
	for _, raw := range []string{"", "abc", "0", "-1", "@workflow:"} {
		if _, err := parseWorkflowID(raw); err == nil {
			t.Fatalf("parseWorkflowID(%q) expected error", raw)
		}
	}

	if id, err := parseAnalysisID("@analysis:123"); err != nil || id != 123 {
		t.Fatalf("parseAnalysisID(@analysis:123) = (%d, %v)", id, err)
	}
	// @analysis_node:<id> 是节点引用，不能当作分析运行 ID 解析成功。
	if _, err := parseAnalysisID("@analysis_node:123"); err == nil {
		t.Fatal("parseAnalysisID(@analysis_node:123) expected error")
	}
	for _, raw := range []string{"", "abc", "0"} {
		if _, err := parseAnalysisID(raw); err == nil {
			t.Fatalf("parseAnalysisID(%q) expected error", raw)
		}
	}
}

// TestJSONTextUnmarshal 覆盖「JSON 对象 / JSON 字符串都接受」的入参解码。
func TestJSONTextUnmarshal(t *testing.T) {
	var in struct {
		Dag JSONText `json:"dag_definition"`
	}
	if err := json.Unmarshal([]byte(`{"dag_definition":{"nodes":[],"edges":[]}}`), &in); err != nil {
		t.Fatalf("unmarshal object: %v", err)
	}
	if got := in.Dag.value(); !strings.Contains(got, `"nodes"`) {
		t.Fatalf("object form: got %q", got)
	}

	var asString struct {
		Dag JSONText `json:"dag_definition"`
	}
	if err := json.Unmarshal([]byte(`{"dag_definition":"{\"nodes\":[]}"}`), &asString); err != nil {
		t.Fatalf("unmarshal string: %v", err)
	}
	if got := asString.Dag.value(); got != `{"nodes":[]}` {
		t.Fatalf("string form: got %q", got)
	}

	var missing struct {
		Dag *JSONText `json:"dag_definition"`
	}
	if err := json.Unmarshal([]byte(`{}`), &missing); err != nil {
		t.Fatalf("unmarshal missing: %v", err)
	}
	if missing.Dag != nil {
		t.Fatalf("missing key should stay nil, got %v", missing.Dag)
	}
}

// fakeWorkflowCreator 记录入参并回显，用于验证工具层的入参归一化。
type fakeWorkflowCreator struct {
	input runtimecontext.WorkflowCreateInput
}

func (f *fakeWorkflowCreator) CreateWorkflow(_ context.Context, in runtimecontext.WorkflowCreateInput) (runtimecontext.WorkflowCreateResult, error) {
	f.input = in
	return runtimecontext.WorkflowCreateResult{
		WorkflowBrief: runtimecontext.WorkflowBrief{WorkflowID: 7, WorkflowKey: "wf-key", Name: in.Name},
	}, nil
}

func TestCreateWorkflowToolNormalizesInput(t *testing.T) {
	fake := &fakeWorkflowCreator{}
	tl := CreateWorkflow(fake)
	ctx := tool.WithTaskFacts(context.Background(), types.TaskFacts{ProjectID: 9})

	args := json.RawMessage(`{
		"name": "demo pipeline",
		"description": "desc",
		"dag_definition": {"nodes": [{"id": "n1", "name": "bwa", "script_id": "s1"}], "edges": []},
		"io_schema": "{\"inputs\":[{\"name\":\"fastq1\"}]}"
	}`)
	raw, err := tl.Execute(ctx, args)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, ok := raw.(CreateWorkflowOutput)
	if !ok {
		t.Fatalf("unexpected output type %T", raw)
	}
	if out.Error != "" {
		t.Fatalf("unexpected error: %s", out.Error)
	}
	if fake.input.ProjectID != 9 {
		t.Fatalf("project id = %d, want 9", fake.input.ProjectID)
	}
	if !strings.Contains(fake.input.DagDefinition, `"nodes"`) {
		t.Fatalf("dag_definition = %q", fake.input.DagDefinition)
	}
	if fake.input.IOSchema != `{"inputs":[{"name":"fastq1"}]}` {
		t.Fatalf("io_schema = %q", fake.input.IOSchema)
	}
	if out.WorkflowBrief.WorkflowID != 7 {
		t.Fatalf("workflow id = %d, want 7", out.WorkflowBrief.WorkflowID)
	}
}

// fakeAnalysisRunner 同时实现 AnalysisRunner 与 AnalysisUpdater，记录入参。
type fakeAnalysisRunner struct {
	runInput    runtimecontext.AnalysisRunInput
	updateInput runtimecontext.AnalysisUpdateInput
}

func (f *fakeAnalysisRunner) RunAnalysis(_ context.Context, in runtimecontext.AnalysisRunInput) (runtimecontext.AnalysisRunResult, error) {
	f.runInput = in
	return runtimecontext.AnalysisRunResult{AnalysisID: in.AnalysisID, Scheduler: "dag", Started: true}, nil
}

func (f *fakeAnalysisRunner) UpdateAnalysis(_ context.Context, in runtimecontext.AnalysisUpdateInput) (runtimecontext.AnalysisUpdateResult, error) {
	f.updateInput = in
	return runtimecontext.AnalysisUpdateResult{AnalysisBrief: runtimecontext.AnalysisBrief{AnalysisID: in.AnalysisID}}, nil
}

func TestUpdateAnalysisToolKeepsEmptyParamObject(t *testing.T) {
	fake := &fakeAnalysisRunner{}
	ctx := tool.WithTaskFacts(context.Background(), types.TaskFacts{ProjectID: 3})

	raw, err := UpdateAnalysis(fake, nil).Execute(ctx, json.RawMessage(`{"analysis_id":"11","request_param":{}}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, ok := raw.(UpdateAnalysisOutput)
	if !ok {
		t.Fatalf("unexpected output type %T", raw)
	}
	if out.Error != "" {
		t.Fatalf("unexpected error: %s", out.Error)
	}
	if fake.updateInput.RequestParam == nil {
		t.Fatal("empty object must be forwarded as non-nil map (clear parameters)")
	}
	if len(fake.updateInput.RequestParam) != 0 {
		t.Fatalf("request_param = %#v, want empty map", fake.updateInput.RequestParam)
	}
	if fake.updateInput.AnalysisID != 11 || fake.updateInput.ProjectID != 3 {
		t.Fatalf("ids = (%d, %d), want (11, 3)", fake.updateInput.AnalysisID, fake.updateInput.ProjectID)
	}
}

func TestRunAnalysisToolForwardsCacheType(t *testing.T) {
	fake := &fakeAnalysisRunner{}
	ctx := tool.WithTaskFacts(context.Background(), types.TaskFacts{ProjectID: 5})

	raw, err := RunAnalysis(fake, nil).Execute(ctx, json.RawMessage(`{"analysis_id":"12","cache_type":2}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, ok := raw.(RunAnalysisOutput)
	if !ok {
		t.Fatalf("unexpected output type %T", raw)
	}
	if out.Error != "" {
		t.Fatalf("unexpected error: %s", out.Error)
	}
	if fake.runInput.CacheType == nil || *fake.runInput.CacheType != 2 {
		t.Fatalf("cache_type = %v, want 2", fake.runInput.CacheType)
	}
	if fake.runInput.RequestParam != nil {
		t.Fatalf("request_param should stay nil, got %#v", fake.runInput.RequestParam)
	}
	if !out.Started || out.Scheduler != "dag" {
		t.Fatalf("started/scheduler = %v/%q", out.Started, out.Scheduler)
	}
}

// fakeWorkflowBrowser 实现 WorkflowBrowser 的两个只读方法，供列表工具测试使用。
type fakeWorkflowBrowser struct {
	workflows []runtimecontext.WorkflowBrief
}

func (f *fakeWorkflowBrowser) ListWorkflowsByProject(_ context.Context, _ int64) ([]runtimecontext.WorkflowBrief, error) {
	return f.workflows, nil
}

func (f *fakeWorkflowBrowser) GetWorkflowByID(_ context.Context, _ int64, workflowID int64) (runtimecontext.WorkflowDetail, error) {
	for _, workflow := range f.workflows {
		if workflow.WorkflowID == workflowID {
			return runtimecontext.WorkflowDetail{WorkflowBrief: workflow}, nil
		}
	}
	return runtimecontext.WorkflowDetail{}, nil
}

func TestListProjectWorkflowsTool(t *testing.T) {
	browser := &fakeWorkflowBrowser{workflows: []runtimecontext.WorkflowBrief{
		{WorkflowID: 21, WorkflowKey: "wf-key", Name: "rnaseq", NodeCount: 3, EdgeCount: 2},
	}}
	ctx := tool.WithTaskFacts(context.Background(), types.TaskFacts{ProjectID: 8})

	raw, err := ListProjectWorkflows(browser).Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	out, ok := raw.(ListProjectWorkflowsOutput)
	if !ok {
		t.Fatalf("unexpected output type %T", raw)
	}
	if out.Error != "" {
		t.Fatalf("unexpected error: %s", out.Error)
	}
	if out.ProjectID != 8 || out.Total != 1 {
		t.Fatalf("project/total = (%d, %d), want (8, 1)", out.ProjectID, out.Total)
	}
	if out.Workflows[0].WorkflowID != 21 || out.Workflows[0].NodeCount != 3 {
		t.Fatalf("first workflow = %#v", out.Workflows[0])
	}

	// 空列表：给出可操作提示（create_workflow），而不是空错误。
	emptyRaw, err := ListProjectWorkflows(&fakeWorkflowBrowser{}).Execute(ctx, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute empty: %v", err)
	}
	emptyOut := emptyRaw.(ListProjectWorkflowsOutput)
	if emptyOut.Total != 0 || emptyOut.Message == "" {
		t.Fatalf("empty list should carry a message, got %#v", emptyOut)
	}
	if emptyOut.Workflows == nil {
		t.Fatal("empty list should return an empty slice, not nil")
	}
}

func TestListProjectWorkflowsToolRequiresProject(t *testing.T) {
	if _, err := ListProjectWorkflows(&fakeWorkflowBrowser{}).Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected error when no active project in context")
	}
}

// TestBuiltinNamesIncludeWorkflowAndAnalysisTools 确认工作流 / 分析运行相关工具已注册进工具名清单
// （Profile 白名单与 /agent/tool 目录都来源于 Names()）。
func TestBuiltinNamesIncludeWorkflowAndAnalysisTools(t *testing.T) {
	names := Names()
	index := make(map[string]struct{}, len(names))
	for _, name := range names {
		index[name] = struct{}{}
	}
	for _, want := range []string{
		ListProjectWorkflowsToolName,
		CreateWorkflowToolName,
		GetWorkflowToolName,
		UpdateWorkflowToolName,
		GetAnalysisToolName,
		UpdateAnalysisToolName,
		RunAnalysisToolName,
	} {
		if _, ok := index[want]; !ok {
			t.Fatalf("tool %q not registered in Names()", want)
		}
	}
}
