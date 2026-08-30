package skill

import (
	"sort"
	"sync"
)

// Registry 注册并解析技能。
//
// 技能按名称唯一；List 返回全部技能的 Definition，供 Provider 暴露给模型
// （即 function calling 里的 skills 参数）。
type Registry struct {
	mu     sync.RWMutex
	skills map[string]Skill
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{skills: make(map[string]Skill)}
}

// NewRegistryWith 创建注册表并注册给定技能。
func NewRegistryWith(skills ...Skill) *Registry {
	r := NewRegistry()
	for _, s := range skills {
		r.Register(s)
	}
	return r
}

// Register 注册技能；同名覆盖。nil 或空名技能会被忽略。
func (r *Registry) Register(s Skill) {
	if s == nil {
		return
	}
	name := s.Definition().Name
	if name == "" {
		return
	}
	r.mu.Lock()
	r.skills[name] = s
	r.mu.Unlock()
}

// Get 按名称解析技能。
func (r *Registry) Get(name string) (Skill, bool) {
	r.mu.RLock()
	s, ok := r.skills[name]
	r.mu.RUnlock()
	return s, ok
}

// Has 报告指定技能是否已注册。
func (r *Registry) Has(name string) bool {
	_, ok := r.Get(name)
	return ok
}

// List 返回全部技能定义（按名称升序），供暴露给模型。
func (r *Registry) List() []Definition {
	r.mu.RLock()
	skills := make([]Skill, 0, len(r.skills))
	for _, s := range r.skills {
		skills = append(skills, s)
	}
	r.mu.RUnlock()

	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Definition().Name < skills[j].Definition().Name
	})
	out := make([]Definition, 0, len(skills))
	for _, s := range skills {
		out = append(out, s.Definition())
	}
	return out
}

// Instructions 返回全部技能的指令正文（按名称升序），用于把技能说明批量注入到
// 系统提示词 / 上下文，让模型在没有 function calling 的 Provider 上也能感知技能。
func (r *Registry) Instructions() []Manifest {
	r.mu.RLock()
	skills := make([]Skill, 0, len(r.skills))
	for _, s := range r.skills {
		skills = append(skills, s)
	}
	r.mu.RUnlock()

	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Definition().Name < skills[j].Definition().Name
	})
	out := make([]Manifest, 0, len(skills))
	for _, s := range skills {
		out = append(out, Manifest{
			Definition:   s.Definition(),
			Instructions: s.Instructions(),
		})
	}
	return out
}

// Manifests 返回全部技能的完整 Manifest（含版本号与指令正文），按名称升序。
//
// 与 Instructions 不同，这里会尽量回填 Version 字段，适合用于观测 / 查看技能详情。
func (r *Registry) Manifests() []Manifest {
	r.mu.RLock()
	skills := make([]Skill, 0, len(r.skills))
	for _, s := range r.skills {
		skills = append(skills, s)
	}
	r.mu.RUnlock()

	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Definition().Name < skills[j].Definition().Name
	})
	out := make([]Manifest, 0, len(skills))
	for _, s := range skills {
		m := Manifest{
			Definition:   s.Definition(),
			Instructions: s.Instructions(),
		}
		// 若技能暴露版本号则回填（Func / Static 均实现了 Version()）。
		if v, ok := s.(interface{ Version() string }); ok {
			m.Version = v.Version()
		}
		out = append(out, m)
	}
	return out
}

// Names 返回全部技能名（升序），便于调试 / 校验。
func (r *Registry) Names() []string {
	defs := r.List()
	names := make([]string, 0, len(defs))
	for _, d := range defs {
		names = append(names, d.Name)
	}
	return names
}
