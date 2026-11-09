package containerruntime

import (
	"path/filepath"
	"strings"
)

// IsWindowsHostPath 报告 p 是否是 Windows 宿主路径（含盘符或 UNC 前缀）。
//
// 仅凭字符串判断而不看 runtime.GOOS，原因是：
//   - 路径可能来自数据库 / 配置文件 / 前端表单，由别的操作系统写入；
//   - Docker bind 语法的转义规则只取决于「宿主是 Windows 还是 Unix」，
//     而宿主形态可以从路径本身看出来。
func IsWindowsHostPath(p string) bool {
	p = strings.TrimSpace(p)
	if p == "" {
		return false
	}
	// 盘符：C:、C:\、C:/、c:relative（Docker 只对 C:\ / C:/ 生效，这里都按 Windows 处理）
	if len(p) >= 2 && p[1] == ':' && isASCIIAlpha(p[0]) {
		return true
	}
	// UNC 共享：\\server\share
	if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//") && looksLikeUNC(p) {
		return true
	}
	return false
}

func isASCIIAlpha(r byte) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func looksLikeUNC(p string) bool {
	trimmed := strings.TrimPrefix(p, "//")
	if trimmed == p {
		return false
	}
	parts := strings.SplitN(trimmed, "/", 3)
	return len(parts) >= 2 && parts[0] != "" && parts[1] != ""
}

// ToContainerPath 把宿主路径转成容器内的 POSIX 路径。
//
// 容器（linux 镜像）里没有盘符概念，所以 Windows 宿主路径需要重写成容器路径：
//   - 盘符前缀 C:\ / C:/ 去掉，反斜杠换成 /，例如
//     D:\workspace\go-project\gobrave\.gobrave → /workspace/go-project/gobrave/.gobrave
//   - UNC 路径 \\server\share\dir → //server/share/dir
//
// 已经是 POSIX 路径（Linux/macOS 宿主）时原样返回，保证 Linux 行为零变化。
func ToContainerPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}

	if len(p) >= 2 && p[1] == ':' && isASCIIAlpha(p[0]) {
		rest := p[2:]
		// 单独一个 "C:" 没有意义，当作根目录。
		if strings.TrimSpace(rest) == "" {
			return "/"
		}
		if rest[0] != '\\' && rest[0] != '/' {
			// 盘符相对路径（C:foo），补上分隔符再处理。
			rest = "/" + rest
		}
		return normalizePosixSlashes(rest)
	}

	if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//") {
		return normalizePosixSlashes(strings.ReplaceAll(p, `\`, "/"))
	}

	return p
}

// normalizePosixSlashes 把反斜杠折叠成 /，并把真实路径（如 C:\a\\b）尽量清理干净。
func normalizePosixSlashes(p string) string {
	slashed := strings.ReplaceAll(p, `\`, "/")
	// filepath.Clean 在 Windows 上会产生反斜杠，在 Linux 上才是 POSIX 语义，
	// 所以这里用 path.Clean 的等价实现：只做 "//" 折叠与结尾斜杠处理。
	for strings.Contains(slashed, "//") {
		slashed = strings.ReplaceAll(slashed, "//", "/")
	}
	if slashed != "/" {
		slashed = strings.TrimRight(slashed, "/")
	}
	return slashed
}

// ToHostPath 把容器路径映射回宿主路径（Windows 时需要 C:/... 形式）。
func ToHostPath(containerPath, windowsDrivePrefix string) string {
	p := strings.TrimSpace(containerPath)
	if p == "" {
		return ""
	}
	prefix := strings.TrimSpace(windowsDrivePrefix)
	if prefix == "" {
		return p
	}
	// 去掉容器路径的前导 / 再拼上盘符。
	rel := strings.TrimLeft(strings.ReplaceAll(p, `/`, `\`), `\`)
	return prefix + rel
}

// FromSlashForHost 把可能存在的正斜杠宿主路径转成当前操作系统习惯的分隔符。
// Windows 上返回 C:\a\b，Linux 上返回 /a/b，可直接给 filepath/os 使用。
func FromSlashForHost(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if IsWindowsHostPath(p) {
		return strings.ReplaceAll(p, "/", `\`)
	}
	return filepath.FromSlash(p)
}
