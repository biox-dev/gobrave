package client

import (
	"context"
	"errors"
	"strings"
	"sync"

	. "github.com/biox-dev/gobrave/internal/agent/types"
	. "github.com/biox-dev/gobrave/internal/agent/types/interface"
)

// ErrRuntimeRequired 表示调用方没有提供 Runtime。
//
// Client 不再内置「无任务的独立 Runtime」：Agent 调用必须由上层（AgentService 的
// taskRuntime）注入，保证事件落库、权限、提问都有明确的任务归属。
var ErrRuntimeRequired = errors.New("agent/client: runtime is required")

// Client 是 Agent 调用的统一门面（Facade）。
//
// 上层业务（AISummaryWorker 等）只依赖 Client，不直接感知具体 Provider。
// Client 负责：按请求中的 Provider（或默认 Provider）解析 Agent 实例，并转发 Invoke / Stream。
type Client struct {
	registry *Registry

	mu             sync.RWMutex
	defaultOptions Options
}

// NewClient 创建 Client。
func NewClient(registry *Registry, opts Options) *Client {
	return &Client{
		registry:       registry,
		defaultOptions: opts,
	}
}

// InvokeRuntime 使用调用方提供的 Runtime 执行一次性任务（供 AgentService 任务模式使用）。
func (c *Client) InvokeRuntime(ctx context.Context, req Request, rt Runtime) (*Result, error) {
	a, err := c.resolve(req.Provider)
	if err != nil {
		return nil, err
	}
	if rt == nil {
		return nil, ErrRuntimeRequired
	}
	return a.Invoke(ctx, req, rt)
}

// StreamRuntime 使用调用方提供的 Runtime 执行流式请求（供 AgentService 任务模式使用）。
func (c *Client) StreamRuntime(ctx context.Context, req Request, rt Runtime) (*Result, error) {
	a, err := c.resolve(req.Provider)
	if err != nil {
		return nil, err
	}
	if rt == nil {
		return nil, ErrRuntimeRequired
	}
	return a.Stream(ctx, req, rt)
}

// options 返回默认 Options 的快照。
//
// 其中的 ProviderSource 是函数而非快照，因此 Provider 在调用时能读到最新配置。
func (c *Client) options() Options {
	c.mu.RLock()
	opts := c.defaultOptions
	c.mu.RUnlock()
	return opts
}

// resolve 解析请求应使用的 Agent 实例。
//
// 模型提供商配置不在此时解析：Options 只携带 ProviderSource，由 Provider 在每次调用时
// 通过 ResolveProvider(模型名) 实时查询，因此修改 agent 段无需重启即可生效。
func (c *Client) resolve(provider string) (Agent, error) {
	name := strings.TrimSpace(provider)
	if name == "" {
		name = DefaultProvider
	}
	return c.registry.Resolve(name, c.options())
}
