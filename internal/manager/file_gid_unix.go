//go:build !windows

package manager

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// resolvePathGID 返回路径属主的 GID（用于 docker.sock 之类的场景）。
// 仅类 Unix 平台可解析；路径不存在或无法取得 stat 时返回 false。
func resolvePathGID(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false
	}

	info, err := os.Stat(path)
	if err != nil {
		return "", false
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", false
	}

	return strconv.FormatUint(uint64(stat.Gid), 10), true
}

// hostUserIdentity 返回运行容器的宿主用户/组 ID（用于容器 --user 映射）。
// ok=false 表示当前平台没有可用的数值身份，调用方应跳过该映射。
func hostUserIdentity() (uid, gid string, ok bool) {
	return strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid()), true
}
