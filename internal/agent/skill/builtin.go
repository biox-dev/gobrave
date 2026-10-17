package skill

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/builtin"
)

// 内置技能：源码位于仓库 builtin/skills/<name>/SKILL.md（与内置提示词同处 builtin/ 目录），
// 由 go:embed 打包进二进制；系统启动时读出并覆盖写入用户技能目录
// （$BaseDir/.skills/<name>/SKILL.md）。
//
// 为什么用「启动时覆盖写文件」而不是内存注入：技能的加载 / 热更新 / 前端查看 / 删除
// 都只走一条路径（磁盘 + Discover），内置技能因此不需要额外的合并逻辑；同时运维可以
// 直接打开对应文件查看模型实际拿到的内容。
//
// 注意：内置技能写入后与用户技能同处一个目录，前端删除后在下次启动时会再次出现。
const (
	// BuiltinSkillDir 是内置技能源码目录（相对工作目录，与内置提示词一致）。
	BuiltinSkillDir = "builtin/skills"
	// BuiltinAnalysisScriptSkillName 是「创建分析脚本流程」内置技能名。
	BuiltinAnalysisScriptSkillName = "analysis-script-workflow"
	// BuiltinAnalysisNodeScriptSkillName 是「分析节点脚本修改」内置技能名。
	BuiltinAnalysisNodeScriptSkillName = "analysis-node-script-editing"
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

// builtinSkillNames 是全部内置技能名（新增内置技能时在此追加）。
var builtinSkillNames = []string{
	BuiltinAnalysisScriptSkillName,
	BuiltinAnalysisNodeScriptSkillName,
}

// Builtin 读取全部内置技能（磁盘优先，缺失时回退到 go:embed 副本）。
//
// 读取 / 解析失败的技能会被跳过，不影响其余技能。
func Builtin() []*Skill {
	var out []*Skill
	for _, name := range builtinSkillNames {
		if s, err := readBuiltinSkill(name); err == nil && s != nil {
			out = append(out, s)
		}
	}
	return out
}

// BuiltinNames 返回全部内置技能名。
func BuiltinNames() []string {
	return append([]string(nil), builtinSkillNames...)
}

// IsBuiltin 报告 name 是否为内置技能名。
func IsBuiltin(name string) bool {
	for _, n := range builtinSkillNames {
		if n == name {
			return true
		}
	}
	return false
}

// EnsureBuiltinSkills 把内置技能写入 dir（通常为 $BaseDir/.skills），每次启动都覆盖。
//
// 写入内容来自 builtin/skills 下的 SKILL.md 原文（不再重新序列化），保证磁盘与源码一致。
// dir 为空时无操作；写入失败只记录第一个错误，不影响其他技能继续写入。
func EnsureBuiltinSkills(dir string) error {
	if dir == "" {
		return nil
	}
	var firstErr error
	for _, name := range builtinSkillNames {
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
