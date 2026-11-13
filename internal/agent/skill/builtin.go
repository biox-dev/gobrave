package skill

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/biox-dev/gobrave/builtin"
)

// 内置技能：源码位于仓库 builtin/skills/<name>/SKILL.md（与内置提示词同处 builtin/ 目录），
// 由 go:embed 打包进二进制；系统启动时读出并覆盖写入用户技能目录
// （$BaseDir/.skills/<name>/SKILL.md）。
//
// 新增内置技能不需要改代码：在 builtin/skills 下新建 <name>/SKILL.md 即可
// （frontmatter 的 name 必须与目录名一致）。技能名由 builtinSkillNames 在运行期
// 扫描磁盘目录与内嵌副本自动得出，因此打包成单文件二进制后同样生效。
// 注意：go:embed 会忽略以 "." 或 "_" 开头的目录，技能目录名不要使用这类前缀。
//
// 为什么用「启动时覆盖写文件」而不是内存注入：技能的加载 / 热更新 / 前端查看 / 删除
// 都只走一条路径（磁盘 + Discover），内置技能因此不需要额外的合并逻辑；同时运维可以
// 直接打开对应文件查看模型实际拿到的内容。
//
// 注意：内置技能写入后与用户技能同处一个目录，前端删除后在下次启动时会再次出现
// （config.agent.overwrite_builtin_skills=false 时，已存在的文件不会被覆盖）。
const (
	// BuiltinSkillDir 是内置技能源码目录（相对工作目录，与内置提示词一致）。
	//
	// 子目录名即技能名（必须与 SKILL.md frontmatter 的 name 一致）。
	BuiltinSkillDir = "builtin/skills"
)

// builtinSkillBaseDirs 是运行期查找内置技能源码的目录列表（按顺序，先磁盘后内嵌）。
var builtinSkillBaseDirs = []string{BuiltinSkillDir}

// SetBuiltinSkillDir 覆盖内置技能源码目录（空串表示恢复默认：相对当前工作目录）。
// 仅用于测试或自定义部署目录；非并发安全，应在服务启动前调用。
func SetBuiltinSkillDir(dir string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		builtinSkillBaseDirs = []string{BuiltinSkillDir}
		return
	}
	builtinSkillBaseDirs = []string{dir}
}

// builtinSkillNames 扫描全部内置技能名：磁盘目录 <base>/<name>/SKILL.md 与内嵌副本
// builtin.SkillFSRoot/<name>/SKILL.md，去重后按名称升序返回。
//
// 采用扫描而不是维护名字清单，新增内置技能只需新增目录，无需改代码。
func builtinSkillNames() []string {
	seen := make(map[string]struct{})

	for _, base := range builtinSkillBaseDirs {
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if _, err := os.Stat(filepath.Join(base, e.Name(), SkillFileName)); err == nil {
				seen[e.Name()] = struct{}{}
			}
		}
	}

	if entries, err := fs.ReadDir(builtin.SkillFiles, builtin.SkillFSRoot); err == nil {
		for _, e := range entries {
			if _, err := fs.Stat(builtin.SkillFiles, path.Join(builtin.SkillFSRoot, e.Name(), SkillFileName)); err == nil {
				seen[e.Name()] = struct{}{}
			}
		}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Builtin 读取全部内置技能（磁盘优先，缺失时回退到 go:embed 副本）。
//
// 读取 / 解析失败的技能会被跳过，不影响其余技能。
func Builtin() []*Skill {
	var out []*Skill
	for _, name := range builtinSkillNames() {
		if s, err := readBuiltinSkill(name); err == nil && s != nil {
			out = append(out, s)
		}
	}
	return out
}

// BuiltinNames 返回全部内置技能名（按名称升序）。
func BuiltinNames() []string {
	return builtinSkillNames()
}

// IsBuiltin 报告 name 是否为内置技能名。
func IsBuiltin(name string) bool {
	return slices.Contains(builtinSkillNames(), name)
}

// BuiltinWriteOptions 控制内置技能落盘行为。
type BuiltinWriteOptions struct {
	// Overwrite 为 true 时覆盖已存在的 SKILL.md；为 false 时仅在文件缺失时写入，
	// 已存在的文件原样保留（用户在 $BaseDir/.skills 下对内置技能的修改不会被冲掉）。
	Overwrite bool
}

// EnsureBuiltinSkills 把内置技能写入 dir（通常为 $BaseDir/.skills），每次启动都覆盖。
//
// 写入内容来自 builtin/skills 下的 SKILL.md 原文（不再重新序列化），保证磁盘与源码一致。
// dir 为空时无操作；写入失败只记录第一个错误，不影响其他技能继续写入。
func EnsureBuiltinSkills(dir string) error {
	return EnsureBuiltinSkillsWithOptions(dir, BuiltinWriteOptions{Overwrite: true})
}

// EnsureBuiltinSkillsWithOptions 按 opts 把内置技能写入 dir。
//
// Overwrite 为 false 时，目标 <dir>/<name>/SKILL.md 已存在则跳过，只补齐缺失的内置技能；
// 这对应 config.agent.overwrite_builtin_skills=false 的场景。
func EnsureBuiltinSkillsWithOptions(dir string, opts BuiltinWriteOptions) error {
	if dir == "" {
		return nil
	}
	var firstErr error
	for _, name := range builtinSkillNames() {
		if !opts.Overwrite {
			if _, err := os.Stat(filepath.Join(dir, name, SkillFileName)); err == nil {
				continue
			}
		}
		if err := writeBuiltinSkill(dir, name); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// writeBuiltinSkill 把内置技能 name 的 SKILL.md 原文写入 dir/<name>/SKILL.md。
func writeBuiltinSkill(dir, name string) error {
	content, err := readBuiltinSkillFile(name)
	if err != nil {
		return err
	}
	skillDir := filepath.Join(dir, name)
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(skillDir, SkillFileName), content, 0o644)
}

// readBuiltinSkill 读取并校验一个内置技能（补齐 Path / SkillFilePath）。
func readBuiltinSkill(name string) (*Skill, error) {
	content, err := readBuiltinSkillFile(name)
	if err != nil {
		return nil, err
	}
	s, err := ParseContent(content)
	if err != nil {
		return nil, err
	}
	s.Name = name
	s.Path = filepath.Join(BuiltinSkillDir, name)
	s.SkillFilePath = filepath.Join(s.Path, SkillFileName)
	return s, nil
}

// readBuiltinSkillFile 读取内置技能 SKILL.md 的原始内容。
//
// 读取顺序与内置提示词一致：先读磁盘 <base>/<name>/SKILL.md（便于直接改部署目录里的文件），
// 不存在时回退到编译进二进制的 builtin.SkillFiles。两处都不存在时返回错误。
func readBuiltinSkillFile(name string) ([]byte, error) {
	var lastErr error
	for _, base := range builtinSkillBaseDirs {
		filePath := filepath.Join(base, name, SkillFileName)
		data, err := os.ReadFile(filePath)
		if err == nil {
			return data, nil
		}
		if !os.IsNotExist(err) {
			return nil, err
		}
		lastErr = err
	}

	embedded, err := fs.ReadFile(builtin.SkillFiles, path.Join(builtin.SkillFSRoot, name, SkillFileName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && lastErr != nil {
			return nil, lastErr
		}
		return nil, err
	}
	return embedded, nil
}
