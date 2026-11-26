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
	addInput runtimecontext.DatasetFileAddInput
	addCalls int
}

func (f *fakeDatasetWriter) AddFileToDataset(_ context.Context, in runtimecontext.DatasetFileAddInput) (runtimecontext.DatasetFileAddResult, error) {
	f.addCalls++
	f.addInput = in
	return runtimecontext.DatasetFileAddResult{
		DatasetID: in.DatasetID,
		FileID:    99,
		FilePath:  in.Path,
		FileName:  "sample.db",
		Role:      types.RoleDatabase,
		Format:    "db",
		Size:      1024,
	}, nil
}

func TestAddFileToDatasetTool(t *testing.T) {
	writer := &fakeDatasetWriter{}
	exec := tool.NewExecutor(tool.NewRegistryWith(AddFileToDataset(writer)))

	ctx := tool.WithTaskFacts(context.Background(), agenttypes.TaskFacts{ProjectID: 7})
	res := exec.Execute(ctx, tool.Call{
		ID:        "c1",
		Name:      AddFileToDatasetToolName,
		Arguments: json.RawMessage(`{"dataset_id":"@dataset:42","path":"/data/7/analysis/x.db","role":"DATABASE"}`),
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
	if writer.addInput.Path != "/data/7/analysis/x.db" || writer.addInput.Role != types.RoleDatabase {
		t.Fatalf("unexpected input: %+v", writer.addInput)
	}

	var out AddFileToDatasetOutput
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("unmarshal result %q: %v", res.Content, err)
	}
	if out.DatasetID != 42 || out.FileID != 99 || out.Role != "DATABASE" {
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
