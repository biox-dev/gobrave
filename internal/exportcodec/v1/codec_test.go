package v1

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/biox-dev/gobrave/internal/exportcodec"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
)

// fakeWorkflowService 只实现写侧用到的两个方法；其余方法由嵌入的（nil）接口提供，
// 测试不会调用它们，这样避免为一个窄用途去实现 30+ 个方法。
type fakeWorkflowService struct {
	interfaces.WorkflowService
	script   *types.ScriptJSONExportResponse
	workflow *types.WorkflowJSONExportResponse
	err      error
}

func (f *fakeWorkflowService) GenerateScriptJSONByScriptID(ctx context.Context, scriptID int64) (*types.ScriptJSONExportResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.script, nil
}

func (f *fakeWorkflowService) GenerateWorkflowJSONByWorkflowID(ctx context.Context, workflowID int64, storageBaseDir string) (*types.WorkflowJSONExportResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.workflow, nil
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestWriteScriptFilesStampsVersion 保证写出的 script.json 顶层 version 等于 Codec.Version()，
// 否则安装侧从 Registry 取 Codec 时无法路由回同一个版本。
func TestWriteScriptFilesStampsVersion(t *testing.T) {
	codec := NewCodec(&fakeWorkflowService{
		script: &types.ScriptJSONExportResponse{
			ScriptID: "s-1",
			Script:   map[string]any{"name": "demo"},
		},
	})

	scriptDir := filepath.Join(t.TempDir(), "nested", "script")
	if _, err := codec.WriteScriptFiles(context.Background(), exportcodec.ScriptWriteRequest{ScriptPK: 1, ScriptDir: scriptDir}); err != nil {
		t.Fatalf("WriteScriptFiles: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(scriptDir, exportcodec.ScriptJSONFileName))
	if err != nil {
		t.Fatalf("read %s: %v", exportcodec.ScriptJSONFileName, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["version"] != exportcodec.VersionV1 {
		t.Fatalf("version = %#v, want %q", decoded["version"], exportcodec.VersionV1)
	}
	if decoded["script_id"] != "s-1" {
		t.Fatalf("script_id = %#v, want s-1", decoded["script_id"])
	}
}

// TestWriteWorkflowFilesSnapshotsScripts 覆盖 v1 的目录布局约定：
// workflow.json 落盘 + 引用的脚本快照到 <workflowDir>/script/<scriptID>，且不带脚本自身的 .git。
func TestWriteWorkflowFilesSnapshotsScripts(t *testing.T) {
	baseDir := t.TempDir()
	projectID := "p-1"
	scriptID := "s-1"

	scriptDir := utils.GetScriptFileDir(baseDir, projectID, scriptID)
	writeFile(t, filepath.Join(scriptDir, "main.R"), "print('hi')\n")
	writeFile(t, filepath.Join(scriptDir, ".git", "HEAD"), "ref: refs/heads/main\n")

	codec := NewCodec(&fakeWorkflowService{
		workflow: &types.WorkflowJSONExportResponse{
			WorkflowID: "wf-1",
			Workflow:   map[string]any{"name": "demo", "dag_definition": map[string]any{"nodes": []any{}}},
			Scripts: []map[string]any{
				{"component_id": scriptID},
				{"script_id": "s-2"},
				{},
			},
		},
	})

	workflowDir := filepath.Join(t.TempDir(), "workflow")
	payload, err := codec.WriteWorkflowFiles(context.Background(), exportcodec.WorkflowWriteRequest{
		WorkflowPK:  1,
		ProjectID:   projectID,
		BaseDir:     baseDir,
		WorkflowDir: workflowDir,
	})
	if err != nil {
		t.Fatalf("WriteWorkflowFiles: %v", err)
	}
	if payload.Version != exportcodec.VersionV1 {
		t.Fatalf("payload.Version = %q, want %q", payload.Version, exportcodec.VersionV1)
	}

	raw, err := os.ReadFile(filepath.Join(workflowDir, exportcodec.WorkflowJSONFileName))
	if err != nil {
		t.Fatalf("read %s: %v", exportcodec.WorkflowJSONFileName, err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded["version"] != exportcodec.VersionV1 {
		t.Fatalf("version = %#v, want %q", decoded["version"], exportcodec.VersionV1)
	}

	// 快照命中：文件在约定位置，且 .git 未被带进 workflow 仓库。
	if _, err := os.Stat(filepath.Join(ScriptSnapshotDir(workflowDir, scriptID), "main.R")); err != nil {
		t.Fatalf("snapshot main.R missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ScriptSnapshotDir(workflowDir, scriptID), ".git")); !os.IsNotExist(err) {
		t.Fatalf("script .git must not be snapshotted, stat err = %v", err)
	}
	// 源脚本目录不存在（如 s-2）时按幂等处理，不产生快照也不报错。
	if _, err := os.Stat(ScriptSnapshotDir(workflowDir, "s-2")); !os.IsNotExist(err) {
		t.Fatalf("missing source script should not create snapshot, stat err = %v", err)
	}
}

// TestDecodeWorkflowRoundTrip 保证写出与读回是同一套格式（version 相同、字段可还原）。
func TestDecodeWorkflowRoundTrip(t *testing.T) {
	workflowDir := t.TempDir()
	codec := NewCodec(&fakeWorkflowService{
		workflow: &types.WorkflowJSONExportResponse{
			WorkflowID: "wf-1",
			Workflow:   map[string]any{"name": "demo", "dag_definition": map[string]any{"a": 1}},
		},
	})
	written, err := codec.WriteWorkflowFiles(context.Background(), exportcodec.WorkflowWriteRequest{
		WorkflowDir: workflowDir,
		BaseDir:     t.TempDir(),
	})
	if err != nil {
		t.Fatalf("WriteWorkflowFiles: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(workflowDir, exportcodec.WorkflowJSONFileName))
	if err != nil {
		t.Fatalf("read workflow.json: %v", err)
	}

	version, err := exportcodec.PeekVersion(raw)
	if err != nil {
		t.Fatalf("PeekVersion: %v", err)
	}
	if version != written.Version {
		t.Fatalf("peek version = %q, want %q", version, written.Version)
	}

	decoded, err := codec.DecodeWorkflow(raw)
	if err != nil {
		t.Fatalf("DecodeWorkflow: %v", err)
	}
	if decoded.WorkflowID != "wf-1" {
		t.Fatalf("workflow_id = %q, want wf-1", decoded.WorkflowID)
	}
	// dag_definition 在写侧是对象，读侧必须规整回字符串（落库列是字符串）。
	if _, ok := decoded.Workflow["dag_definition"].(string); !ok {
		t.Fatalf("dag_definition = %#v, want string", decoded.Workflow["dag_definition"])
	}
}

// TestWriteRequiresWorkflowService 校验未注入 WorkflowService 时给出明确错误，
// 而不是在调用 service 时 nil panic。
func TestWriteRequiresWorkflowService(t *testing.T) {
	codec := NewCodec(nil)
	if _, err := codec.WriteScriptFiles(context.Background(), exportcodec.ScriptWriteRequest{ScriptDir: t.TempDir()}); err == nil {
		t.Fatal("WriteScriptFiles without workflow service should fail")
	}
	if _, err := codec.WriteWorkflowFiles(context.Background(), exportcodec.WorkflowWriteRequest{WorkflowDir: t.TempDir(), BaseDir: t.TempDir()}); err == nil {
		t.Fatal("WriteWorkflowFiles without workflow service should fail")
	}
}
