// Package skill 实现 Agent Skills 开放标准（https://agentskills.io）。
//
// 一个技能就是一份带 YAML frontmatter 的 markdown 文档（SKILL.md）：frontmatter
// 声明 name / description 等元信息，正文是指令内容。技能只做「上下文注入」，不是
// 可执行工具——采用渐进式披露（progressive disclosure）：
//
//   - 仅把技能元信息（name / description）以 XML 注入系统提示词；
//   - 正文（Instructions）不注入，模型判断需要时调用 get_skill 工具加载；
//   - 技能目录下的参考文档 / 脚本 / 模板等附属资源通过 load_skill_resource 工具按需读取。
//
// 设计参考 crush/internal/skills 与 opencode/internal/skills。
package skill

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	yaml "github.com/goccy/go-yaml"

	"github.com/biox-dev/gobrave/internal/agent/tool"
)

const (
	// SkillFileName 是技能描述文件的文件名。
	SkillFileName = "SKILL.md"
	// MaxNameLength 是技能名允许的最大长度。
	MaxNameLength = 64
	// MaxDescriptionLength 是技能描述允许的最大长度。
	MaxDescriptionLength = 1024
)

var (
	namePattern   = regexp.MustCompile(`^[a-zA-Z0-9]+(-[a-zA-Z0-9]+)*$`)
	promptEscaper = strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
)

// Skill 表示一份解析后的 SKILL.md。
type Skill struct {
	Name                   string `yaml:"name" json:"name"`
	Description            string `yaml:"description" json:"description"`
	ManifestVersion        string `yaml:"version,omitempty" json:"version,omitempty"`
	UserInvocable          bool   `yaml:"user-invocable" json:"user_invocable"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation" json:"disable_model_invocation"`

	// 以下字段不参与 frontmatter 解析，由加载过程填充。
	Instructions  string `yaml:"-" json:"instructions,omitempty"`    // 指令正文
	Path          string `yaml:"-" json:"path,omitempty"`            // 技能目录
	SkillFilePath string `yaml:"-" json:"skill_file_path,omitempty"` // SKILL.md 路径
}

// DiscoveryState 表示单个技能文件的发现结果。
type DiscoveryState int

const (
	// StateNormal 表示技能解析并校验成功。
	StateNormal DiscoveryState = iota
	// StateError 表示扫描 / 解析 / 校验失败。
	StateError
)

// SkillState 描述单个技能文件的最新发现状态。
type SkillState struct {
	Name  string
	Path  string
	State DiscoveryState
	Err   error
}

// Validate 校验技能是否满足规范要求。
func (s *Skill) Validate() error {
	var errs []error

	if s.Name == "" {
		errs = append(errs, errors.New("name is required"))
	} else {
		if len(s.Name) > MaxNameLength {
			errs = append(errs, fmt.Errorf("name exceeds %d characters", MaxNameLength))
		}
		if !namePattern.MatchString(s.Name) {
			errs = append(errs, errors.New("name must be alphanumeric with hyphens, no leading/trailing/consecutive hyphens"))
		}
		if s.Path != "" && !strings.EqualFold(filepath.Base(s.Path), s.Name) {
			errs = append(errs, fmt.Errorf("name %q must match directory %q", s.Name, filepath.Base(s.Path)))
		}
	}

	if s.Description == "" {
		errs = append(errs, errors.New("description is required"))
	} else if len(s.Description) > MaxDescriptionLength {
		errs = append(errs, fmt.Errorf("description exceeds %d characters", MaxDescriptionLength))
	}

	return errors.Join(errs...)
}

// Parse 从磁盘解析一个 SKILL.md 文件。
func Parse(path string) (*Skill, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	s, err := ParseContent(content)
	if err != nil {
		return nil, err
	}

	s.Path = filepath.Dir(path)
	s.SkillFilePath = path
	return s, nil
}

// ParseContent 从原始字节解析 SKILL.md。
func ParseContent(content []byte) (*Skill, error) {
	frontmatter, body, err := splitFrontmatter(string(content))
	if err != nil {
		return nil, err
	}

	var s Skill
	if err := yaml.Unmarshal([]byte(frontmatter), &s); err != nil {
		return nil, fmt.Errorf("parsing frontmatter: %w", err)
	}
	s.Instructions = strings.TrimSpace(body)
	return &s, nil
}

// frontmatter 是技能写回 SKILL.md 时的 YAML 头部结构（控制字段顺序与省略空值）。
type frontmatter struct {
	Name                   string `yaml:"name"`
	Description            string `yaml:"description"`
	Version                string `yaml:"version,omitempty"`
	UserInvocable          bool   `yaml:"user-invocable,omitempty"`
	DisableModelInvocation bool   `yaml:"disable-model-invocation,omitempty"`
}

// Marshal 把技能序列化为 SKILL.md 内容（YAML frontmatter + 正文）。
//
// 与 Parse / ParseContent 互逆：Save 出来的文件应能被 Parse 原样读回。
func (s *Skill) Marshal() ([]byte, error) {
	head, err := yaml.Marshal(frontmatter{
		Name:                   s.Name,
		Description:            s.Description,
		Version:                s.ManifestVersion,
		UserInvocable:          s.UserInvocable,
		DisableModelInvocation: s.DisableModelInvocation,
	})
	if err != nil {
		return nil, err
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.Write(head)
	if !strings.HasSuffix(string(head), "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("---\n")
	if body := strings.TrimSpace(s.Instructions); body != "" {
		sb.WriteString("\n")
		sb.WriteString(body)
		sb.WriteString("\n")
	}
	return []byte(sb.String()), nil
}

// splitFrontmatter 从 markdown 内容中拆出 YAML frontmatter 与正文。
func splitFrontmatter(content string) (frontmatter, body string, err error) {
	// 去除编辑器可能写入的 UTF-8 BOM，并统一换行符。
	content = strings.TrimPrefix(content, "\uFEFF")
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	lines := strings.Split(content, "\n")
	start := slices.IndexFunc(lines, func(line string) bool {
		return strings.TrimSpace(line) != ""
	})
	if start == -1 || strings.TrimSpace(lines[start]) != "---" {
		return "", "", errors.New("no YAML frontmatter found")
	}

	endOffset := slices.IndexFunc(lines[start+1:], func(line string) bool {
		return strings.TrimSpace(line) == "---"
	})
	if endOffset == -1 {
		return "", "", errors.New("unclosed frontmatter")
	}
	end := start + 1 + endOffset

	frontmatter = strings.Join(lines[start+1:end], "\n")
	body = strings.Join(lines[end+1:], "\n")
	return frontmatter, body, nil
}

// Discover 发现给定路径下所有合法技能。
func Discover(paths []string) []*Skill {
	skills, _ := DiscoverWithStates(paths)
	return skills
}

// DiscoverWithStates 发现给定路径下所有合法技能，并返回每个文件的发现状态。
func DiscoverWithStates(paths []string) ([]*Skill, []*SkillState) {
	var skills []*Skill
	var states []*SkillState
	seen := map[string]bool{}

	addState := func(name, path string, state DiscoveryState, err error) {
		states = append(states, &SkillState{Name: name, Path: path, State: state, Err: err})
	}

	for _, base := range paths {
		walkErr := filepath.WalkDir(base, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				addState("", path, StateError, err)
				return nil
			}
			if d.IsDir() || !strings.EqualFold(d.Name(), SkillFileName) {
				return nil
			}
			if seen[path] {
				return nil
			}
			seen[path] = true

			s, parseErr := Parse(path)
			if parseErr != nil {
				addState("", path, StateError, parseErr)
				return nil
			}
			if validateErr := s.Validate(); validateErr != nil {
				addState(s.Name, path, StateError, validateErr)
				return nil
			}
			skills = append(skills, s)
			addState(s.Name, path, StateNormal, nil)
			return nil
		})
		if walkErr != nil && !os.IsNotExist(walkErr) {
			addState("", base, StateError, walkErr)
		}
	}

	sortSkills(skills)
	return skills, states
}

// Deduplicate 按名称去重；同名时后出现的覆盖先出现的（用户技能覆盖内置技能）。
func Deduplicate(all []*Skill) []*Skill {
	seen := make(map[string]int, len(all))
	for i, s := range all {
		seen[s.Name] = i
	}
	out := make([]*Skill, 0, len(seen))
	for i, s := range all {
		if seen[s.Name] == i {
			out = append(out, s)
		}
	}
	return out
}

// Filter 移除名称出现在 disabled 列表中的技能。
func Filter(all []*Skill, disabled []string) []*Skill {
	if len(disabled) == 0 {
		return all
	}
	disabledSet := make(map[string]bool, len(disabled))
	for _, name := range disabled {
		disabledSet[name] = true
	}
	out := make([]*Skill, 0, len(all))
	for _, s := range all {
		if !disabledSet[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

// sortSkills 按路径、再按名称（不区分大小写）稳定排序。
func sortSkills(skills []*Skill) {
	slices.SortStableFunc(skills, func(a, b *Skill) int {
		if c := strings.Compare(strings.ToLower(a.Path), strings.ToLower(b.Path)); c != 0 {
			return c
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
}

// ToPromptXML 生成注入系统提示词的 <available_skills> XML。
//
// 仅包含元信息（name / description），刻意不含指令正文与文件路径——正文与附属资源
// 都由模型通过工具（get_skill / load_skill_resource）按需加载，避免一次性塞满
// 上下文。DisableModelInvocation 的技能会被排除。
func ToPromptXML(skills []*Skill) string {
	if len(skills) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("<available_skills>\n")
	for _, s := range skills {
		if s.DisableModelInvocation {
			continue
		}
		sb.WriteString("  <skill>\n")
		fmt.Fprintf(&sb, "    <name>%s</name>\n", escape(s.Name))
		fmt.Fprintf(&sb, "    <description>%s</description>\n", escape(s.Description))
		sb.WriteString("  </skill>\n")
	}
	sb.WriteString("</available_skills>")
	return sb.String()
}

// escape 转义会破坏提示词 XML 结构的字符。
func escape(s string) string {
	return promptEscaper.Replace(s)
}

// 技能附属资源相关常量。
const (
	// ManifestFileName 是技能目录下声明版本等元信息的可选清单文件。
	ManifestFileName = "manifest.json"
	// MaxResourceBytes 是 load_skill_resource 单次返回的资源大小上限。
	MaxResourceBytes = 256 * 1024
)

// Manifest 是技能目录下 manifest.json 的结构（全部字段可选）。
//
// 现有实现只使用 version（供 get_skill 的版本选择与展示）。
type Manifest struct {
	Version string `json:"version"`
}

// ManifestPath 返回技能目录下 manifest.json 的路径。
func (s *Skill) ManifestPath() string {
	if s == nil || s.Path == "" {
		return ""
	}
	return filepath.Join(s.Path, ManifestFileName)
}

// Version 返回技能版本：优先取 manifest.json 中的 version，缺失时回退到
// SKILL.md frontmatter 的 version 字段；都没有则返回空串。
func (s *Skill) Version() string {
	if s == nil {
		return ""
	}
	if v := s.manifestVersion(); v != "" {
		return v
	}
	return strings.TrimSpace(s.ManifestVersion)
}

// manifestVersion 读取 manifest.json 中声明的版本；文件缺失或非法时返回空串。
func (s *Skill) manifestVersion() string {
	path := s.ManifestPath()
	if path == "" {
		return ""
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var m Manifest
	if err := json.Unmarshal(content, &m); err != nil {
		return ""
	}
	return strings.TrimSpace(m.Version)
}

// ListResources 递归列出技能目录下的附属资源（相对技能目录的斜杠路径，已排序）。
//
// SKILL.md 与 manifest.json 属于技能描述本身，不在资源列表内。
func (s *Skill) ListResources() []string {
	if s == nil || s.Path == "" {
		return nil
	}
	var out []string
	_ = filepath.WalkDir(s.Path, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(s.Path, path)
		if relErr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == SkillFileName || rel == ManifestFileName {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	slices.Sort(out)
	return out
}

// ResolveResource 把技能目录内的相对路径解析为绝对路径，并拒绝目录穿越。
//
// 只允许访问技能目录内部的文件；越界、绝对路径或不存在都会返回错误。
func (s *Skill) ResolveResource(rel string) (string, error) {
	if s == nil || s.Path == "" {
		return "", errors.New("skill path is not available")
	}
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", errors.New("path is required")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to the skill directory", rel)
	}

	root, err := filepath.Abs(s.Path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	if !isWithin(root, abs) {
		return "", fmt.Errorf("path %q escapes the skill directory", rel)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory, not a resource file", rel)
	}
	return abs, nil
}

// ReadResource 读取技能附属资源文件内容（大小上限 MaxResourceBytes）。
func (s *Skill) ReadResource(rel string) (string, string, error) {
	abs, err := s.ResolveResource(rel)
	if err != nil {
		return "", "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", "", err
	}
	if info.Size() > MaxResourceBytes {
		return "", "", fmt.Errorf("resource %q is too large (%d bytes, limit %d)", rel, info.Size(), MaxResourceBytes)
	}
	content, err := os.ReadFile(abs)
	if err != nil {
		return "", "", err
	}
	return string(content), abs, nil
}

// isWithin 报告 path 是否位于 root 目录之内（含 root 自身）。
func isWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// 以下方法实现 tool.SkillResource，供 get_skill / load_skill_resource 工具在
// 执行期读取技能指令与附属资源（skill 包依赖 tool 包，反向通过接口解耦）。

// SkillInfo 返回技能的最小元信息与指令正文。
func (s *Skill) SkillInfo() tool.SkillInfo {
	if s == nil {
		return tool.SkillInfo{}
	}
	return tool.SkillInfo{
		Name:         s.Name,
		Description:  s.Description,
		SkillPath:    s.Path,
		SkillFile:    s.SkillFilePath,
		Instructions: s.Instructions,
	}
}

// SkillVersion 返回技能版本（manifest.json 优先，回退 frontmatter version）。
func (s *Skill) SkillVersion() string { return s.Version() }

// SkillResources 返回技能目录下的附属资源相对路径（不含 SKILL.md / manifest.json）。
func (s *Skill) SkillResources() []string { return s.ListResources() }

// ReadSkillResource 读取技能目录内的附属资源，返回内容与绝对路径。
func (s *Skill) ReadSkillResource(path string) (string, string, error) {
	return s.ReadResource(path)
}
