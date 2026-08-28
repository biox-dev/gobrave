package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent"
)

// mockProvider 是自研的 mock Provider，用于在没有真实 Agent 的情况下
// 让完整调用链路可以跑通（例如 AI 摘要的异步生成）。
type mockProvider struct{}

// NewMock 创建 mock Provider。
func NewMock() agent.Provider { return mockProvider{} }

func (mockProvider) Name() string { return agent.ProviderMock }

func (mockProvider) New(opts agent.Options) (agent.Agent, error) {
	return &mockAgent{opts: opts}, nil
}

type mockAgent struct {
	opts agent.Options
}

func (a *mockAgent) Name() string { return agent.ProviderMock }

func (a *mockAgent) Invoke(_ context.Context, req agent.Request) (*agent.Result, error) {
	content := fmt.Sprintf("[mock:%s] %s", a.opts.Model, lastUserPrompt(req))
	return &agent.Result{Content: content}, nil
}

func (a *mockAgent) Stream(ctx context.Context, req agent.Request, handler agent.StreamHandler) (*agent.Result, error) {
	full := fmt.Sprintf("[mock:%s] %s", a.opts.Model, lastUserPrompt(req))

	// 按固定块切分模拟流式输出。
	const chunkSize = 8
	runes := []rune(full)
	var sb strings.Builder
	for i := 0; i < len(runes); i += chunkSize {
		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunk := string(runes[i:end])
		sb.WriteString(chunk)
		if err := handler(ctx, agent.StreamEvent{Type: agent.StreamEventText, Content: chunk}); err != nil {
			return nil, err
		}
	}

	if err := handler(ctx, agent.StreamEvent{Type: agent.StreamEventDone}); err != nil {
		return nil, err
	}
	return &agent.Result{Content: sb.String()}, nil
}

// lastUserPrompt 提取最后一条 user 消息作为提示词；无 user 消息时回退到系统提示。
func lastUserPrompt(req agent.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == agent.RoleUser {
			return req.Messages[i].Content
		}
	}
	return req.SystemPrompt
}
