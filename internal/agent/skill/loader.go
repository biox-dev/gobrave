package skill

import (
	"os"
	"path/filepath"
	"strings"
)

// Loader 从文件系统加载技能（SKILL.md）。
//
// 布局约定：
//   - 一个技能目录 = 一个技能；目录内 SKILL.md 描述该技能；
//   - 也支持直接加载单个 SKILL.md 文件。
//
// SKILL.md 采用 frontmatter + 正文：
//
//	---
//	name: my-skill
//	description: 技能描述
//	version: 1.0.0
//	---
//	# 指令正文
//	...
//
// frontmatter 只解析 name / description / version 三个字段，其余内容作为
// 指令正文（Instructions）原样保留。
type Loader struct{}

// NewLoader 创建加载器。
func NewLoader() *Loader { return &Loader{} }

// Load 加载单个 SKILL.md 文件为技能。
//
// 若 frontmatter 未声明 name，则回退为 SKILL.md 所在目录名。
func (l *Loader) Load(path string) (Skill, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	m, err := ParseManifest(string(b))
	if err != nil {
		return nil, err
	}
	if m.Name == "" {
		m.Name = filepath.Base(filepath.Dir(path))
	}
	return NewStatic(m), nil
}

// LoadDir 递归加载目录下所有 SKILL.md（每个技能目录一个），返回全部技能。
func (l *Loader) LoadDir(dir string) ([]Skill, error) {
	var out []Skill
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(d.Name(), "SKILL.md") {
			s, lerr := l.Load(p)
			if lerr != nil {
				return lerr
			}
			out = append(out, s)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ParseManifest 解析 SKILL.md 内容（frontmatter + 正文）为 Manifest。
func ParseManifest(content string) (Manifest, error) {
	name, description, version, body := parseFrontmatter(content)
	return Manifest{
		Definition: Definition{
			Name:        name,
			Description: description,
		},
		Version:      version,
		Instructions: strings.TrimSpace(body),
	}, nil
}

// parseFrontmatter 解析 "---" 包裹的 frontmatter（key: value 行）与正文。
//
// 无 frontmatter 时返回空元信息，正文为原始内容。
func parseFrontmatter(content string) (name, description, version, body string) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", "", content
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "", "", "", content
	}
	for i := 1; i < end; i++ {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case "name":
			name = strings.TrimSpace(v)
		case "description":
			description = strings.TrimSpace(v)
		case "version":
			version = strings.TrimSpace(v)
		}
	}
	body = strings.Join(lines[end+1:], "\n")
	return name, description, version, body
}
