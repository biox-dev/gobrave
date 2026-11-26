package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// fakeFileDatasetBrowser 记录工具调用并回放固定结果。
type fakeFileDatasetBrowser struct {
	listedRoles []string
	listedFiles []runtimecontext.ProjectFileBrief
	datasets    []runtimecontext.DatasetBrief
}

func (f *fakeFileDatasetBrowser) GetDatasetByID(_ context.Context, _ int64, datasetID int64) (runtimecontext.DatasetBrief, error) {
	return runtimecontext.DatasetBrief{DatasetID: datasetID}, nil
}

func (f *fakeFileDatasetBrowser) ListDatasetsByProject(_ context.Context, _ int64) ([]runtimecontext.DatasetBrief, error) {
	return f.datasets, nil
}

func (f *fakeFileDatasetBrowser) ListFilesByProject(_ context.Context, _ int64, roles []string) ([]runtimecontext.ProjectFileBrief, error) {
	f.listedRoles = roles
	return f.listedFiles, nil
}

// fakeDatasetListWriter 记录 create / update 调用并回放固定结果。
type fakeDatasetListWriter struct {
	createInput runtimecontext.DatasetCreateInput
	updateInput runtimecontext.DatasetUpdateInput
	calls       int
}

func (f *fakeDatasetListWriter) CreateDataset(_ context.Context, in runtimecontext.DatasetCreateInput) (runtimecontext.DatasetBrief, error) {
	f.calls++
	f.createInput = in
	return runtimecontext.DatasetBrief{DatasetID: 55, DatasetName: in.DatasetName, Path: "/data/7/dataset/55"}, nil
}

func (f *fakeDatasetListWriter) UpdateDataset(_ context.Context, in runtimecontext.DatasetUpdateInput) (runtimecontext.DatasetBrief, error) {
	f.calls++
	f.updateInput = in
	return runtimecontext.DatasetBrief{DatasetID: in.DatasetID, DatasetName: in.DatasetName, Path: "/data/7/dataset/55"}, nil
}

// fakeFileUpdater 记录 update_file 调用并回放固定结果。
type fakeFileUpdater struct {
	input runtimecontext.FileUpdateInput
	calls int
}

func (f *fakeFileUpdater) UpdateFile(_ context.Context, in runtimecontext.FileUpdateInput) (runtimecontext.FileUpdateResult, error) {
	f.calls++
	f.input = in
	return runtimecontext.FileUpdateResult{
		FileID:  in.FileID,
		FileKey: in.FileKey,
		Role:    in.Role,
		Changed: []string{"file_key", "role"},
	}, nil
}

func testProjectCtx() context.Context {
	return tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{ProjectID: 7})
}

func TestListFileTool(t *testing.T) {
	browser := &fakeFileDatasetBrowser{
		listedFiles: []runtimecontext.ProjectFileBrief{{FileID: 99, Path: "/data/x.bam", Role: "BAM"}},
	}
	exec := tool.NewExecutor(tool.NewRegistryWith(ListFile(browser)))

	res := exec.Execute(testProjectCtx(), tool.Call{
		ID:        "c1",
		Name:      ListFileToolName,
		Arguments: json.RawMessage(`{"roles":["BAM"]}`),
	})
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	if len(browser.listedRoles) != 1 || browser.listedRoles[0] != "BAM" {
		t.Fatalf("unexpected roles: %v", browser.listedRoles)
	}
	var out ListFileOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Total != 1 || out.Files[0].FileID != 99 {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestListDatasetTool(t *testing.T) {
	browser := &fakeFileDatasetBrowser{
		datasets: []runtimecontext.DatasetBrief{{DatasetID: 1, DatasetName: "d1", Path: "/data/7/dataset/1"}},
	}
	exec := tool.NewExecutor(tool.NewRegistryWith(ListDataset(browser)))

	res := exec.Execute(testProjectCtx(), tool.Call{ID: "c1", Name: ListDatasetToolName, Arguments: json.RawMessage(`{}`)})
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	var out ListDatasetOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Total != 1 || out.Datasets[0].DatasetID != 1 {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestCreateDatasetTool(t *testing.T) {
	writer := &fakeDatasetListWriter{}
	exec := tool.NewExecutor(tool.NewRegistryWith(CreateDataset(writer)))

	res := exec.Execute(testProjectCtx(), tool.Call{
		ID:        "c1",
		Name:      CreateDatasetToolName,
		Arguments: json.RawMessage(`{"dataset_name":"new","description":"d"}`),
	})
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	if writer.calls != 1 || writer.createInput.ProjectID != 7 || writer.createInput.DatasetName != "new" {
		t.Fatalf("unexpected input: %+v", writer.createInput)
	}
	var out CreateDatasetOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.DatasetID != 55 || out.Path != "/data/7/dataset/55" {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestUpdateDatasetTool(t *testing.T) {
	writer := &fakeDatasetListWriter{}
	exec := tool.NewExecutor(tool.NewRegistryWith(UpdateDataset(writer)))

	res := exec.Execute(testProjectCtx(), tool.Call{
		ID:        "c1",
		Name:      UpdateDatasetToolName,
		Arguments: json.RawMessage(`{"dataset_id":"@dataset:55","dataset_name":"renamed"}`),
	})
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	if writer.calls != 1 || writer.updateInput.DatasetID != 55 || writer.updateInput.DatasetName != "renamed" {
		t.Fatalf("unexpected input: %+v", writer.updateInput)
	}
}

func TestUpdateDatasetToolRequiresField(t *testing.T) {
	writer := &fakeDatasetListWriter{}
	exec := tool.NewExecutor(tool.NewRegistryWith(UpdateDataset(writer)))

	res := exec.Execute(testProjectCtx(), tool.Call{
		ID:        "c1",
		Name:      UpdateDatasetToolName,
		Arguments: json.RawMessage(`{"dataset_id":"55"}`),
	})
	if !res.IsError {
		t.Fatalf("expected validation error, got %q", res.Content)
	}
	if writer.calls != 0 {
		t.Fatalf("writer should not be called, got %d", writer.calls)
	}
}

func TestUpdateFileTool(t *testing.T) {
	updater := &fakeFileUpdater{}
	exec := tool.NewExecutor(tool.NewRegistryWith(UpdateFile(updater)))

	res := exec.Execute(testProjectCtx(), tool.Call{
		ID:        "c1",
		Name:      UpdateFileToolName,
		Arguments: json.RawMessage(`{"file_id":"@file:99","role":"VCF","file_key":"VCF_1"}`),
	})
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}
	if updater.calls != 1 || updater.input.ProjectID != 7 || updater.input.FileID != 99 || updater.input.Role != "VCF" {
		t.Fatalf("unexpected input: %+v", updater.input)
	}
	var out UpdateFileOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.FileID != 99 || len(out.Changed) != 2 {
		t.Fatalf("unexpected output: %+v", out)
	}
}
