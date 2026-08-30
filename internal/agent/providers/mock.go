package providers

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/agent"
	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// mockProvider 是自研的 mock Provider，用于在没有真实 Agent 的情况下
// 让完整调用链路可以跑通（例如 AI 摘要的异步生成）。
//
// 它还演示了 design.md 中的两类链路：
//   - 权限链路：当请求的 Env 携带 MOCK_PERMISSION_DEMO=true 时，mock Agent 会通过
//     Runtime.RequestPermission 请求一次“写文件”权限，并阻塞等待 UI 决策；
//   - 工具调用链路：当请求的 Env 携带 MOCK_TOOL_DEMO=true 时，mock Agent 会模拟模型
//     发起一次工具调用，通过 ToolRunner 执行并输出 tool_call / tool_result 事件。
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

	// 演示工具调用链路：模拟模型发起一次工具调用并执行。
	if demoToolCall(req) {
		content = fmt.Sprintf("%s\n%s", content, a.runMockToolCall(ctx, req, rt))
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

	// 演示工具调用链路：ToolRunner 会输出 tool_call / tool_result 事件，
	// 这里再把结果以文本增量拼进输出，便于观察。
	if demoToolCall(req) {
		note := "\n" + a.runMockToolCall(ctx, req, rt)
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

// demoToolCall 判断本次请求是否触发 tool-call 演示。
func demoToolCall(req agent.Request) bool {
	return strings.EqualFold(strings.TrimSpace(req.Env["MOCK_TOOL_DEMO"]), "true")
}

// mockEchoInput 是 echo 演示工具的入参。
type mockEchoInput struct {
	Text string `json:"text"`
}

// mockEchoOutput 是 echo 演示工具的返回。
type mockEchoOutput struct {
	Echoed string `json:"echoed"`
}

// defaultMockTools 返回 mock Provider 内置的演示工具集（Options.Tools 为空时兜底）。
//
// 提供两个工具用于演示 tool-call 链路：
//   - echo：原样回显文本；
//   - now：返回当前 Unix 时间戳（秒）。
func defaultMockTools() *tool.Registry {
	return tool.NewRegistryWith(
		tool.NewFunc("echo", "echo the input text back", tool.Schema("echo input", map[string]any{
			"text": tool.StringProperty("text to echo"),
		}, "text"), func(_ context.Context, in mockEchoInput) (mockEchoOutput, error) {
			return mockEchoOutput{Echoed: in.Text}, nil
		}),
		tool.NewFunc("now", "return the current unix timestamp in seconds", tool.Schema("", map[string]any{}), func(_ context.Context, _ struct{}) (map[string]any, error) {
			return map[string]any{"unix": time.Now().Unix()}, nil
		}),
	)
}

// runMockToolCall 模拟模型发起一次工具调用并执行，返回展示用文本。
//
// 流程：
//  1. 工具名优先取 Env["MOCK_TOOL_NAME"]，默认 "echo"；
//  2. 工具注册表优先取 Options.Tools，为空时回退到内置演示工具集；
//  3. 通过 ToolRunner 执行（Stream 场景会输出 tool_call / tool_result 事件）；
//  4. 返回形如 "[tool=echo] {...}" 的文本，拼进最终结果。
func (a *mockAgent) runMockToolCall(ctx context.Context, req agent.Request, rt agent.Runtime) string {
	name := strings.TrimSpace(req.Env["MOCK_TOOL_NAME"])
	if name == "" {
		name = "echo"
	}

	reg := a.opts.Tools
	if reg == nil {
		reg = defaultMockTools()
	}

	runner := agent.NewToolRunner(tool.NewExecutor(reg), rt)
	res := runner.Run(ctx, agent.ToolCall{
		ID:        "mock_call_1",
		Name:      name,
		Arguments: map[string]any{"text": lastUserPrompt(req)},
	})
	return fmt.Sprintf("[tool=%s] %s", name, res.Content)
}
