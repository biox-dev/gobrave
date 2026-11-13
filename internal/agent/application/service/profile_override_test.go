package service

import (
	"slices"
	"testing"

	. "github.com/biox-dev/gobrave/internal/agent/types"
)

func TestApplyProfileOverridesTools(t *testing.T) {
	// Profile 未配置工具白名单：保留请求级 Tools（空 = 全部）。
	req := Request{Tools: []string{"read_file"}}
	if got := applyProfileOverrides(req, &Profile{}).Tools; !slices.Equal(got, []string{"read_file"}) {
		t.Fatalf("tools = %v, want request-level value", got)
	}

	// Profile 配置了白名单：以 Profile 为准。
	got := applyProfileOverrides(req, &Profile{Tools: []string{"terminal_exec", "glob"}}).Tools
	if want := []string{"terminal_exec", "glob"}; !slices.Equal(got, want) {
		t.Fatalf("tools = %v, want %v", got, want)
	}
}
