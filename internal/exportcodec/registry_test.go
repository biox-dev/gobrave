package exportcodec

import (
	"context"
	"testing"

	"github.com/biox-dev/gobrave/internal/types"
)

// stubCodec 是最小 Codec 实现，只用于注册表本身的行为测试
// （注册表只关心 Version()/Get 的键，不关心读写实现）。
type stubCodec struct {
	version string
}

func (c *stubCodec) Version() string { return c.version }

func (c *stubCodec) WriteCommitScriptFiles(_ context.Context, _ ScriptWriteRequest) (*types.ScriptJSONExportResponse, error) {
	return &types.ScriptJSONExportResponse{Version: c.version}, nil
}

func (c *stubCodec) WriteCommmitWorkflowFiles(_ context.Context, _ WorkflowWriteRequest) (*types.WorkflowJSONExportResponse, error) {
	return &types.WorkflowJSONExportResponse{Version: c.version}, nil
}

func (c *stubCodec) DecodeScript(_ []byte) (*types.ScriptJSONExportResponse, error) {
	return &types.ScriptJSONExportResponse{Version: c.version}, nil
}

func (c *stubCodec) DecodeWorkflow(_ []byte) (*types.WorkflowJSONExportResponse, error) {
	return &types.WorkflowJSONExportResponse{Version: c.version}, nil
}

func (c *stubCodec) ScriptSnapshotDir(workflowDir, scriptID string) string {
	return workflowDir + "/" + scriptID
}

func (c *stubCodec) ScriptIDFromExportScript(map[string]any) string { return "" }

func (c *stubCodec) InstallScript(_ context.Context, _ ScriptInstallRequest) (*ScriptInstallResult, error) {
	return &ScriptInstallResult{}, nil
}

func (c *stubCodec) InstallWorkflow(_ context.Context, _ WorkflowInstallRequest) (*WorkflowInstallResult, error) {
	return &WorkflowInstallResult{}, nil
}

// TestRegistryRegisterAndGet 覆盖注册表的基本契约：
// 键是 codec.Version()，version 前后空白可容忍，未注册返回 nil（调用方据此转 400）。
func TestRegistryRegisterAndGet(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&stubCodec{version: VersionV1})
	// 同版本重复注册以最后一次为准（测试里替换实现的常用手法）。
	replacement := &stubCodec{version: VersionV1}
	reg.Register(replacement)

	for _, version := range []string{VersionV1, " v1 "} {
		got := reg.Get(version)
		if got == nil {
			t.Fatalf("Get(%q) = nil, want registered codec", version)
		}
		if got != Codec(replacement) {
			t.Fatalf("Get(%q) did not return the latest registration", version)
		}
	}

	if got := reg.Get("v2"); got != nil {
		t.Fatalf("Get(v2) = %v, want nil", got)
	}
	if got := reg.Get(""); got != nil {
		t.Fatalf("Get(\"\") = %v, want nil (空版本由调用方决定如何处理)", got)
	}

	if versions := reg.Versions(); len(versions) != 1 || versions[0] != VersionV1 {
		t.Fatalf("Versions() = %v, want [%s]", versions, VersionV1)
	}
	if items := reg.List(); len(items) != 1 {
		t.Fatalf("List() len = %d, want 1", len(items))
	}
}

// TestRegistryNilSafety 覆盖 nil receiver 与空版本注册：装配漏了不应 panic，
// 而是让调用方拿到 nil / 明确的错误路径。
func TestRegistryNilSafety(t *testing.T) {
	var reg *Registry
	if got := reg.Get(VersionV1); got != nil {
		t.Fatalf("nil registry Get = %v, want nil", got)
	}
	if got := reg.List(); got != nil {
		t.Fatalf("nil registry List = %v, want nil", got)
	}
	if got := reg.Versions(); got != nil {
		t.Fatalf("nil registry Versions = %v, want nil", got)
	}
	reg.Register(&stubCodec{version: VersionV1}) // 不应 panic

	fresh := NewRegistry()
	fresh.Register(nil)
	fresh.Register(&stubCodec{version: "  "})
	if versions := fresh.Versions(); len(versions) != 0 {
		t.Fatalf("Versions() = %v, want empty", versions)
	}
}

// TestPeekVersion 覆盖读侧取版本号：只认顶层 version，缺失/空白返回空字符串，
// 非法 JSON 报错（调用方据此区分 400 与 500）。
func TestPeekVersion(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{`{"version":"v1"}`, VersionV1},
		{`{"version":"  v1  "}`, VersionV1},
		{`{"version":""}`, ""},
		{`{"script_id":"s-1"}`, ""},
	}
	for _, tc := range cases {
		got, err := PeekVersion([]byte(tc.raw))
		if err != nil {
			t.Fatalf("PeekVersion(%s): %v", tc.raw, err)
		}
		if got != tc.want {
			t.Fatalf("PeekVersion(%s) = %q, want %q", tc.raw, got, tc.want)
		}
	}

	if _, err := PeekVersion([]byte(`{`)); err == nil {
		t.Fatal("PeekVersion(invalid) should fail")
	}
}
