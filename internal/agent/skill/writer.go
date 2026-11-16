package skill

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
)

// Writer 组合技能注册表与文件存储，实现技能的运行期写入（create_skill / edit_skill）。
//
// 磁盘上的 SKILL.md 仍是唯一真实来源：写入后立即热更新 Registry，使本次及后续
// Agent 运行都能拿到新内容；进程重启后由 Discover 重新加载。
type Writer struct {
	registry *Registry
	store    *Store
}

// NewWriter 创建技能写入器：registry 用于解析与热更新，store 用于落盘。
func NewWriter(registry *Registry, store *Store) *Writer {
	return &Writer{registry: registry, store: store}
}

// PlanCreate 实现 runtimecontext.SkillWriter：校验入参并计算新建技能的写入计划（不落盘）。
func (w *Writer) PlanCreate(in runtimecontext.SkillCreateInput) (runtimecontext.SkillWritePlan, error) {
	if !w.available() {
		return runtimecontext.SkillWritePlan{}, errors.New("skill store is not configured")
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		return runtimecontext.SkillWritePlan{}, errors.New("name is required")
	}
	if w.registry.Has(name) {
		return runtimecontext.SkillWritePlan{}, fmt.Errorf("skill %q already exists", name)
	}

	sk := &Skill{
		Name:         name,
		Description:  strings.TrimSpace(in.Description),
		Instructions: in.Instructions,
		// Path 参与校验（name 必须与目录名一致），先按目标目录设定。
		Path: w.store.SkillDir(name),
	}
	if err := sk.Validate(); err != nil {
		return runtimecontext.SkillWritePlan{}, err
	}
	path := w.store.SkillPath(name)
	if _, err := os.Stat(path); err == nil {
		return runtimecontext.SkillWritePlan{}, fmt.Errorf("skill %q already exists", name)
	}

	content, err := sk.Marshal()
	if err != nil {
		return runtimecontext.SkillWritePlan{}, fmt.Errorf("marshal skill: %w", err)
	}
	return runtimecontext.SkillWritePlan{
		Name:     name,
		FilePath: path,
		After:    string(content),
	}, nil
}

// PlanEdit 实现 runtimecontext.SkillWriter：按名称计算一次唯一文本替换的写入计划（不落盘）。
//
// 与 coder 的 edit 工具语义一致：old_string 必须在文件中唯一出现；替换结果会重新解析
// 并校验（frontmatter 若被改动仍然要合法），校验通过后才进入计划。
func (w *Writer) PlanEdit(name, oldString, newString string) (runtimecontext.SkillWritePlan, error) {
	if !w.available() {
		return runtimecontext.SkillWritePlan{}, errors.New("skill store is not configured")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return runtimecontext.SkillWritePlan{}, errors.New("name is required")
	}
	if oldString == "" {
		return runtimecontext.SkillWritePlan{}, errors.New("old_string is required")
	}

	existing, ok := w.registry.Get(name)
	if !ok {
		return runtimecontext.SkillWritePlan{}, fmt.Errorf("skill %q not found", name)
	}
	// 只允许修改用户技能存储目录内的技能，避免改坏内置源码 / 外部目录里的技能。
	if !w.store.Contains(existing.SkillFilePath) {
		return runtimecontext.SkillWritePlan{}, fmt.Errorf("skill %q is not user-managed and cannot be edited", name)
	}

	content, err := os.ReadFile(existing.SkillFilePath)
	if err != nil {
		return runtimecontext.SkillWritePlan{}, fmt.Errorf("failed to read skill file: %w", err)
	}
	oldContent := string(content)

	index := strings.Index(oldContent, oldString)
	if index == -1 {
		return runtimecontext.SkillWritePlan{}, errors.New("old_string not found in skill file. Make sure it matches exactly, including whitespace and line breaks")
	}
	if last := strings.LastIndex(oldContent, oldString); last != index {
		return runtimecontext.SkillWritePlan{}, errors.New("old_string appears multiple times in the skill file. Please provide more context to ensure a unique match")
	}

	newContent := oldContent[:index] + newString + oldContent[index+len(oldString):]
	if newContent == oldContent {
		return runtimecontext.SkillWritePlan{}, errors.New("new content is the same as old content. No changes made")
	}

	// 先校验替换后的内容，避免把技能改坏后再落盘。
	updated, err := ParseContent([]byte(newContent))
	if err != nil {
		return runtimecontext.SkillWritePlan{}, fmt.Errorf("edited skill is invalid: %w", err)
	}
	updated.Path = existing.Path
	updated.SkillFilePath = existing.SkillFilePath
	if err := updated.Validate(); err != nil {
		return runtimecontext.SkillWritePlan{}, fmt.Errorf("edited skill is invalid: %w", err)
	}

	return runtimecontext.SkillWritePlan{
		Name:     name,
		FilePath: existing.SkillFilePath,
		Before:   oldContent,
		After:    newContent,
	}, nil
}

// Apply 实现 runtimecontext.SkillWriter：把计划落盘为 SKILL.md 并热更新注册表。
//
// 写入前再次核对磁盘现状（预览后文件若被改动则拒绝，避免覆盖他人修改），并直接写回
// 原文（不经 Marshal），保留 frontmatter 字段顺序与未知字段。
func (w *Writer) Apply(plan runtimecontext.SkillWritePlan) (runtimecontext.SkillInfo, error) {
	if !w.available() {
		return runtimecontext.SkillInfo{}, errors.New("skill store is not configured")
	}
	name := strings.TrimSpace(plan.Name)
	if !isSafeSkillName(name) {
		return runtimecontext.SkillInfo{}, fmt.Errorf("invalid skill name %q", plan.Name)
	}
	if strings.TrimSpace(plan.After) == "" {
		return runtimecontext.SkillInfo{}, errors.New("skill content is empty")
	}

	path := w.store.SkillPath(name)
	if current, err := os.ReadFile(path); err == nil && string(current) != plan.Before {
		return runtimecontext.SkillInfo{}, errors.New("skill file changed since the diff was prepared; please retry")
	} else if err != nil && !os.IsNotExist(err) {
		return runtimecontext.SkillInfo{}, fmt.Errorf("failed to read skill file: %w", err)
	}

	updated, err := ParseContent([]byte(plan.After))
	if err != nil {
		return runtimecontext.SkillInfo{}, fmt.Errorf("invalid skill content: %w", err)
	}
	updated.Path = w.store.SkillDir(name)
	updated.SkillFilePath = path
	if err := updated.Validate(); err != nil {
		return runtimecontext.SkillInfo{}, fmt.Errorf("invalid skill content: %w", err)
	}

	if err := os.MkdirAll(w.store.SkillDir(name), 0o755); err != nil {
		return runtimecontext.SkillInfo{}, fmt.Errorf("create skill directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(plan.After), 0o644); err != nil {
		return runtimecontext.SkillInfo{}, fmt.Errorf("write skill file: %w", err)
	}

	w.registry.Register(updated)
	return updated.SkillInfo(), nil
}

// available 报告写入器是否已装配可用的注册表与存储目录。
func (w *Writer) available() bool {
	return w != nil && w.registry != nil && w.store != nil && w.store.Available()
}
