package agent

import (
	"context"
	"strings"
	"sync"
)

// Client 是 Agent 调用的统一门面（Facade）。
//
// 上层业务（AISummaryWorker、LLMHandler 等）只依赖 Client，不直接感知具体 Provider。
// Client 负责：按请求中的 Provider（或默认 Provider）解析 Agent 实例，并转发 Invoke / Stream。
type Client struct {
	registry *Registry

	mu              sync.RWMutex
	defaultProvider string
	defaultOptions  Options
}

// NewClient 创建 Client。
// defaultProvider 为空时使用 DefaultProvider（mock）。
func NewClient(registry *Registry, defaultProvider string, opts Options) *Client {
	if strings.TrimSpace(defaultProvider) == "" {
		defaultProvider = DefaultProvider
	}
	return &Client{
		registry:        registry,
		defaultProvider: defaultProvider,
		defaultOptions:  opts,
	}
}

// SetDefault 动态切换默认 Provider 与默认 Options（用于后续运行时切换能力）。
func (c *Client) SetDefault(provider string, opts Options) {
	c.mu.Lock()
	c.defaultProvider = provider
	c.defaultOptions = opts
	c.mu.Unlock()
}

// Invoke 执行一次性任务：解析 Provider → Agent.Invoke。
// 内部使用 standalone Runtime（无任务上下文，事件丢弃、权限宽松放行）。
// func (c *Client) Invoke(ctx context.Context, req Request) (*Result, error) {
// 	return c.InvokeRuntime(ctx, req, NewStandaloneRuntime(nil))
// }

// Stream 执行流式请求：解析 Provider → Agent.Stream。
// 内部使用 standalone Runtime，其 Emit 直接把事件交给 handler。
func (c *Client) Stream(ctx context.Context, req Request, handler StreamHandler) (*Result, error) {
	return c.StreamRuntime(ctx, req, NewStandaloneRuntime(handler))
}

// InvokeRuntime 使用调用方提供的 Runtime 执行一次性任务（供 AgentService 任务模式使用）。
func (c *Client) InvokeRuntime(ctx context.Context, req Request, rt Runtime) (*Result, error) {
	a, err := c.resolve(req.Provider)
	if err != nil {
		return nil, err
	}
	if rt == nil {
		rt = NewStandaloneRuntime(nil)
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
		rt = NewStandaloneRuntime(nil)
	}
	return a.Stream(ctx, req, rt)
}

// resolve 解析请求应使用的 Agent 实例。
func (c *Client) resolve(provider string) (Agent, error) {
	c.mu.RLock()
	def := c.defaultProvider
	opts := c.defaultOptions
	c.mu.RUnlock()

	name := strings.TrimSpace(provider)
	if name == "" {
		name = def
	}
	if name == "" {
		name = DefaultProvider
	}
	return c.registry.Resolve(name, opts)
}
