package manager

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/biox-dev/gobrave/internal/agent/lsp"
	"github.com/biox-dev/gobrave/internal/agent/lsp/logging"
	"github.com/biox-dev/gobrave/internal/agent/lsp/watcher"
)

// initTimeout 是单个 LSP 服务初始化的超时时间（部分服务首次索引较慢）。
const initTimeout = 30 * time.Second

// Manager 管理一组 LSP 客户端（迁移自 opencode internal/app/lsp.go）。
//
// 它负责：按配置启动客户端 → initialize → 等待 ready → 启动工作区 watcher，
// 并对外暴露 `map[string]*lsp.Client`（即原 opencode 的 lspClients），供
// diagnostics / edit / view / write / patch 等工具使用。
type Manager struct {
	mu      sync.RWMutex
	clients map[string]*lsp.Client
	cancels []context.CancelFunc
	wg      sync.WaitGroup
	closed  bool
}

// NewManager 创建 LSP 客户端管理器。
func NewManager() *Manager {
	return &Manager{clients: make(map[string]*lsp.Client)}
}

// Start 按配置在后台启动全部 LSP 服务（各自独立 goroutine，互不阻塞）。
func (m *Manager) Start(ctx context.Context, servers map[string]lsp.Server) {
	for name, server := range servers {
		if server.Disabled || server.Command == "" {
			continue
		}
		m.start(ctx, name, server)
	}
}

// Clients 返回当前已就绪 / 正在启动的客户端快照（map 为副本）。
func (m *Manager) Clients() map[string]*lsp.Client {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]*lsp.Client, len(m.clients))
	for name, c := range m.clients {
		out[name] = c
	}
	return out
}

// Close 停止全部 watcher 并关闭客户端。
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	cancels := m.cancels
	m.cancels = nil
	m.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	m.wg.Wait()

	for _, c := range m.clients {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = c.Shutdown(shutdownCtx)
		cancel()
		_ = c.Close()
	}
}

// start 启动单个 LSP 服务；失败只记录日志，不影响其它服务。
func (m *Manager) start(ctx context.Context, name string, server lsp.Server) {
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.run(ctx, name, server)
	}()
}

func (m *Manager) run(ctx context.Context, name string, server lsp.Server) {
	client, err := lsp.NewClient(ctx, server.Command, server.Args...)
	if err != nil {
		logging.Error(fmt.Sprintf("LSP 客户端创建失败: %s: %v", name, err))
		return
	}

	initCtx, cancel := context.WithTimeout(ctx, initTimeout)
	defer cancel()
	if _, err := client.InitializeLSPClient(initCtx, lsp.WorkingDirectory()); err != nil {
		logging.Error(fmt.Sprintf("LSP 初始化失败: %s: %v", name, err))
		_ = client.Close()
		return
	}

	if err := client.WaitForServerReady(initCtx); err != nil {
		logging.Warn(fmt.Sprintf("LSP 服务未就绪（继续使用现有能力）: %s: %v", name, err))
		client.SetServerState(lsp.StateError)
	} else {
		client.SetServerState(lsp.StateReady)
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		_ = client.Close()
		return
	}
	m.clients[name] = client
	m.mu.Unlock()

	watchCtx, watchCancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancels = append(m.cancels, watchCancel)
	m.mu.Unlock()

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer logging.RecoverPanic("LSP-watcher-"+name, nil)
		watcher.NewWorkspaceWatcher(client).WatchWorkspace(watchCtx, lsp.WorkingDirectory())
		logging.Debug(fmt.Sprintf("LSP 工作区 watcher 退出: %s", name))
	}()
}
