//go:build !windows

package provision

import "github.com/biox-dev/gobrave/internal/container_runtime/provision/types"

// New 按当前平台返回合适的 Provisioner。
//
// 非 Windows 平台统一管理本机 dockerd（Linux 是主要场景）。
// 与 Windows 实现共享完全相同的接口与错误语义，上层无需分支。
func New(cfg Config) Provisioner {
	_ = cfg
	return newLocal()
}

var _ = types.ErrNotInstalled
