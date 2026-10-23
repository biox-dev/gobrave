package agent

import (
	"context"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// 本文件仅为测试提供导出包装，使外部测试包（agent_test）能够访问 AgentService 的
// 未导出内部方法。它不参与生产构建（仅在 go test 编译包 agent 时生效）。

// InjectMemoryForTest 暴露 AgentService.injectMemoryWithContext 供外部测试调用。
func (s *AgentService) InjectMemoryForTest(ctx context.Context, prompt string, rc *tool.RuntimeContext) string {
	return s.injectMemoryWithContext(ctx, prompt, rc)
}

// ExtractMemoryForTest 暴露 AgentService.extractMemory 供外部测试调用。
func (s *AgentService) ExtractMemoryForTest(ctx context.Context, task *Task, req Request, result *Result) {
	s.extractMemory(ctx, task, req, result)
}
