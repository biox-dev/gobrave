package builtin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	"github.com/biox-dev/gobrave/internal/types"
)

// fakeDatasetWriter 记录工具调用并回放固定结果。
type fakeDatasetWriter struct {
	ensureKey   string
	addInput    runtimecontext.DatasetFileAddInput
	ensureCalls int
	addCalls    int
}

func (f *fakeDatasetWriter) EnsureDatasetByKey(_ context.Context, _ int64, datasetKey string) (runtimecontext.DatasetBrief, bool, error) {
	f.ensureCalls++
	f.ensureKey = datasetKey
	return runtimecontext.DatasetBrief{
		DatasetID:   42,
		DatasetName: types.DatasetKeyDatabases,
		Path:        "/data/7/dataset/42",
	}, true, nil
}

func (f *fakeDatasetWriter) AddFileToDataset(_ context.Context, in runtimecontext.DatasetFileAddInput) (runtimecontext.DatasetFileAddResult, error) {
	f.addCalls++
	f.addInput = in
	return runtimecontext.DatasetFileAddResult{
		DatasetID: in.DatasetID,
		FileID:    99,
		FilePath:  in.Path,
		FileName:  "sample.vcf",
		Role:      "VCF",
		Format:    "vcf",
		Size:      1024,
	}, nil
}

func TestGetProjectDatabasePathTool(t *testing.T) {
	writer := &fakeDatasetWriter{}
	exec := tool.NewExecutor(tool.NewRegistryWith(GetProjectDatabasePath(writer)))

	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{ProjectID: 7})
	res := exec.Execute(ctx, tool.Call{
		ID:        "c1",
		Name:      GetProjectDatabasePathToolName,
		Arguments: json.RawMessage(`{}`),
	})
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}

	var out GetProjectDatabasePathOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal result %q: %v", res.Content, err)
	}
	if writer.ensureCalls != 1 || writer.ensureKey != types.DatasetKeyDatabases {
		t.Fatalf("ensure = (%d, %q), want (1, %q)", writer.ensureCalls, writer.ensureKey, types.DatasetKeyDatabases)
	}
	if out.Path != "/data/7/dataset/42" || !out.Created || out.DatasetID != 42 {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestAddFileToDatasetTool(t *testing.T) {
	writer := &fakeDatasetWriter{}
	exec := tool.NewExecutor(tool.NewRegistryWith(AddFileToDataset(writer)))

	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{ProjectID: 7})
	res := exec.Execute(ctx, tool.Call{
		ID:        "c1",
		Name:      AddFileToDatasetToolName,
		Arguments: json.RawMessage(`{"dataset_id":"@dataset:42","path":"/data/7/analysis/x.vcf","role":"VCF"}`),
	})
	if res.IsError {
		t.Fatalf("unexpected error: %v", res.Content)
	}

	if writer.addCalls != 1 {
		t.Fatalf("add calls = %d, want 1", writer.addCalls)
	}
	if writer.addInput.ProjectID != 7 || writer.addInput.DatasetID != 42 {
		t.Fatalf("unexpected input: %+v", writer.addInput)
	}
	if writer.addInput.Path != "/data/7/analysis/x.vcf" || writer.addInput.Role != "VCF" {
		t.Fatalf("unexpected input: %+v", writer.addInput)
	}

	var out AddFileToDatasetOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal result %q: %v", res.Content, err)
	}
	if out.DatasetID != 42 || out.FileID != 99 || out.Role != "VCF" {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestAddFileToDatasetToolRequiresPath(t *testing.T) {
	writer := &fakeDatasetWriter{}
	exec := tool.NewExecutor(tool.NewRegistryWith(AddFileToDataset(writer)))

	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{ProjectID: 7})
	res := exec.Execute(ctx, tool.Call{
		ID:        "c1",
		Name:      AddFileToDatasetToolName,
		Arguments: json.RawMessage(`{"dataset_id":"42"}`),
	})
	if !res.IsError {
		t.Fatalf("expected path validation error, got %q", res.Content)
	}
	if writer.addCalls != 0 {
		t.Fatalf("writer should not be called, got %d calls", writer.addCalls)
	}
}
