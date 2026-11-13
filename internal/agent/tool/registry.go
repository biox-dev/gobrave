package tool

import (
	"sort"
	"strings"
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
//
// 进程内单例通常由装配方构造一次（见 container.buildAgentTools），再注册全部工具
// 后交给 Provider 与查询接口共享。
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

// Definition 按名称返回工具的定义（含 Permission 元信息）。
//
// 供上层在执行前读取工具声明的授权动作（如构建权限解析器）使用。
func (r *Registry) Definition(name string) (Definition, bool) {
	t, ok := r.Get(name)
	if !ok {
		return Definition{}, false
	}
	return t.Definition(), true
}

// List 返回全部工具定义（按名称升序），供暴露给模型。
func (r *Registry) List() []Definition {
	tools := r.Tools()
	out := make([]Definition, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Definition())
	}
	return out
}

// Tools 返回全部工具实例（按名称升序）。
//
// 供需要重新组合工具集合的调用方使用（如 Provider 在内置工具之外追加 ask_user）。
func (r *Registry) Tools() []Tool {
	r.mu.RLock()
	tools := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		tools = append(tools, t)
	}
	r.mu.RUnlock()

	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Definition().Name < tools[j].Definition().Name
	})
	return tools
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

// Filter 按名称白名单挑选工具（按名称升序，与 Tools 一致）。
//
// names 为空表示「不限制」，返回全部工具；这是 Profile 未配置工具白名单时的语义。
// 名称前后空白会被忽略，重复名只取一次；白名单中未注册的名称静默跳过，
// 调用方如需告警可用 Has 先行校验。
func (r *Registry) Filter(names []string) []Tool {
	if len(names) == 0 {
		return r.Tools()
	}

	r.mu.RLock()
	tools := make([]Tool, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		key := strings.TrimSpace(name)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if t, ok := r.tools[key]; ok {
			tools = append(tools, t)
		}
	}
	r.mu.RUnlock()

	sort.Slice(tools, func(i, j int) bool {
		return tools[i].Definition().Name < tools[j].Definition().Name
	})
	return tools
}

// Missing 返回 names 中尚未注册的工具名（去空白、去重、保持输入顺序）。
//
// 供白名单校验使用：Profile 里写错的工具名不应让调用失败，但需要留痕。
func (r *Registry) Missing(names []string) []string {
	if len(names) == 0 {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []string
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		key := strings.TrimSpace(name)
		if key == "" {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if _, ok := r.tools[key]; !ok {
			out = append(out, key)
		}
	}
	return out
}
