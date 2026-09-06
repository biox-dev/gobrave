package agent

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/biox-dev/gobrave/internal/agent/skill"
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

// ModelProviderConfig 描述一个「模型提供商」的连接配置（base_url / api_key / 类型等）。
//
// 与 config.ModelProviderConfig 一一对应，由容器启动时从配置映射而来。Provider 在每次
// 调用时按 Profile 指定的模型名（req.Model）从此表解析出本次调用所需的 LLM 配置。
type ModelProviderConfig struct {
	Model       string            `json:"model"`
	BaseURL     string            `json:"base_url"`
	APIKey      string            `json:"api_key"`
	BearerToken string            `json:"bearer_token"`
	WorkingDir  string            `json:"working_dir"`
	Extra       map[string]string `json:"extra"`
}

// Options 是构建 Agent 实例所需的通用配置；具体 Provider 按需读取。
//
// 模型相关的连接配置（base_url / api_key / 类型等）不再作为单值字段存在，而是由
// Providers（模型名 → 配置）承载：一次请求使用的模型由 Profile.Model 决定，Provider
// 据此按模型名解析出本次调用所需的 LLM 配置。
type Options struct {
	WorkingDir string            `json:"working_dir"`
	Extra      map[string]string `json:"extra"`

	// Providers 是模型提供商配置表（key 为模型名，与 Profile.Model 对应）。
	//
	// Provider 据此按请求解析出的模型名查找 base_url / api_key / 类型等 LLM 配置，
	// 用于构建 session / provider 配置。为 nil 表示无自定义模型提供商。
	Providers map[string]ModelProviderConfig `json:"-"`

	// Tools 是本次调用可用的工具注册表。
	//
	// Provider 据此向模型暴露工具定义（tool.List()），并通过 ToolRunner /
	// tool.Executor 执行模型发起的工具调用。为 nil 表示本次调用不启用工具。
	Tools *tool.Registry `json:"-"`

	// Skills 是本次调用可用的技能注册表。
	//
	// Provider 据此向模型暴露技能定义（skill.List()）与指令正文（skill.Instructions()），
	// 并通过 SkillRunner / skill.Invoker 执行模型发起的技能调用。为 nil 表示本次
	// 调用不启用技能。
	Skills *skill.Registry `json:"-"`
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
