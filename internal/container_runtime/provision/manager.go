package provision

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/biox-dev/gobrave/internal/logger"
)

// Manager 包装一个具体 Provisioner，补上上层需要的三件事：
//
//  1. 统一的「启动时调用一次」入口 Start / StartAsync；
//  2. 把 ErrNotInstalled 与真正的失败区分开，方便调用方决定是否静默；
//  3. 提供只读的 Endpoint，避免装配层为了拿地址再探测一次。
type Manager struct {
	inner Provisioner

	mu      sync.RWMutex
	started bool
	lastErr error
}

// NewManager 包装给定实现；inner 为 nil 时返回 nil（调用方按「无预置能力」处理）。
func NewManager(inner Provisioner) *Manager {
	if inner == nil {
		return nil
	}
	return &Manager{inner: inner}
}

// Inner 返回底层实现，供需要平台特有能力（如安装向导）的调用方使用。
func (m *Manager) Inner() Provisioner {
	if m == nil {
		return nil
	}
	return m.inner
}

// Start 启动运行时。
//
// 未安装时返回 ErrNotInstalled（调用方应静默忽略并等待 UI 安装向导）；
// 其他错误视为真实故障。
func (m *Manager) Start(ctx context.Context) (string, error) {
	if m == nil || m.inner == nil {
		return "", ErrUnsupported
	}
	ep, err := m.inner.Start(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.lastErr = err
		if !errors.Is(err, ErrNotInstalled) {
			m.started = false
		}
		return "", err
	}
	m.started = true
	m.lastErr = nil
	return ep, nil
}

// StartTimeout 是 StartAsync 的默认超时上限。
//
// dockerd 冷启动（WSL 实例唤醒 + 容器运行时初始化）通常 3-15s，
// 给 30s 上限既避免误判，也不会留下长期挂起的 goroutine。
const StartTimeout = 30 * time.Second

// StartAsync 在后台启动运行时，立即返回，不阻塞调用方（如进程启动 / 界面打开）。
//
// 语义与 Start 一致，只是把等待与错误处理移入 goroutine：
//   - 未安装（ErrNotInstalled）只记录日志，等待 UI 安装向导；
//   - 其他错误记录告警；
//   - 成功后通过 Endpoint 读取实际地址。
//
// m 为 nil（未启用预置）时什么都不做。
func (m *Manager) StartAsync(ctx context.Context) {
	if m == nil || m.inner == nil {
		return
	}
	go func() {
		runCtx, cancel := context.WithTimeout(ctx, StartTimeout)
		defer cancel()

		if _, err := m.Start(runCtx); err != nil {
			if errors.Is(err, ErrNotInstalled) {
				logger.Infof(runCtx, "[provision] runtime not installed, waiting for setup wizard")
				return
			}
			logger.Warnf(runCtx, "[provision] runtime start failed: %v", err)
			return
		}
		logger.Infof(runCtx, "[provision] runtime ready: %s", m.Endpoint())
	}()
}

// Endpoint 返回 daemon 地址（由底层实现提供，如 WSL 侧解析出的发行版地址）；
// 未启用或未启动时为空。
func (m *Manager) Endpoint() string {
	if m == nil || m.inner == nil {
		return ""
	}
	return m.inner.Endpoint()
}

// WrappedError 返回最近一次 Start 的错误（供状态接口展示）。
func (m *Manager) WrappedError() error {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastErr
}

// ProbeEndpoint 是装配层的便捷方法：启动成功则返回 endpoint，
// 未安装返回 ("", ErrNotInstalled)，其余返回错误。
func (m *Manager) ProbeEndpoint(ctx context.Context) (string, error) {
	ep, err := m.Start(ctx)
	if err != nil {
		return "", fmt.Errorf("provision runtime: %w", err)
	}
	return ep, nil
}
