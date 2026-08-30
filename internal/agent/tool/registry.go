package tool

import (
	"sort"
	"sync"
)

// Registry 注册并解析工具。
//
// 工具按名称唯一；List 返回全部工具的 Definition，供 Provider 暴露给模型
// （即 function calling 里的 tools 参数）。
type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// NewRegistryWith 创建注册表并注册给定工具。
func NewRegistryWith(tools ...Tool) *Registry {
	r := NewRegistry()
	for _, t := range tools {
		r.Register(t)
	}
	return r
}

// Register 注册工具；同名覆盖。nil 或空名工具会被忽略。
func (r *Registry) Register(t Tool) {
	if t == nil {
		return
	}
	name := t.Definition().Name
	if name == "" {
		return
	}
	r.mu.Lock()
	r.tools[name] = t
	r.mu.Unlock()
}

// Get 按名称解析工具。
func (r *Registry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	t, ok := r.tools[name]
	r.mu.RUnlock()
	return t, ok
}

// Has 报告指定工具是否已注册。
func (r *Registry) Has(name string) bool {
	_, ok := r.Get(name)
	return ok
}

// List 返回全部工具定义（按名称升序），供暴露给模型。
func (r *Registry) List() []Definition {
	r.mu.RLock()
	tools := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		tools = append(tools, t)
	}
	r.mu.RUnlock()

	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Definition().Name < tools[j].Definition().Name
	})
	out := make([]Definition, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Definition())
	}
	return out
}

// Names 返回全部工具名（升序），便于调试 / 校验。
func (r *Registry) Names() []string {
	defs := r.List()
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
	}
	return names
}
