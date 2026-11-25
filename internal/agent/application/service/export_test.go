package service

import (
	"context"

	. "github.com/biox-dev/gobrave/internal/agent/types"
)

// 本文件仅为测试提供导出包装，使外部测试包（service_test）能够访问 AgentService 的
// 未导出内部方法。它不参与生产构建（仅在 go test 编译包 service 时生效）。

// InjectMemoryForTest 暴露 AgentService.injectMemoryWithContext 供外部测试调用。
func (s *AgentService) InjectMemoryForTest(ctx context.Context, prompt string, facts TaskFacts) string {
	return s.injectMemoryWithContext(ctx, prompt, facts)
}

// ConversationHistoryForTest 暴露 AgentService.conversationHistory 供外部测试调用。
func (s *AgentService) ConversationHistoryForTest(ctx context.Context, task *Task) []Message {
	return s.conversationHistory(ctx, task, Request{})
}

// ConversationHistoryWithRequestForTest 暴露带请求的 conversationHistory（可触发压缩）。
func (s *AgentService) ConversationHistoryWithRequestForTest(ctx context.Context, task *Task, req Request) []Message {
	return s.conversationHistory(ctx, task, req)
}

// CompactionOptionsForTest 暴露生效后的压缩配置。
func (s *AgentService) CompactionOptionsForTest() CompactionOptions {
	return s.compaction
}

// ScheduleCompactionForTest 暴露轮末异步预压缩入口。
func (s *AgentService) ScheduleCompactionForTest(ctx context.Context, task *Task, req Request) {
	s.scheduleCompaction(ctx, task, req)
}

// ExtractMemoryForTest 暴露 AgentService.extractMemory 供外部测试调用。
func (s *AgentService) ExtractMemoryForTest(ctx context.Context, task *Task, req Request, result *Result) {
	s.extractMemory(ctx, task, req, result)
}
