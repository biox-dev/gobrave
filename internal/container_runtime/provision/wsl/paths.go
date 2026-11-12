package wsl

import (
	"strings"
)

// toDaemonPath 把 Windows 宿主路径转成发行版内可用的 POSIX 路径。
//
// 发行版内 bind mount 的 Source 必须由 dockerd（运行在 WSL 里）解析，
// 而 WSL2 默认把 Windows 盘符挂在 /mnt/<drive> 下：
//
//	D:\a\b      → /mnt/d/a/b
//	C:\Users\x  → /mnt/c/Users/x
//
// 已经是 POSIX 路径时原样返回（幂等），保证 Linux 语义零变化。
func toDaemonPath(hostPath string) string {
	p := strings.TrimSpace(hostPath)
	if p == "" {
		return ""
	}
	if len(p) >= 2 && p[1] == ':' && isAlpha(p[0]) {
		drive := strings.ToLower(string(p[0]))
		rest := strings.TrimLeft(strings.ReplaceAll(p[2:], `\`, "/"), "/")
		if rest == "" {
			return "/mnt/" + drive
		}
		return "/mnt/" + drive + "/" + rest
	}
	return strings.ReplaceAll(p, `\`, "/")
}

func isAlpha(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}
