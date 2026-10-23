package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	agentclient "github.com/biox-dev/gobrave/internal/agent/client"
	agentruntime "github.com/biox-dev/gobrave/internal/agent/runtime"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	agentinterfaces "github.com/biox-dev/gobrave/internal/agent/types/interface"
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
func NewMock() agentclient.Provider { return mockProvider{} }

func (mockProvider) Name() string { return agenttypes.ProviderMock }

func (mockProvider) New(opts agentclient.Options) (agentinterfaces.Agent, error) {
	return &mockAgent{opts: opts}, nil
}

type mockAgent struct {
	opts agentclient.Options
}

func (a *mockAgent) Name() string { return agenttypes.ProviderMock }

func (a *mockAgent) Invoke(ctx context.Context, req agenttypes.Request, rt agentinterfaces.Runtime) (*agenttypes.Result, error) {
	content := fmt.Sprintf("[mock:%s] %s", req.ModelID, lastUserPrompt(req))

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

	// 演示提问链路：模拟模型通过 ask_user 向用户提问并等待作答。
	if demoAskUser(req) {
		answer, err := requestDemoQuestion(ctx, rt)
		if err != nil {
			return nil, err
		}
		body, _ := json.Marshal(answer)
		content = fmt.Sprintf("%s\n[ask_user=%s]", content, body)
	}
	return &agenttypes.Result{Content: content}, nil
}

func (a *mockAgent) Stream(ctx context.Context, req agenttypes.Request, rt agentinterfaces.Runtime) (*agenttypes.Result, error) {
	full := fmt.Sprintf("[mock:%s] %s", req.ModelID, lastUserPrompt(req))

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
		if err := rt.Emit(ctx, agenttypes.StreamEvent{Type: agenttypes.StreamEventText, Content: chunk}); err != nil {
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
		if err := rt.Emit(ctx, agenttypes.StreamEvent{Type: agenttypes.StreamEventText, Content: note}); err != nil {
			return nil, err
		}
	}

	// 演示工具调用链路：ToolRunner 会输出 tool_call / tool_result 事件，
	// 这里再把结果以文本增量拼进输出，便于观察。
	if demoToolCall(req) {
		note := "\n" + a.runMockToolCall(ctx, req, rt)
		sb.WriteString(note)
		if err := rt.Emit(ctx, agenttypes.StreamEvent{Type: agenttypes.StreamEventText, Content: note}); err != nil {
			return nil, err
		}
	}

	// 演示提问链路：模拟模型通过 ask_user 向用户提问并阻塞等待作答。
	if demoAskUser(req) {
		answer, err := requestDemoQuestion(ctx, rt)
		if err != nil {
			return nil, err
		}
		body, _ := json.Marshal(answer)
		note := fmt.Sprintf("\n[ask_user=%s]", body)
		sb.WriteString(note)
		if err := rt.Emit(ctx, agenttypes.StreamEvent{Type: agenttypes.StreamEventText, Content: note}); err != nil {
			return nil, err
		}
	}

	// 完整 assistant 消息块：与真实 Provider 对齐（时间线以完整块为准），
	// 由 TimelineRecorder 在轮内落库为 KindAssistant。
	if err := rt.Emit(ctx, agenttypes.StreamEvent{
		Type: agenttypes.StreamEventMessage,
		Data: agenttypes.MessageBlock{ID: fmt.Sprintf("mock-%d", time.Now().UnixNano()), Content: sb.String()},
	}); err != nil {
		return nil, err
	}

	if err := rt.Emit(ctx, agenttypes.StreamEvent{Type: agenttypes.StreamEventDone}); err != nil {
		return nil, err
	}
	return &agenttypes.Result{Content: sb.String()}, nil
}

// demoPermission 判断本次请求是否触发权限演示。
func demoPermission(req agenttypes.Request) bool {
	return strings.EqualFold(strings.TrimSpace(req.Env["MOCK_PERMISSION_DEMO"]), "true")
}

// demoAskUser 判断本次请求是否触发提问（ask_user）演示。
func demoAskUser(req agenttypes.Request) bool {
	return strings.EqualFold(strings.TrimSpace(req.Env["MOCK_ASK_USER_DEMO"]), "true")
}

// requestDemoQuestion 构造一个示例问题，并通过 Runtime.AskUser 阻塞等待用户作答。
//
// 覆盖单选 + 自定义输入两种交互，供 mock 链路联调 / 演示使用。
func requestDemoQuestion(ctx context.Context, rt agentinterfaces.Runtime) (agenttypes.AskUserAnswer, error) {
	input := agenttypes.AskUserInput{
		RequestID:   "ask_001",
		Title:       "选择分析类型",
		Description: "请确认你希望进行哪种差异分析。",
		Questions: []agenttypes.Question{
			{
				ID:       "analysis_type",
				Type:     agenttypes.QuestionTypeSingleChoice,
				Title:    "你要分析什么？",
				Required: true,
				Options: []agenttypes.QuestionOption{
					{Label: "差异表达分析", Value: "differential_expression", Description: "适用于 RNA-seq 基因表达数据"},
					{Label: "差异丰度分析", Value: "differential_abundance", Description: "适用于微生物或其他特征丰度数据"},
					{Label: "差异代谢物分析", Value: "differential_metabolites", Description: "适用于代谢组数据"},
				},
				AllowCustom: true,
			},
		},
	}
	return rt.AskUser(ctx, input)
}

// requestDemoPermission 构造一个 write 操作并通过 Runtime 请求权限。
func requestDemoPermission(ctx context.Context, rt agentinterfaces.Runtime, req agenttypes.Request) (agenttypes.PermissionDecision, error) {
	op := agenttypes.Operation{
		Type:    agenttypes.OperationWrite,
		Path:    filepath.Join(req.WorkingDir, "demo.txt"),
		Content: "[mock] demo write content",
	}

	decision, err := rt.RequestPermission(ctx, op)
	if err != nil {
		return "", err
	}
	if decision == agenttypes.DecisionDeny {
		return decision, fmt.Errorf("%w: write %s", agenttypes.ErrPermissionDenied, op.Path)
	}
	return decision, nil
}

// lastUserPrompt 提取最后一条 user 消息作为提示词；无 user 消息时回退到系统提示。
func lastUserPrompt(req agenttypes.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == agenttypes.RoleUser {
			return req.Messages[i].Content
		}
	}
	return req.SystemPrompt
}

// demoToolCall 判断本次请求是否触发 tool-call 演示。
func demoToolCall(req agenttypes.Request) bool {
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
func (a *mockAgent) runMockToolCall(ctx context.Context, req agenttypes.Request, rt agentinterfaces.Runtime) string {
	name := strings.TrimSpace(req.Env["MOCK_TOOL_NAME"])
	if name == "" {
		name = "echo"
	}

	reg := a.opts.Tools
	if reg == nil {
		reg = defaultMockTools()
	}

	runner := agentruntime.NewToolRunner(tool.NewExecutor(reg), rt)
	res := runner.Run(ctx, agenttypes.ToolCall{
		ID:        "mock_call_1",
		Name:      name,
		Arguments: map[string]any{"text": lastUserPrompt(req)},
	})
	return fmt.Sprintf("[tool=%s] %s", name, res.Content)
}
