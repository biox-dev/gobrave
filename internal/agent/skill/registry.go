package skill

import (
	"sort"
	"strings"
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

// PromptBlock 把技能（selected 为空表示全部）的名称 / 描述 / 指令正文拼接为一个 markdown 块，
// 供上层注入系统提示词，让模型感知可用技能及其用法（技能区别于工具的关键：携带上下文指令）。
//
// 仅渲染 selected 中命中注册表的技能；无匹配技能、或全部技能均无内容时返回空字符串。
func (r *Registry) PromptBlock(selected ...string) string {
	if r == nil {
		return ""
	}

	var manifests []Manifest
	if len(selected) == 0 {
		manifests = r.Instructions()
	} else {
		for _, name := range selected {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			if s, ok := r.Get(name); ok {
				manifests = append(manifests, Manifest{
					Definition:   s.Definition(),
					Instructions: s.Instructions(),
				})
			}
		}
		sort.Slice(manifests, func(i, j int) bool {
			return manifests[i].Name < manifests[j].Name
		})
	}

	var b strings.Builder
	b.WriteString("## Available skills\n\n")
	wrote := false
	for _, m := range manifests {
		if strings.TrimSpace(m.Name) == "" {
			continue
		}
		wrote = true
		b.WriteString("### ")
		b.WriteString(m.Name)
		if desc := strings.TrimSpace(m.Description); desc != "" {
			b.WriteString(": ")
			b.WriteString(desc)
		}
		b.WriteString("\n\n")
		if instr := strings.TrimSpace(m.Instructions); instr != "" {
			b.WriteString(instr)
			b.WriteString("\n\n")
		}
	}
	if !wrote {
		return ""
	}
	return strings.TrimSpace(b.String())
}
