package agent

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// Provider 是 Agent 的工厂接口：根据 Options 构建一个 Agent 实例。
// 这样同一 Provider 可以有不同的配置（不同模型 / 不同 endpoint）。
type Provider interface {
	// Name 返回 Provider 唯一标识（小写，如 claude_code / codex / copilot / custom）。
	Name() string
	// New 基于 Options 构建 Agent 实例。
	New(opts Options) (Agent, error)
}

// Options 是构建 Agent 实例所需的通用配置；具体 Provider 按需读取。
// Extra 用于承载 Provider 特有配置，避免为每个 Provider 单独定义结构。
type Options struct {
	Model       string            `json:"model"`
	BaseURL     string            `json:"base_url"`
	APIKey      string            `json:"api_key"`
	BearerToken string            `json:"bearer_token"`
	WorkingDir  string            `json:"working_dir"`
	Extra       map[string]string `json:"extra"`

	// Tools 是本次调用可用的工具注册表。
	//
	// Provider 据此向模型暴露工具定义（tool.List()），并通过 ToolRunner /
	// tool.Executor 执行模型发起的工具调用。为 nil 表示本次调用不启用工具。
	Tools *tool.Registry `json:"-"`
}

// Registry 负责注册与解析 Provider。
// 容器启动时把全部 Provider 注册进来，运行时按名称解析。
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry 创建 Registry 并注册传入的 Provider。
func NewRegistry(providers ...Provider) *Registry {
	r := &Registry{providers: make(map[string]Provider)}
	for _, p := range providers {
		r.Register(p)
	}
	return r
}

// Register 注册 Provider；同名覆盖。
func (r *Registry) Register(p Provider) {
	if p == nil {
		return
	}
	name := strings.ToLower(strings.TrimSpace(p.Name()))
	r.mu.Lock()
	r.providers[name] = p
	r.mu.Unlock()
}

// Resolve 按名称解析并构建 Agent 实例。
func (r *Registry) Resolve(name string, opts Options) (Agent, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	r.mu.RLock()
	p, ok := r.providers[name]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("agent: unknown provider %q", name)
	}
	return p.New(opts)
}

// Has 报告指定名称的 Provider 是否已注册。
func (r *Registry) Has(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	r.mu.RLock()
	_, ok := r.providers[name]
	r.mu.RUnlock()
	return ok
}

// Names 返回已注册的 Provider 名称列表（升序，便于调试与校验）。
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
