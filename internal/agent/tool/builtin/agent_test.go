package builtin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/native"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

func TestAgentToolDefinition(t *testing.T) {
	def := AgentTool(Deps{}).Definition()
	if def.Name != native.AgentToolName {
		t.Fatalf("tool name = %q, want %q", def.Name, native.AgentToolName)
	}
	if def.Description == "" {
		t.Fatalf("tool description is empty")
	}
	if _, ok := def.InputSchema["properties"]; !ok {
		t.Fatalf("input schema = %v, want properties", def.InputSchema)
	}
}

func TestAgentToolRequiresContextDependencies(t *testing.T) {
	exec := tool.NewExecutor(tool.NewRegistryWith(AgentTool(Deps{})))
	args, err := json.Marshal(native.AgentParams{Prompt: "find the logger"})
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	// 缺 SubAgentOptions（LLM 客户端工厂）：返回失败结果而不是 panic。
	res := exec.Execute(context.Background(), tool.Call{Name: native.AgentToolName, Arguments: args})
	if !res.IsError {
		t.Fatalf("expected error result without context options, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "llm client factory") {
		t.Fatalf("content = %q, want llm client factory hint", res.Content)
	}
}
