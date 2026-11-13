package providers

import (
	"context"
	"testing"

	agentclient "github.com/biox-dev/gobrave/internal/agent/client"
	"github.com/biox-dev/gobrave/internal/agent/native"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// newTestTool 构造一个只关心名字的测试工具。
func newTestTool(name string) tool.Tool {
	return tool.NewFunc(name, "test tool "+name, tool.Schema("", map[string]any{}),
		func(_ context.Context, _ struct{}) (any, error) { return nil, nil })
}

func TestCustomAgentToolRegistryFiltersByProfile(t *testing.T) {
	reg := tool.NewRegistryWith(newTestTool("read_file"), newTestTool("terminal_exec"))
	agent := &customAgent{opts: agentclient.Options{Tools: reg}}

	// 未配置白名单：注入全部注册表工具。
	all := agent.toolRegistry(context.Background(), agenttypes.Request{}, nil)
	for _, name := range []string{"read_file", "terminal_exec", native.AgentToolName} {
		if !all.Has(name) {
			t.Fatalf("unfiltered registry missing tool %q (got %v)", name, all.Names())
		}
	}

	// 白名单只保留指定工具；未注册的名称被忽略，框架级工具始终可用。
	subset := agent.toolRegistry(context.Background(), agenttypes.Request{
		Tools: []string{"terminal_exec", "not_registered"},
	}, nil)
	if subset.Has("read_file") {
		t.Fatalf("whitelist should exclude read_file (got %v)", subset.Names())
	}
	for _, name := range []string{"terminal_exec", native.AgentToolName} {
		if !subset.Has(name) {
			t.Fatalf("whitelisted registry missing tool %q (got %v)", name, subset.Names())
		}
	}
}
