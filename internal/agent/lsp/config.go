package lsp

import "sync"

// 本文件是 opencode internal/config 在 LSP 使用面上的最小替代：
// opencode 的 lsp 包只依赖 config.Get().Debug / DebugLSP / LSP 与
// config.WorkingDirectory()，迁移到 gobrave 后改由上层显式配置（SetConfig），
// 避免 lsp 包反向依赖 gobrave 的全局 config。

// Server 描述一个 LSP 服务的启动配置。
type Server struct {
	// Disabled 为 true 时跳过该服务。
	Disabled bool
	// Command 是可执行文件路径（如 gopls / typescript-language-server）。
	Command string
	// Args 是启动参数。
	Args []string
	// Options 是传给 initialize 的 initializationOptions（可选）。
	Options any
}

// Config 是 LSP 子系统的运行时配置。
type Config struct {
	// Debug 打开 LSP watcher 的常规调试日志。
	Debug bool
	// DebugLSP 打开 LSP 协议层的详细日志（收发消息）。
	DebugLSP bool
	// WorkingDir 是 LSP 服务的工作目录（也是打开文件的根目录）。
	WorkingDir string
	// Servers 是已注册的 LSP 服务，key 为服务名。
	Servers map[string]Server
}

var (
	configMu sync.RWMutex
	config   = &Config{}
)

// SetConfig 替换全局 LSP 配置（并发安全）。
func SetConfig(cfg *Config) {
	if cfg == nil {
		cfg = &Config{}
	}
	configMu.Lock()
	config = cfg
	configMu.Unlock()
}

// Get 返回当前 LSP 配置；未配置时返回零值配置而非 nil。
func Get() *Config {
	configMu.RLock()
	defer configMu.RUnlock()
	return config
}

// WorkingDirectory 返回 LSP 工作目录。
func WorkingDirectory() string { return Get().WorkingDir }
