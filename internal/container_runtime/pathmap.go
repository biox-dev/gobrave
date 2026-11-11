package containerruntime

import (
	"path/filepath"
	"strings"

	"github.com/biox-dev/gobrave/internal/types"
)

// PathMapper 是跨平台路径语义的唯一入口。
//
// 系统里同时存在三种路径，混用是 Windows 上各种「丢盘符 / 多盘符 / daemon 找不到源」
// 问题的根源。这里用「来源」而不是「运行时 OS」来区分，并把换算全部收敛在本类型：
//
//	宿主路径   Windows 上是 D:\a\b，Linux 上是 /a/b；用于落盘和 bind mount 的 Source。
//	容器路径   容器内 POSIX：/a/b；用于 mount 的 Target、工作目录、命令与 env 取值。
//	daemon 路径 docker daemon 所在机器的路径（WSL2 下 D:\a → /mnt/d/a），仅 bind Source 用。
//
// 约定：storage.base_dir 是唯一可信的宿主锚点——盘符由它决定，容器路径由它派生。
// 因此「容器路径 → 宿主路径」只在路径确实位于 storage.base_dir 之下时才补盘符，
// 其余 POSIX 路径（/var/run/docker.sock、镜像内路径）原样返回。
//
// 值为 nil 时所有方法都可安全调用，等价于「不做任何转换」。
type PathMapper struct {
	// drive 是 storage.base_dir 的盘符前缀（形如 `D:\`）；非 Windows 或未配置为空。
	drive string
	// containerBase 是 storage.base_dir 的容器路径形式（形如 /workspace/gobrave/.gobrave）。
	containerBase string
	// wslDaemon 表示 docker daemon 跑在 WSL2 里。
	wslDaemon bool
}

// NewPathMapper 按 storage.base_dir 与 daemon 形态构建路径映射器。
//
// baseDir 为空时只做容器路径归一（Container/Join 仍然可用），不做宿主路径还原。
func NewPathMapper(baseDir string, wslDaemon bool) *PathMapper {
	baseDir = strings.TrimSpace(baseDir)
	m := &PathMapper{wslDaemon: wslDaemon}
	if len(baseDir) >= 2 && baseDir[1] == ':' && isASCIIAlpha(baseDir[0]) {
		m.drive = baseDir[:2] + `\`
	}
	m.containerBase = strings.TrimRight(ToContainerPath(baseDir), "/")
	return m
}

// WSLDaemon 报告是否按 WSL2 daemon 语义翻译 bind Source。
func (m *PathMapper) WSLDaemon() bool {
	return m != nil && m.wslDaemon
}

// Host 把任意路径还原成当前宿主可用的路径。
//
//   - 已是宿主路径（盘符 / UNC）：只统一分隔符，幂等，避免 D:\ 被二次拼成 D:\D:\；
//   - storage 派生出的容器路径：补回 base_dir 的盘符；
//   - 其他 POSIX 路径（docker.sock / 容器内路径）：原样返回，不做猜测。
func (m *PathMapper) Host(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || m == nil {
		return p
	}
	if IsWindowsHostPath(p) {
		return strings.ReplaceAll(p, "/", `\`)
	}
	if m.drive == "" {
		return filepath.FromSlash(p)
	}
	if !isUnderPath(p, m.containerBase) {
		return p
	}
	return ToHostPath(p, m.drive)
}

// Container 把任意路径归一成容器内 POSIX 路径（幂等）。
func (m *PathMapper) Container(p string) string {
	return ToContainerPath(p)
}

// Daemon 把 bind Source 翻译成 docker daemon 视角的路径；非 WSL daemon 时原样返回。
func (m *PathMapper) Daemon(p string) string {
	if m == nil || !m.wslDaemon {
		return p
	}
	return ToWSLDaemonPath(p)
}

// Join 以容器路径语义拼接（与路径包无关，避免 Windows 上产生反斜杠）。
func (m *PathMapper) Join(base string, elems ...string) string {
	joined := m.Container(base)
	for _, elem := range elems {
		elem = strings.TrimSpace(elem)
		if elem == "" {
			continue
		}
		if joined == "" || joined == "/" {
			joined = "/" + strings.Trim(elem, "/")
			continue
		}
		joined = strings.TrimRight(joined, "/") + "/" + strings.Trim(elem, "/")
	}
	return joined
}

// NormalizeVolumes 把 volume 的路径契约收敛为一处：bind 的 Source 是宿主路径，
// Target 是容器内路径。模板变量展开后 Source 可能是容器路径（Windows 上没盘符），
// 在任何落盘 / 交给 runtime 之前统一还原。
func (m *PathMapper) NormalizeVolumes(volumes []types.ContainerVolume) {
	for i := range volumes {
		vol := &volumes[i]
		if !IsBindVolumeType(vol.Type) {
			continue
		}
		vol.Source = m.Host(vol.Source)
		vol.Target = m.Container(vol.Target)
	}
}

// IsBindVolumeType 判断 volume 类型是否 bind（Source 是宿主路径）。
//
// volume / named 的 Source 是卷名，tmpfs 没有 Source，都不参与路径换算。
func IsBindVolumeType(volumeType string) bool {
	switch strings.ToLower(strings.TrimSpace(volumeType)) {
	case "volume", "named", "tmpfs":
		return false
	default:
		return true
	}
}

// isUnderPath 判断 p 是否等于 base 或位于 base 之下（POSIX 语义）。
func isUnderPath(p, base string) bool {
	if base == "" || !strings.HasPrefix(p, "/") {
		return false
	}
	if p == base {
		return true
	}
	return strings.HasPrefix(p, base+"/")
}
