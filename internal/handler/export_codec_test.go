package handler

import (
	stderrs "errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/biox-dev/gobrave/internal/exportcodec"
	exportcodecv1 "github.com/biox-dev/gobrave/internal/exportcodec/v1"
	"github.com/biox-dev/gobrave/internal/utils"
)

// newTestWorkflowHandler 构造只带导出格式注册表的 handler：
// 读盘路径（readXxxJSONFromDir / exportCodec）不依赖其它 service，够用即可。
func newTestWorkflowHandler() *WorkflowHandler {
	reg := exportcodec.NewRegistry()
	// 读侧不会用到 WorkflowService（只有写侧会），故这里传 nil；写侧提交身份用默认值。
	reg.Register(exportcodecv1.NewCodec(nil, nil, utils.GitIdentity{Name: "test", Email: "test@example.com"}))
	return &WorkflowHandler{exportCodecs: reg}
}

// TestExportCodecRegistryWiring 校验装配不遗漏：注册表里必须有写侧使用的当前版本，
// 否则 SaveScript/SaveWorkflow/PublishScript/PublishWorkflow 全部无法落盘。
func TestExportCodecRegistryWiring(t *testing.T) {
	h := newTestWorkflowHandler()

	codec, err := h.exportCodecForWrite()
	if err != nil {
		t.Fatalf("exportCodecForWrite: %v", err)
	}
	if got := codec.Version(); got != exportcodec.CurrentVersion {
		t.Fatalf("write codec version = %q, want %q", got, exportcodec.CurrentVersion)
	}
	// 写出的 version 必须能读回同一个 Codec。
	if _, err := h.exportCodec(codec.Version()); err != nil {
		t.Fatalf("exportCodec(write version): %v", err)
	}
	if versions := h.exportCodecs.Versions(); len(versions) == 0 {
		t.Fatal("registry has no registered codec")
	}
}

// TestExportCodecForFileVersions 覆盖读侧按 version 路由：
// v1 与「无 version 的历史产物」都必须命中 v1（version 前后空白可容忍），
// 未知版本必须报 exportcodec.ErrUnsupportedVersion（调用方据此转 400）。
func TestExportCodecForFileVersions(t *testing.T) {
	h := newTestWorkflowHandler()

	for _, version := range []string{"", exportcodec.VersionV1, " v1 "} {
		raw := []byte(`{"version":"` + version + `"}`)
		codec, err := h.exportCodecForFile(raw)
		if err != nil {
			t.Fatalf("exportCodecForFile(version=%q): %v", version, err)
		}
		if got := codec.Version(); got != exportcodec.VersionV1 {
			t.Fatalf("version %q routed to %q, want %q", version, got, exportcodec.VersionV1)
		}
	}

	for _, version := range []string{"v2", "v0", "unknown"} {
		raw := []byte(`{"version":"` + version + `"}`)
		if _, err := h.exportCodecForFile(raw); !stderrs.Is(err, exportcodec.ErrUnsupportedVersion) {
			t.Fatalf("exportCodecForFile(%q) err = %v, want ErrUnsupportedVersion", version, err)
		}
	}

	// 不是合法 JSON 时是解析错误，而不是「版本不支持」。
	if _, err := h.exportCodecForFile([]byte(`{`)); err == nil || stderrs.Is(err, exportcodec.ErrUnsupportedVersion) {
		t.Fatalf("exportCodecForFile(invalid json) err = %v, want parse error", err)
	}
}

// TestExportCodecWithoutRegistry 校验 Registry 未装配时给出明确错误，
// 而不是让后续以 nil Codec 在上游 panic。
func TestExportCodecWithoutRegistry(t *testing.T) {
	h := &WorkflowHandler{}
	if _, err := h.exportCodecForWrite(); err == nil {
		t.Fatal("exportCodecForWrite without registry should fail")
	}
	if _, err := h.exportCodecForFile([]byte(`{"version":"v1"}`)); err == nil {
		t.Fatal("exportCodecForFile without registry should fail")
	}
}

// TestReadJSONFromDirRoutesByVersion 覆盖读盘入口：文件顶层 version 决定 Codec，
// 未知版本在解析前就被拒绝；无 version 的历史产物仍按 v1 解析。
func TestReadJSONFromDirRoutesByVersion(t *testing.T) {
	h := newTestWorkflowHandler()

	scriptDir := t.TempDir()
	writeTestFile(t, filepath.Join(scriptDir, exportcodec.ScriptJSONFileName), `{"script_id":"s-1","script":{"name":"n"}}`)
	codec, scriptRaw, err := h.readScriptRawFromDir(scriptDir)
	if err != nil {
		t.Fatalf("readScriptRawFromDir (no version): %v", err)
	}
	payload, err := codec.DecodeScript(scriptRaw)
	if err != nil {
		t.Fatalf("DecodeScript: %v", err)
	}
	if payload.ScriptID != "s-1" {
		t.Fatalf("script_id = %q, want s-1", payload.ScriptID)
	}
	if codec.Version() != exportcodec.VersionV1 {
		t.Fatalf("codec version = %q, want %q", codec.Version(), exportcodec.VersionV1)
	}

	workflowDir := t.TempDir()
	writeTestFile(t, filepath.Join(workflowDir, exportcodec.WorkflowJSONFileName), `{"version":"v1","workflow_id":"wf-1","workflow":{"name":"n"},"scripts":[]}`)
	workflowCodec, workflowRaw, err := h.readWorkflowRawFromDir(workflowDir)
	if err != nil {
		t.Fatalf("readWorkflowRawFromDir: %v", err)
	}
	workflowPayload, err := workflowCodec.DecodeWorkflow(workflowRaw)
	if err != nil {
		t.Fatalf("DecodeWorkflow: %v", err)
	}
	if workflowPayload.WorkflowID != "wf-1" {
		t.Fatalf("workflow_id = %q, want wf-1", workflowPayload.WorkflowID)
	}

	// 没有任何导出文件时仍要返回可被 os.IsNotExist 识别的错误（Install 映射为 404）。
	if _, _, err := h.readScriptRawFromDir(t.TempDir()); !os.IsNotExist(err) {
		t.Fatalf("missing script.json err = %v, want os.IsNotExist", err)
	}
	if _, _, err := h.readWorkflowRawFromDir(t.TempDir()); !os.IsNotExist(err) {
		t.Fatalf("missing workflow.json err = %v, want os.IsNotExist", err)
	}

	// 非法 JSON 在窥探 version 阶段就报错，而不是返回零值。
	badDir := t.TempDir()
	writeTestFile(t, filepath.Join(badDir, exportcodec.WorkflowJSONFileName), `{`)
	if _, _, err := h.readWorkflowRawFromDir(badDir); err == nil || os.IsNotExist(err) {
		t.Fatalf("invalid workflow.json err = %v, want parse error", err)
	}
}

// TestRejectUnsupportedVersionFile 覆盖 Install 路径上的 400 语义：
// store 里是更高版本的产物时，读盘必须报 ErrUnsupportedVersion（而不是 500）。
func TestRejectUnsupportedVersionFile(t *testing.T) {
	h := newTestWorkflowHandler()

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, exportcodec.WorkflowJSONFileName), `{"version":"v99","workflow_id":"wf-1"}`)
	writeTestFile(t, filepath.Join(dir, exportcodec.ScriptJSONFileName), `{"version":"v99","script_id":"s-1"}`)

	if _, _, err := h.readWorkflowRawFromDir(dir); !stderrs.Is(err, exportcodec.ErrUnsupportedVersion) {
		t.Fatalf("readWorkflowRawFromDir err = %v, want ErrUnsupportedVersion", err)
	}
	if _, _, err := h.readScriptRawFromDir(dir); !stderrs.Is(err, exportcodec.ErrUnsupportedVersion) {
		t.Fatalf("readScriptRawFromDir err = %v, want ErrUnsupportedVersion", err)
	}
}

// TestReadIDsFromStoreDir 覆盖「远程 store 里脚本/工作流 ID 未知」的回退路径：
// 从 store 仓库 HEAD 提交里的导出文件读 ID（文件可能嵌套在任意层级），读不到时返回空字符串而不是报错。
func TestReadIDsFromStoreDir(t *testing.T) {
	h := newTestWorkflowHandler()

	storeDir := t.TempDir()
	commitTestRepo(t, storeDir, map[string]string{
		"nested/" + exportcodec.ScriptJSONFileName:   `{"script_id":"s-remote"}`,
		"nested/" + exportcodec.WorkflowJSONFileName: `{"workflow_id":"wf-remote"}`,
	})

	if got := h.readScriptIDFromStoreDir(storeDir); got != "s-remote" {
		t.Fatalf("readScriptIDFromStoreDir = %q, want s-remote", got)
	}
	if got := h.readWorkflowIDFromStoreDir(storeDir); got != "wf-remote" {
		t.Fatalf("readWorkflowIDFromStoreDir = %q, want wf-remote", got)
	}

	empty := t.TempDir()
	if got := h.readScriptIDFromStoreDir(empty); got != "" {
		t.Fatalf("readScriptIDFromStoreDir(empty) = %q, want empty", got)
	}
	if got := h.readWorkflowIDFromStoreDir(empty); got != "" {
		t.Fatalf("readWorkflowIDFromStoreDir(empty) = %q, want empty", got)
	}
}

// TestReadWorkflowJSONNormalizesDagDefinition 覆盖 v1 特有的字段规整：
// 导出侧把 dag_definition 展开成对象，落库列是字符串，读盘时必须改回字符串。
func TestReadWorkflowJSONNormalizesDagDefinition(t *testing.T) {
	h := newTestWorkflowHandler()

	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, exportcodec.WorkflowJSONFileName),
		`{"version":"v1","workflow_id":"wf-1","workflow":{"dag_definition":{"nodes":[1,2]}}}`)

	codec, raw, err := h.readWorkflowRawFromDir(dir)
	if err != nil {
		t.Fatalf("readWorkflowRawFromDir: %v", err)
	}
	payload, err := codec.DecodeWorkflow(raw)
	if err != nil {
		t.Fatalf("DecodeWorkflow: %v", err)
	}
	got, ok := payload.Workflow["dag_definition"].(string)
	if !ok {
		t.Fatalf("dag_definition = %#v, want string", payload.Workflow["dag_definition"])
	}
	if got != `{"nodes":[1,2]}` {
		t.Fatalf("dag_definition = %q, want serialized object", got)
	}
}
