package wsl

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 发行版与文件命名常量。
const (
	// DefaultDistro 是导入到 WSL 的发行版名称。
	DefaultDistro = "gobrave-ubuntu-docker"
	// DefaultRootfsName 是下载到本地缓存的 rootfs 包文件名。
	DefaultRootfsName = "gobrave-ubuntu-docker-amd64.tar"
	// DefaultDaemonPort 是 WSL 内 dockerd 监听的 TCP 端口。
	DefaultDaemonPort = 2375
)

// layout 描述 <base_dir>/wsl 下的目录结构。
//
// 刻意把所有派生路径收敛在结构体方法上：安装/卸载/启动都需要这些路径，
// 散落在各处会让「卸载时该删哪些目录」这类问题变得不可验证。
type layout struct {
	root string
}

func newLayout(baseDir string) layout {
	return layout{root: filepath.Join(strings.TrimSpace(baseDir), "wsl")}
}

// Root <base_dir>/wsl
func (l layout) Root() string { return l.root }

// DistroDir 发行版数据目录（wsl --import 的 --location）。
func (l layout) DistroDir() string { return filepath.Join(l.root, "distro") }

// CacheDir 下载缓存目录。
func (l layout) CacheDir() string { return filepath.Join(l.root, "cache") }

// RootfsPath rootfs 包路径。
func (l layout) RootfsPath() string { return filepath.Join(l.CacheDir(), DefaultRootfsName) }

// PartPath 下载中的临时文件。
func (l layout) PartPath() string { return l.RootfsPath() + ".part" }

// StatePath state.json 路径。
func (l layout) StatePath() string { return filepath.Join(l.root, "state.json") }

// LogPath dockerd 日志路径。
func (l layout) LogPath() string { return filepath.Join(l.root, "dockerd.log") }

// PidPath dockerd 进程号文件路径。
func (l layout) PidPath() string { return filepath.Join(l.root, "dockerd.pid") }

// EnsureDirs 创建 root / cache / distro 目录。
func (l layout) EnsureDirs() error {
	for _, dir := range []string{l.root, l.CacheDir(), l.DistroDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create dir %s: %w", dir, err)
		}
	}
	return nil
}

// installed 判断发行版是否已安装：distro 目录存在且 state.json 可读。
func (l layout) installed() bool {
	if st, err := os.Stat(l.DistroDir()); err != nil || !st.IsDir() {
		return false
	}
	_, err := readState(l.StatePath())
	return err == nil
}

// downloaded 判断 rootfs 包是否已下载完成。
func (l layout) downloaded() bool {
	st, err := os.Stat(l.RootfsPath())
	return err == nil && st.Size() > 0
}

// state 是持久化的安装状态，用于避免每次探测都执行昂贵的 wsl 命令。
type state struct {
	// Distro 发行版名。
	Distro string `json:"distro"`
	// Version rootfs 版本号。
	Version string `json:"version"`
	// Port dockerd 监听的 TCP 端口。
	Port int `json:"port"`
	// InstalledAt 安装时间。
	InstalledAt time.Time `json:"installed_at"`
}

// readState 读取 state.json；文件不存在或损坏时返回错误。
func readState(path string) (*state, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if strings.TrimSpace(st.Distro) == "" {
		st.Distro = DefaultDistro
	}
	if st.Port <= 0 {
		st.Port = DefaultDaemonPort
	}
	return &st, nil
}

// writeState 原子写入 state.json（先写临时文件再 rename）。
func writeState(path string, st *state) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", tmp, err)
	}
	return nil
}
