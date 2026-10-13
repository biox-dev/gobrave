package skill

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store 把技能以 SKILL.md 的形式持久化到某个根目录（通常为 $BaseDir/.skills）。
//
// 磁盘是用户技能的唯一真实来源：运行期新增 / 修改 / 删除都会落盘，进程重启后
// 由 Discover 重新加载。这样既能保证「程序运行中也能改技能」，又不会因为进程
// 重启而丢失改动。内存中的 Registry 只是磁盘内容的一份可热更新的缓存。
type Store struct {
	dir string
}

// NewStore 创建以 dir 为根目录的技能存储；dir 为空时视为未配置。
func NewStore(dir string) *Store {
	return &Store{dir: strings.TrimSpace(dir)}
}

// Dir 返回存储根目录。
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	return s.dir
}

// Available 报告存储是否已配置根目录。
func (s *Store) Available() bool {
	return s != nil && s.dir != ""
}

// SkillDir 返回名为 name 的技能目录 <dir>/<name>。
func (s *Store) SkillDir(name string) string {
	if s == nil {
		return ""
	}
	return filepath.Join(s.dir, strings.TrimSpace(name))
}

// SkillPath 返回名为 name 的技能文件路径 <dir>/<name>/SKILL.md。
func (s *Store) SkillPath(name string) string {
	return filepath.Join(s.SkillDir(name), SkillFileName)
}

// Save 把技能写入 <dir>/<name>/SKILL.md（写入前做规范校验）。name 作为目录名。
func (s *Store) Save(sk *Skill) error {
	if !s.Available() {
		return errors.New("skill store directory is not configured")
	}
	if sk == nil {
		return errors.New("skill is nil")
	}
	if err := sk.Validate(); err != nil {
		return err
	}
	if !isSafeSkillName(sk.Name) {
		return fmt.Errorf("invalid skill name %q", sk.Name)
	}

	content, err := sk.Marshal()
	if err != nil {
		return fmt.Errorf("marshal skill: %w", err)
	}

	dir := s.SkillDir(sk.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create skill directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, SkillFileName), content, 0o644); err != nil {
		return fmt.Errorf("write skill file: %w", err)
	}
	return nil
}

// Delete 删除名为 name 的技能目录；目录不存在时视为成功（幂等）。
func (s *Store) Delete(name string) error {
	if !s.Available() {
		return errors.New("skill store directory is not configured")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("name is required")
	}
	if !isSafeSkillName(name) {
		return fmt.Errorf("invalid skill name %q", name)
	}

	dir := s.SkillDir(name)
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.RemoveAll(dir)
}

// Contains 报告 path 是否位于该 Store 的根目录之下。
// 用于限制只允许删除 / 修改存储在用户技能目录内的技能，避免误删内置或外部技能。
func (s *Store) Contains(path string) bool {
	if !s.Available() || strings.TrimSpace(path) == "" {
		return false
	}
	root, err := filepath.Abs(s.dir)
	if err != nil {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

// isSafeSkillName 校验 name 可以作为单层目录名，防止目录穿越（../ 或绝对路径）。
func isSafeSkillName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if name != filepath.Base(name) {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}
