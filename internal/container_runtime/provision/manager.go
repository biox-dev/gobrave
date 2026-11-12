package provision

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// Manager 包装一个具体 Provisioner，补上上层需要的三件事：
//
//  1. 统一的「启动时调用一次」入口 Start，缓存 endpoint 供装配层读取；
//  2. 把 ErrNotInstalled 与真正的失败区分开，方便调用方决定是否静默；
//  3. 提供只读的 Endpoint，避免装配层为了拿地址再探测一次。
type Manager struct {
	inner Provisioner

	mu       sync.RWMutex
	endpoint string
	started  bool
	lastErr  error
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

// Start 启动运行时并缓存 endpoint。
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
	m.endpoint = ep
	m.started = true
	m.lastErr = nil
	return ep, nil
}

// Endpoint 返回缓存中的 endpoint；未成功启动时为空。
func (m *Manager) Endpoint() string {
	if m == nil {
		return ""
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.endpoint
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
