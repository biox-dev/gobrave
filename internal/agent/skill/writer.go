package skill

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/biox-dev/gobrave/internal/agent/runtimecontext"
	yaml "github.com/goccy/go-yaml"
)

// Writer 组合技能注册表与文件存储，实现技能的运行期写入（create_skill / edit_skill）。
//
// 磁盘上的 SKILL.md 仍是唯一真实来源：写入后立即热更新 Registry，使本次及后续
// Agent 运行都能拿到新内容；进程重启后由 Discover 重新加载。
type Writer struct {
	registry *Registry
	store    *Store
	// overwriteBuiltinSkills 与 config.agent.overwrite_builtin_skills 保持一致：
	// 为 true（配置默认值）时，每次启动都会用内置版本覆盖同名 SKILL.md，
	// 因此对内置技能的修改会在系统重启后丢失，写入计划需要附带提醒。
	overwriteBuiltinSkills bool
}

// WriterOption 配置 Writer 的可选行为。
type WriterOption func(*Writer)

// WithOverwriteBuiltinSkills 声明启动时是否会覆盖已存在的内置技能文件
// （对应 config.agent.overwrite_builtin_skills）。
//
// 为 true 时，对内置技能的修改会在系统重启后被内置版本覆盖；Writer 会在写入计划
// 中附带提醒，引导用户设置 agent.overwrite_builtin_skills: false 以保留修改。
func WithOverwriteBuiltinSkills(enabled bool) WriterOption {
	return func(w *Writer) {
		if w != nil {
			w.overwriteBuiltinSkills = enabled
		}
	}
}

// NewWriter 创建技能写入器：registry 用于解析与热更新，store 用于落盘。
//
// 未显式传入 WithOverwriteBuiltinSkills 时按配置默认值（覆盖）处理，
// 使内置技能的写入提醒默认生效，避免用户误以为修改会被长期保留。
func NewWriter(registry *Registry, store *Store, opts ...WriterOption) *Writer {
	w := &Writer{registry: registry, store: store, overwriteBuiltinSkills: true}
	for _, opt := range opts {
		if opt != nil {
			opt(w)
		}
	}
	return w
}

// overwriteNotice 返回本次写入需要提醒用户的事项；无需提醒时返回空串。
//
// 内置技能在 overwrite_builtin_skills=true（默认）下会在系统重启时被内置版本覆盖，
// 因此这里提示用户如何保留自己的修改。
func (w *Writer) overwriteNotice(name string) string {
	if w == nil || !w.overwriteBuiltinSkills || !IsBuiltin(name) {
		return ""
	}
	return fmt.Sprintf("skill %q is a builtin skill: this change will be overwritten by the builtin version when the system restarts. To keep it, set agent.overwrite_builtin_skills: false in config.yml (default is true).", name)
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
		// 与内置技能同名的新建会在下次启动被内置版本覆盖，提前提醒用户。
		Notice: w.overwriteNotice(name),
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
		// 内置技能（位于用户技能目录内，可编辑）在重启后会被内置版本覆盖。
		Notice: w.overwriteNotice(name),
	}, nil
}

// PlanUpdate 实现 runtimecontext.SkillWriter：按结构化字段（description / instructions）
// 计算写入计划（不落盘）。
//
// 与 PlanEdit 互补：old_string 唯一匹配要求模型先把原文逐字拷贝出来，对长技能或含
// {{ var }} 的模板技能很容易失配；这里直接给新值即可（空串字段表示保持不变）。
//
// 写入时尽量保留原文：只改 instructions 时原样保留 frontmatter；改 description 时用
// 有序映射重写 frontmatter，未知字段与字段顺序都不会丢失。
func (w *Writer) PlanUpdate(name string, in runtimecontext.SkillUpdateInput) (runtimecontext.SkillWritePlan, error) {
	if !w.available() {
		return runtimecontext.SkillWritePlan{}, errors.New("skill store is not configured")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return runtimecontext.SkillWritePlan{}, errors.New("name is required")
	}
	description := strings.TrimSpace(in.Description)
	instructions := strings.TrimSpace(in.Instructions)
	if description == "" && instructions == "" {
		return runtimecontext.SkillWritePlan{}, errors.New("description or instructions is required")
	}

	existing, ok := w.registry.Get(name)
	if !ok {
		return runtimecontext.SkillWritePlan{}, fmt.Errorf("skill %q not found", name)
	}
	// 与 PlanEdit 一致：只允许修改用户技能存储目录内的技能。
	if !w.store.Contains(existing.SkillFilePath) {
		return runtimecontext.SkillWritePlan{}, fmt.Errorf("skill %q is not user-managed and cannot be edited", name)
	}

	raw, err := os.ReadFile(existing.SkillFilePath)
	if err != nil {
		return runtimecontext.SkillWritePlan{}, fmt.Errorf("failed to read skill file: %w", err)
	}
	oldContent := string(raw)

	descChanged := description != "" && description != existing.Description
	instrChanged := instructions != "" && instructions != strings.TrimSpace(existing.Instructions)
	if !descChanged && !instrChanged {
		return runtimecontext.SkillWritePlan{}, errors.New("new content is the same as old content. No changes made")
	}

	frontmatter, body, err := splitFrontmatter(oldContent)
	if err != nil {
		return runtimecontext.SkillWritePlan{}, fmt.Errorf("edited skill is invalid: %w", err)
	}
	if descChanged {
		frontmatter, err = setFrontmatterField(frontmatter, "description", description)
		if err != nil {
			return runtimecontext.SkillWritePlan{}, fmt.Errorf("edited skill is invalid: %w", err)
		}
	}
	if instrChanged {
		body = instructions
	}
	newContent := composeSkillSource(frontmatter, body)

	// 与 PlanEdit 一致：替换后的内容必须能被重新解析并校验通过。
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
		// 内置技能（位于用户技能目录内，可编辑）在重启后会被内置版本覆盖。
		Notice: w.overwriteNotice(name),
	}, nil
}

// setFrontmatterField 在 frontmatter 文本中设置一个顶层字段，保留其它字段与顺序。
//
// 用有序映射（yaml.MapSlice）而不是重新 Marshal Skill 结构体：后者会丢掉规范之外的
// 未知字段（如 license / allowed-tools / metadata），造成用户数据丢失。
func setFrontmatterField(frontmatter, key, value string) (string, error) {
	var doc yaml.MapSlice
	if err := yaml.Unmarshal([]byte(frontmatter), &doc); err != nil {
		return "", fmt.Errorf("parsing frontmatter: %w", err)
	}
	replaced := false
	for i := range doc {
		if k, ok := doc[i].Key.(string); ok && k == key {
			doc[i].Value = value
			replaced = true
			break
		}
	}
	if !replaced {
		doc = append(doc, yaml.MapItem{Key: key, Value: value})
	}
	out, err := yaml.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("marshal frontmatter: %w", err)
	}
	return string(out), nil
}

// composeSkillSource 把 frontmatter 与正文拼回 SKILL.md 文本（排版与 Skill.Marshal 一致）。
func composeSkillSource(frontmatter, body string) string {
	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(strings.TrimRight(frontmatter, "\n"))
	sb.WriteString("\n---\n")
	if body = strings.TrimSpace(body); body != "" {
		sb.WriteString("\n")
		sb.WriteString(body)
		sb.WriteString("\n")
	}
	return sb.String()
}

// Apply 实现 runtimecontext.SkillWriter：把计划落盘为 SKILL.md 并热更新注册表。
//
// 写入前再次核对磁盘现状（预览后文件若被改动则拒绝，避免覆盖他人修改），并直接写回
// 计划中的原文（不经 Marshal），保留 frontmatter 字段顺序与未知字段。
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
