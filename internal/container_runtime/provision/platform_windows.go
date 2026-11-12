//go:build windows

package provision

import (
	"github.com/biox-dev/gobrave/internal/container_runtime/provision/types"
	"github.com/biox-dev/gobrave/internal/container_runtime/provision/wsl"
)

// New 返回 WSL 发行版实现。
//
// 这条依赖边（provision → wsl）只在 Windows 构建中成立。wsl 包本身只依赖
// provision/types，因此不构成 import cycle；Linux 构建里这条边完全不存在。
func New(cfg Config) Provisioner {
	return wsl.New(wsl.Config{
		BaseDir:     cfg.BaseDir,
		DownloadURL: cfg.DownloadURL,
		Distro:      cfg.Distro,
		Port:        cfg.Port,
	})
}

var _ = types.ErrNotInstalled
