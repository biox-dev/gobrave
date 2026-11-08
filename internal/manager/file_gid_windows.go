//go:build windows

package manager

import "strings"

// resolvePathGID 在 Windows 上没有 POSIX GID 语义（Win32FileAttributeData 无 Gid 字段），
// 始终返回 false，调用方会回退到 GROUPID 运行时变量。
func resolvePathGID(path string) (string, bool) {
	_ = strings.TrimSpace(path)
	return "", false
}

// hostUserIdentity 在 Windows 上没有数值 UID/GID（os.Getuid/Getgid 返回 -1）。
// 返回 ok=false，避免把 "-1" 注入容器 User 字段导致创建失败。
func hostUserIdentity() (uid, gid string, ok bool) {
	return "", "", false
}
