package providers

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent"
)

// mockProvider 是自研的 mock Provider，用于在没有真实 Agent 的情况下
// 让完整调用链路可以跑通（例如 AI 摘要的异步生成）。
//
// 它还演示了 design.md 中的权限链路：当请求的 Env 携带 MOCK_PERMISSION_DEMO=true 时，
// mock Agent 会通过 Runtime.RequestPermission 请求一次“写文件”权限，并阻塞等待 UI 决策，
// 批准后继续、拒绝则以 ErrPermissionDenied 结束。
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

func (a *mockAgent) Invoke(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	content := fmt.Sprintf("[mock:%s] %s", a.opts.Model, lastUserPrompt(req))

	if demoPermission(req) {
		decision, err := requestDemoPermission(ctx, rt, req)
		if err != nil {
			return nil, err
		}
		content = fmt.Sprintf("%s\n[permission=%s]", content, decision)
	}
	return &agent.Result{Content: content}, nil
}

func (a *mockAgent) Stream(ctx context.Context, req agent.Request, rt agent.Runtime) (*agent.Result, error) {
	full := fmt.Sprintf("[mock:%s] %s", a.opts.Model, lastUserPrompt(req))

	// 按固定块切分模拟流式输出，通过 Runtime.Emit 对外输出事件。
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
		if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventText, Content: chunk}); err != nil {
			return nil, err
		}
	}

	// 演示权限链路：请求一次“写文件”权限并等待 UI 决策。
	if demoPermission(req) {
		decision, err := requestDemoPermission(ctx, rt, req)
		if err != nil {
			return nil, err
		}
		note := fmt.Sprintf("\n[permission=%s]", decision)
		sb.WriteString(note)
		if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventText, Content: note}); err != nil {
			return nil, err
		}
	}

	if err := rt.Emit(ctx, agent.StreamEvent{Type: agent.StreamEventDone}); err != nil {
		return nil, err
	}
	return &agent.Result{Content: sb.String()}, nil
}

// demoPermission 判断本次请求是否触发权限演示。
func demoPermission(req agent.Request) bool {
	return strings.EqualFold(strings.TrimSpace(req.Env["MOCK_PERMISSION_DEMO"]), "true")
}

// requestDemoPermission 构造一个 write 操作并通过 Runtime 请求权限。
func requestDemoPermission(ctx context.Context, rt agent.Runtime, req agent.Request) (agent.PermissionDecision, error) {
	op := agent.Operation{
		Type:    agent.OperationWrite,
		Path:    filepath.Join(req.WorkingDir, "demo.txt"),
		Content: "[mock] demo write content",
	}

	decision, err := rt.RequestPermission(ctx, op)
	if err != nil {
		return "", err
	}
	if decision == agent.DecisionDeny {
		return decision, fmt.Errorf("%w: write %s", agent.ErrPermissionDenied, op.Path)
	}
	return decision, nil
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
