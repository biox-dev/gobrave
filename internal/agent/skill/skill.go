// Package skill 实现 Agent Skills 开放标准（https://agentskills.io）。
//
// 一个技能就是一份带 YAML frontmatter 的 markdown 文档（SKILL.md）：frontmatter
// 声明 name / description 等元信息，正文是指令内容。技能只做「上下文注入」，不是
// 可执行工具——采用渐进式披露（progressive disclosure）：
//
//   - 仅把技能元信息（name / description / location）以 XML 注入系统提示词；
//   - 正文（Instructions）不注入，模型判断需要时按 location 自行读取 SKILL.md 文件。
//
// 设计参考 crush/internal/skills 与 opencode/internal/skills。
package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	yaml "github.com/goccy/go-yaml"
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
// 仅包含元信息（name / description / location），刻意不含指令正文——正文由模型
// 按需读取，避免一次性塞满上下文。DisableModelInvocation 的技能会被排除。
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
		fmt.Fprintf(&sb, "    <location>%s</location>\n", escape(s.SkillFilePath))
		sb.WriteString("  </skill>\n")
	}
	sb.WriteString("</available_skills>")
	return sb.String()
}

// FormatInvocation 生成把技能正文加载到上下文时的 XML 块。
func (s *Skill) FormatInvocation() string {
	var sb strings.Builder
	sb.WriteString("<loaded_skill>\n")
	fmt.Fprintf(&sb, "  <name>%s</name>\n", escape(s.Name))
	fmt.Fprintf(&sb, "  <description>%s</description>\n", escape(s.Description))
	fmt.Fprintf(&sb, "  <location>%s</location>\n", escape(s.SkillFilePath))
	sb.WriteString("  <instructions>\n")
	sb.WriteString(escape(s.Instructions))
	sb.WriteString("\n  </instructions>\n")
	sb.WriteString("</loaded_skill>")
	return sb.String()
}

// escape 转义会破坏提示词 XML 结构的字符。
func escape(s string) string {
	return promptEscaper.Replace(s)
}
