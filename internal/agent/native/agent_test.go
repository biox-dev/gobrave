package native

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/llm/provider"
	agentruntime "github.com/biox-dev/gobrave/internal/agent/runtime"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// blockingAgentTool 是名为 agent 的测试工具：进入即登记并发，阻塞到 release 后返回。
type blockingAgentTool struct {
	probe   *concurrencyProbe
	started *sync.WaitGroup
	release <-chan struct{}
}

func (b blockingAgentTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        AgentToolName,
		Description: "blocking agent",
		InputSchema: tool.Schema("agent", map[string]any{
			"prompt": tool.StringProperty("task"),
		}, "prompt"),
	}
}

func (b blockingAgentTool) Execute(ctx context.Context, args json.RawMessage) (any, error) {
	b.probe.enter()
	defer b.probe.leave()

	b.started.Done()
	select {
	case <-b.release:
	case <-ctx.Done():
	}
	return "ok:" + string(args), nil
}

func TestExecuteToolsRunsAgentCallsConcurrently(t *testing.T) {
	probe := &concurrencyProbe{}
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(2)

	var once sync.Once
	closeRelease := func() { once.Do(func() { close(release) }) }
	go func() {
		started.Wait()
		closeRelease()
	}()
	watchdog := time.AfterFunc(2*time.Second, closeRelease)
	defer watchdog.Stop()

	registry := tool.NewRegistryWith(blockingAgentTool{probe: probe, started: &started, release: release})
	calls := []provider.ToolCall{
		{ID: "call-1", Name: AgentToolName, Input: `{"prompt":"a"}`},
		{ID: "call-2", Name: AgentToolName, Input: `{"prompt":"b"}`},
	}

	cfg := &Config{}
	results := cfg.executeTools(context.Background(), tool.NewExecutor(registry), calls, agentruntime.NewStandaloneRuntime(nil), nil)

	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	// 并发完成顺序不影响结果顺序：必须与模型给出的 tool_call 顺序一致。
	if results[0].ToolCallID != "call-1" || results[1].ToolCallID != "call-2" {
		t.Fatalf("results order = %q, %q; want call-1, call-2", results[0].ToolCallID, results[1].ToolCallID)
	}
	if results[0].IsError || results[1].IsError {
		t.Fatalf("unexpected tool errors: %+v", results)
	}
	if probe.peak() < 2 {
		t.Fatalf("peak concurrency = %d, want >= 2", probe.peak())
	}
}
