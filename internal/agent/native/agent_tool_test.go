package native

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	agentservice "github.com/biox-dev/gobrave/internal/agent/application/service"
	"github.com/biox-dev/gobrave/internal/agent/llm/provider"
	"github.com/biox-dev/gobrave/internal/agent/tool"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
)

// fakeClient 是 provider.Client 的测试实现：按预置响应逐轮返回 complete 事件。
type fakeClient struct {
	responses   []*provider.Response
	seenMsgs    [][]provider.Message
	seenTools   [][]provider.Tool
	streamCalls int
}

func (f *fakeClient) Stream(_ context.Context, messages []provider.Message, tools []provider.Tool) <-chan provider.Event {
	f.streamCalls++
	f.seenMsgs = append(f.seenMsgs, append([]provider.Message(nil), messages...))
	f.seenTools = append(f.seenTools, append([]provider.Tool(nil), tools...))

	ch := make(chan provider.Event, 1)
	response := &provider.Response{Content: "noop"}
	if f.streamCalls <= len(f.responses) {
		response = f.responses[f.streamCalls-1]
	}
	ch <- provider.Event{Type: provider.EventComplete, Response: response}
	close(ch)
	return ch
}

// stubTool 是仅用于验证子 Agent 工具集合的最小工具。
type stubTool struct{ name string }

func (s stubTool) Definition() tool.Definition {
	return tool.Definition{
		Name:        s.name,
		Description: "stub",
		InputSchema: tool.Schema("stub", map[string]any{}),
	}
}

func (s stubTool) Execute(context.Context, json.RawMessage) (any, error) { return "stub", nil }

func TestNewAgentToolRunsNestedAgent(t *testing.T) {
	client := &fakeClient{responses: []*provider.Response{{Content: "found config in internal/config"}}}
	created := 0

	agentTool := NewAgentTool(AgentToolConfig{
		NewClient: func(context.Context) (provider.Client, error) {
			created++
			return client, nil
		},
		Tools:        tool.NewRegistryWith(stubTool{name: "probe"}),
		SystemPrompt: "test task prompt",
	})

	out, err := agentTool.Execute(context.Background(), json.RawMessage(`{"prompt":"where is config?"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if out != "found config in internal/config" {
		t.Fatalf("Execute() = %v, want nested agent content", out)
	}
	if created != 1 {
		t.Fatalf("NewClient called %d times, want 1", created)
	}
	if client.streamCalls != 1 {
		t.Fatalf("Stream called %d times, want 1", client.streamCalls)
	}
	if got := client.seenMsgs[0]; len(got) != 1 || got[0].Role != provider.RoleUser || got[0].Content != "where is config?" {
		t.Fatalf("nested messages = %+v, want single user message", got)
	}
	// 子 Agent 只能拿到注入的只读工具集合，不能看到父 Agent 的完整工具表。
	if got := client.seenTools[0]; len(got) != 1 || got[0].Name != "probe" {
		t.Fatalf("nested tools = %+v, want only probe", got)
	}
}

func TestAgentToolRequiresPrompt(t *testing.T) {
	called := false
	agentTool := NewAgentTool(AgentToolConfig{
		NewClient: func(context.Context) (provider.Client, error) {
			called = true
			return &fakeClient{}, nil
		},
	})
	for _, args := range []string{`{}`, `{"prompt":"   "}`} {
		if _, err := agentTool.Execute(context.Background(), json.RawMessage(args)); err == nil {
			t.Fatalf("Execute(%s) expected error", args)
		}
	}
	if called {
		t.Fatal("NewClient must not be called when prompt is empty")
	}
}

func TestAgentToolRejectsEmptyResponse(t *testing.T) {
	agentTool := NewAgentTool(AgentToolConfig{
		NewClient: func(context.Context) (provider.Client, error) {
			return &fakeClient{responses: []*provider.Response{{Content: "   "}}}, nil
		},
	})
	if _, err := agentTool.Execute(context.Background(), json.RawMessage(`{"prompt":"hi"}`)); err == nil {
		t.Fatal("expected error for empty nested response")
	}
}

func TestTaskAgentToolsAreReadOnly(t *testing.T) {
	names := make(map[string]bool)
	for _, item := range TaskAgentTools() {
		names[item.Definition().Name] = true
	}
	for _, want := range []string{"glob", "grep", "ls", "view", "sourcegraph"} {
		if !names[want] {
			t.Fatalf("TaskAgentTools() missing %q: %v", want, names)
		}
	}
	for _, forbidden := range []string{"bash", "edit", "write", "patch", AgentToolName} {
		if names[forbidden] {
			t.Fatalf("TaskAgentTools() must not expose %q", forbidden)
		}
	}
}

// concurrencyProbe 记录并发峰值，用于证明多个子 Agent 确实同时在跑。
type concurrencyProbe struct {
	mu      sync.Mutex
	current int
	max     int
}

func (p *concurrencyProbe) enter() {
	p.mu.Lock()
	p.current++
	if p.current > p.max {
		p.max = p.current
	}
	p.mu.Unlock()
}

func (p *concurrencyProbe) leave() {
	p.mu.Lock()
	p.current--
	p.mu.Unlock()
}

func (p *concurrencyProbe) peak() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.max
}

// barrierClient 会在 Stream 开始时登记，并阻塞到 release 关闭后才返回回答；
// 只有真正并发执行时，多个 Stream 才能同时到达 barrier。
type barrierClient struct {
	probe   *concurrencyProbe
	started *sync.WaitGroup
	release <-chan struct{}
}

func (b *barrierClient) Stream(ctx context.Context, messages []provider.Message, _ []provider.Tool) <-chan provider.Event {
	b.probe.enter()
	defer b.probe.leave()

	b.started.Done()
	select {
	case <-b.release:
	case <-ctx.Done():
	}

	answer := "answer"
	if len(messages) > 0 {
		answer += ":" + messages[len(messages)-1].Content
	}
	ch := make(chan provider.Event, 1)
	ch <- provider.Event{Type: provider.EventComplete, Response: &provider.Response{Content: answer}}
	close(ch)
	return ch
}

func TestAgentToolRunsBatchConcurrently(t *testing.T) {
	probe := &concurrencyProbe{}
	release := make(chan struct{})
	var started sync.WaitGroup
	started.Add(2)

	var closed sync.Once
	closeRelease := func() { closed.Do(func() { close(release) }) }
	// 两个子 Agent 都进入 Stream 后才放行；若退化为串行执行，由 watchdog 兜底避免测试挂死。
	go func() {
		started.Wait()
		closeRelease()
	}()
	watchdog := time.AfterFunc(2*time.Second, closeRelease)
	defer watchdog.Stop()

	agentTool := NewAgentTool(AgentToolConfig{
		NewClient: func(context.Context) (provider.Client, error) {
			return &barrierClient{probe: probe, started: &started, release: release}, nil
		},
	})

	out, err := agentTool.Execute(context.Background(), json.RawMessage(
		`{"prompts":[{"name":"alpha","prompt":"task alpha"},{"name":"beta","prompt":"task beta"}]}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	text, ok := out.(string)
	if !ok {
		t.Fatalf("Execute() = %T, want string", out)
	}
	if probe.peak() < 2 {
		t.Fatalf("peak concurrency = %d, want >= 2", probe.peak())
	}
	alphaIdx := strings.Index(text, "## alpha")
	betaIdx := strings.Index(text, "## beta")
	if alphaIdx < 0 || betaIdx < 0 || alphaIdx > betaIdx {
		t.Fatalf("batch result not merged in requested order:\n%s", text)
	}
	if !strings.Contains(text, "answer:task alpha") || !strings.Contains(text, "answer:task beta") {
		t.Fatalf("batch result missing sub-agent answers:\n%s", text)
	}
}

// promptErrorClient 对指定 prompt 返回错误事件，其余 prompt 正常回答。
type promptErrorClient struct{ failOn string }

func (c *promptErrorClient) Stream(_ context.Context, messages []provider.Message, _ []provider.Tool) <-chan provider.Event {
	ch := make(chan provider.Event, 1)
	prompt := ""
	if len(messages) > 0 {
		prompt = messages[len(messages)-1].Content
	}
	if prompt == c.failOn {
		ch <- provider.Event{Type: provider.EventError, Error: errors.New("sub-agent failed")}
	} else {
		ch <- provider.Event{Type: provider.EventComplete, Response: &provider.Response{Content: "ok:" + prompt}}
	}
	close(ch)
	return ch
}

func TestAgentToolBatchKeepsGoingAfterFailure(t *testing.T) {
	agentTool := NewAgentTool(AgentToolConfig{
		NewClient: func(context.Context) (provider.Client, error) {
			return &promptErrorClient{failOn: "b"}, nil
		},
	})

	out, err := agentTool.Execute(context.Background(), json.RawMessage(
		`{"prompts":[{"name":"good","prompt":"a"},{"name":"bad","prompt":"b"}],"max_concurrency":2}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	text, _ := out.(string)
	if !strings.Contains(text, "## good") || !strings.Contains(text, "ok:a") {
		t.Fatalf("missing successful sub-agent result:\n%s", text)
	}
	if !strings.Contains(text, "## bad") || !strings.Contains(text, "[error]") || !strings.Contains(text, "sub-agent failed") {
		t.Fatalf("missing failed sub-agent error:\n%s", text)
	}
}

func TestAgentToolRejectsPromptAndPromptsTogether(t *testing.T) {
	agentTool := NewAgentTool(AgentToolConfig{
		NewClient: func(context.Context) (provider.Client, error) {
			t.Fatal("NewClient must not be called for invalid params")
			return nil, nil
		},
	})
	_, err := agentTool.Execute(context.Background(), json.RawMessage(
		`{"prompt":"a","prompts":[{"prompt":"b"}]}`))
	if err == nil {
		t.Fatal("expected error when both prompt and prompts are provided")
	}
}

// eventClient 会先发 thinking / content 增量，再发 complete，用于覆盖 reasoning 落库。
type eventClient struct{ content string }

func (c *eventClient) Stream(context.Context, []provider.Message, []provider.Tool) <-chan provider.Event {
	ch := make(chan provider.Event, 3)
	ch <- provider.Event{Type: provider.EventThinkingDelta, Thinking: "先查工具注册"}
	ch <- provider.Event{Type: provider.EventContentDelta, Content: c.content}
	ch <- provider.Event{Type: provider.EventComplete, Response: &provider.Response{Content: c.content}}
	close(ch)
	return ch
}

type fakeSubAgentMessage struct {
	sessionID int64
	role      string
	kind      string
	content   string
}

// fakeSubAgentStore 记录子会话 / 消息写入，用于断言落库行为。
type fakeSubAgentStore struct {
	mu       sync.Mutex
	created  []SubAgentSessionInfo
	messages []fakeSubAgentMessage
}

func (s *fakeSubAgentStore) CreateChildSession(_ context.Context, info SubAgentSessionInfo) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created = append(s.created, info)
	return 999, nil
}

func (s *fakeSubAgentStore) AppendUserMessage(_ context.Context, sessionID int64, content string) error {
	return s.add(sessionID, "user", "user", content)
}

func (s *fakeSubAgentStore) AppendEvent(_ context.Context, sessionID int64, event agenttypes.StreamEvent) error {
	params, ok := agentservice.ProjectTimelineEvent(sessionID, 0, event)
	if !ok {
		return nil
	}
	return s.add(sessionID, params.Role, params.Kind, params.Content)
}

func (s *fakeSubAgentStore) AppendFinalMessage(_ context.Context, sessionID int64, content string) error {
	return s.add(sessionID, "assistant", "assistant_final", content)
}

func (s *fakeSubAgentStore) add(sessionID int64, role, kind, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messages = append(s.messages, fakeSubAgentMessage{sessionID: sessionID, role: role, kind: kind, content: content})
	return nil
}

func TestAgentToolPersistsChildSessionAndMessages(t *testing.T) {
	store := &fakeSubAgentStore{}
	agentTool := NewAgentTool(AgentToolConfig{
		NewClient: func(context.Context) (provider.Client, error) {
			return &eventClient{content: "结论：工具在 registry.go 注册"}, nil
		},
		SessionStore: store,
		Provider:     "custom",
		Model:        "test-model",
	})

	ctx := tool.WithRuntimeContext(context.Background(), &tool.RuntimeContext{SessionID: 42, UserID: 7})
	out, err := agentTool.Execute(ctx, json.RawMessage(`{"prompt":"查工具注册位置"}`))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if out != "结论：工具在 registry.go 注册" {
		t.Fatalf("Execute() = %v", out)
	}

	if len(store.created) != 1 {
		t.Fatalf("child sessions created = %d, want 1", len(store.created))
	}
	info := store.created[0]
	if info.ParentSessionID != 42 || info.UserID != 7 || info.Provider != "custom" || info.Model != "test-model" {
		t.Fatalf("unexpected child session info: %+v", info)
	}
	if info.Title != "查工具注册位置" {
		t.Fatalf("child session title = %q", info.Title)
	}

	kinds := make(map[string]bool)
	for _, m := range store.messages {
		if m.sessionID != 999 {
			t.Fatalf("message written to session %d, want 999", m.sessionID)
		}
		kinds[m.kind] = true
	}
	for _, want := range []string{"user", "reasoning", "assistant_final"} {
		if !kinds[want] {
			t.Fatalf("missing %q message: %+v", want, store.messages)
		}
	}
}

func TestAgentToolSkipsPersistenceWithoutParentSession(t *testing.T) {
	store := &fakeSubAgentStore{}
	agentTool := NewAgentTool(AgentToolConfig{
		NewClient: func(context.Context) (provider.Client, error) {
			return &fakeClient{responses: []*provider.Response{{Content: "ok"}}}, nil
		},
		SessionStore: store,
	})

	if _, err := agentTool.Execute(context.Background(), json.RawMessage(`{"prompt":"hi"}`)); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if len(store.created) != 0 || len(store.messages) != 0 {
		t.Fatalf("expected no persistence without parent session: created=%d messages=%d", len(store.created), len(store.messages))
	}
}
