package backend

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Status 描述后端连接状态，供前端调试面板展示。
type Status struct {
	BaseURL string `json:"baseURL"`
	Healthy bool   `json:"healthy"`
	Message string `json:"message"`
}

// Manager 负责探测后端可用性。
//
// 阶段 1 它只是「已运行后端的探针」；后续接入 sidecar 或库内嵌时，
// 在这里增加 Start/Stop 即可，调用方无需修改。
type Manager struct {
	cfg    Config
	client *http.Client
}

// NewManager 创建 Manager。
func NewManager(cfg Config) *Manager {
	return &Manager{
		cfg: cfg,
		client: &http.Client{
			Timeout: 2 * time.Second,
		},
	}
}

// BaseURL 返回后端根地址。
func (m *Manager) BaseURL() string { return m.cfg.BaseURL() }

// Config 返回当前配置。
func (m *Manager) Config() Config { return m.cfg }

// SetBaseURL 覆盖后端根地址。
//
// 桌面端以库方式启动后端时，实际监听端口可能与配置不同
// （例如 --port=0 用随机空闲端口），启动后需要回填真实地址。
func (m *Manager) SetBaseURL(baseURL string) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "http"
	}
	cfg := Config{Scheme: scheme, Host: u.Hostname(), Port: DefaultPort}
	if p, parseErr := strconv.Atoi(u.Port()); parseErr == nil && p > 0 {
		cfg.Port = p
	}
	m.cfg = cfg
}

// Health 请求后端的 /health 探针。
func (m *Manager) Health(ctx context.Context) Status {
	st := Status{BaseURL: m.cfg.BaseURL()}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.cfg.BaseURL()+"/health", nil)
	if err != nil {
		st.Message = "构造健康检查请求失败: " + err.Error()
		return st
	}

	resp, err := m.client.Do(req)
	if err != nil {
		st.Message = "后端未响应: " + err.Error()
		return st
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		st.Healthy = true
		st.Message = "后端连接正常"
		return st
	}

	st.Message = "后端返回异常状态: " + resp.Status
	return st
}
