// Package backend 负责 brave 桌面壳与后端服务之间的连接。
//
// 当前阶段（阶段 1）的策略是「连接已运行的后端」：窗口启动时探测
// http://127.0.0.1:<port>，外壳只把前端请求反向代理过去，不负责拉起进程。
//
// 后续在同一个包内增加 Start/Stop（sidecar 子进程或库内嵌方式），
// 对上层（cmd/desktop）与前端暴露的接口保持不变。
package backend

import (
	"net/url"
	"os"
	"strconv"
	"strings"
)

const (
	// DefaultPort 是 gobrave 的默认监听端口（见 gobrave/config.yml 的 server 段）。
	DefaultPort = 8084
	// DefaultHost 是 gobrave 的默认监听地址。
	DefaultHost = "127.0.0.1"

	// envBaseURL 允许通过环境变量整体覆盖后端地址，
	// 例如连接远程后端：BRAVE_DESKTOP_BACKEND_URL=http://10.0.0.5:8084
	envBaseURL = "BRAVE_DESKTOP_BACKEND_URL"
	// envHost / envPort 用于在未设置完整 URL 时单独指定。
	envHost = "BRAVE_DESKTOP_BACKEND_HOST"
	envPort = "BRAVE_DESKTOP_BACKEND_PORT"
)

// Config 描述后端连接参数。
type Config struct {
	// Scheme 为 http 或 https。
	Scheme string
	// Host 为后端主机名或 IP。
	Host string
	// Port 为后端端口。
	Port int
}

// LoadConfig 依据环境变量与默认值构造后端连接配置。
func LoadConfig() Config {
	if raw := strings.TrimSpace(os.Getenv(envBaseURL)); raw != "" {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			scheme := u.Scheme
			if scheme == "" {
				scheme = "http"
			}
			host := u.Hostname()
			port := DefaultPort
			if p, err := strconv.Atoi(u.Port()); err == nil && p > 0 {
				port = p
			}
			return Config{Scheme: scheme, Host: host, Port: port}
		}
	}

	cfg := Config{Scheme: "http", Host: DefaultHost, Port: DefaultPort}
	if h := strings.TrimSpace(os.Getenv(envHost)); h != "" {
		cfg.Host = h
	}
	if p, err := strconv.Atoi(strings.TrimSpace(os.Getenv(envPort))); err == nil && p > 0 {
		cfg.Port = p
	}
	return cfg
}

// BaseURL 返回后端根地址，例如 http://127.0.0.1:8084。
func (c Config) BaseURL() string {
	return c.Scheme + "://" + c.Host + ":" + strconv.Itoa(c.Port)
}
