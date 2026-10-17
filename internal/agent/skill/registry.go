package skill

import (
	"slices"
	"strings"
	"sync"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

// promptHeader 是注入系统提示词的技能说明前言。
const promptHeader = "# Skills\n\n" +
	"The following skills are available. When a user's request matches a skill's\n" +
	"description, call the `get_skill` tool with the skill name to load its\n" +
	"instructions and follow them before proceeding. Use `load_skill_resource`\n" +
	"to read a skill's reference docs, scripts, templates or schemas.\n\n"

// Registry 持有一组已发现的技能，提供按名解析与提示词注入。
//
// 技能按名称唯一；同名后注册者覆盖先注册者（用户技能覆盖内置技能）。
//
// Registry 支持在程序运行期热更新（Register / Unregister），因此内部用读写锁
// 保护：一次 Agent 运行读取技能元信息的同时，前端可以新增 / 修改 / 删除技能。
type Registry struct {
	mu     sync.RWMutex
	byName map[string]*Skill
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]*Skill)}
}

// NewRegistryWith 创建注册表并注册给定技能。
func NewRegistryWith(skills ...*Skill) *Registry {
	r := NewRegistry()
	for _, s := range skills {
		r.Register(s)
	}
	return r
}

// Register 注册技能；nil 或空名技能会被忽略，同名覆盖。
func (r *Registry) Register(s *Skill) {
	if r == nil || s == nil {
		return
	}
	name := strings.TrimSpace(s.Name)
	if name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byName == nil {
		r.byName = make(map[string]*Skill)
	}
	r.byName[name] = s
}

// Unregister 按名称移除技能；不存在时无操作。
func (r *Registry) Unregister(name string) {
	if r == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byName, name)
}

// Get 按名称解析技能。
func (r *Registry) Get(name string) (*Skill, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.byName[strings.TrimSpace(name)]
	return s, ok
}

// Resolver 返回实现 tool.SkillResolver 的视图，供技能工具（get_skill /
// load_skill_resource）在执行期按名称解析技能。
func (r *Registry) Resolver() tool.SkillResolver { return registryResolver{r: r} }

// registryResolver 把 *Registry 适配为 tool.SkillResolver（返回 tool.SkillResource 接口）。
type registryResolver struct{ r *Registry }

// Get 实现 tool.SkillResolver。
func (rr registryResolver) Get(name string) (tool.SkillResource, bool) {
	s, ok := rr.r.Get(name)
	if !ok || s == nil {
		return nil, false
	}
	return s, true
}

// Names 实现 tool.SkillResolver。
func (rr registryResolver) Names() []string { return rr.r.Names() }

// Has 报告指定技能是否已注册。
func (r *Registry) Has(name string) bool {
	_, ok := r.Get(name)
	return ok
}

// List 返回全部技能（按名称升序）。
func (r *Registry) List() []*Skill {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	out := make([]*Skill, 0, len(r.byName))
	for _, s := range r.byName {
		out = append(out, s)
	}
	r.mu.RUnlock()

	slices.SortFunc(out, func(a, b *Skill) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// Names 返回全部技能名（升序）。
func (r *Registry) Names() []string {
	list := r.List()
	names := make([]string, 0, len(list))
	for _, s := range list {
		names = append(names, s.Name)
	}
	return names
}

// Filter 返回仅包含指定名称（保持升序、去重）的新注册表；names 为空时返回全部技能。
func (r *Registry) Filter(names ...string) *Registry {
	return NewRegistryWith(r.selectSkills(names)...)
}

// PromptBlock 把技能元信息（selected 为空表示全部）拼接为注入系统提示词的 markdown 块。
//
// 仅注入 name / description / location，不含指令正文；无匹配技能时返回空字符串。
func (r *Registry) PromptBlock(selected ...string) string {
	if r == nil {
		return ""
	}
	xml := ToPromptXML(r.selectSkills(selected))
	if xml == "" {
		return ""
	}
	return strings.TrimSpace(promptHeader + xml)
}

// selectSkills 返回 selected 命中的技能（升序）；selected 为空时返回全部技能。
func (r *Registry) selectSkills(selected []string) []*Skill {
	if r == nil {
		return nil
	}
	if len(selected) == 0 {
		return r.List()
	}
	out := make([]*Skill, 0, len(selected))
	seen := make(map[string]bool, len(selected))
	for _, name := range selected {
		name = strings.TrimSpace(name)
		if name == "" || seen[name] {
			continue
		}
		if s, ok := r.Get(name); ok {
			out = append(out, s)
			seen[name] = true
		}
	}
	slices.SortFunc(out, func(a, b *Skill) int {
		return strings.Compare(a.Name, b.Name)
	})
	return out
}
