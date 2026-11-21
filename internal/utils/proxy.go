package utils

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/http/httpproxy"
)

// ProxySettings 描述出站 HTTP 代理的三项配置。
//
// 它刻意只使用普通字符串字段，不依赖任何配置包的类型，
// 因此 internal/config 可以直接把 proxy 段映射过来，其他包也能独立构造。
//
// 语义约定：代理行为完全由此结构决定，未配置时一律直连，
// 不读取 http_proxy / https_proxy 等环境变量（环境变量读取见 SystemProxySettings）。
type ProxySettings struct {
	// HTTPProxy 是出站 http:// 请求使用的代理地址，例如 http://10.110.1.11:7890。
	// 为空表示不需要 http 协议代理（不会回退到环境变量）。
	HTTPProxy string
	// HTTPSProxy 是出站 https:// 请求使用的代理地址，例如 http://10.110.1.11:7890。
	// 为空表示不需要 https 协议代理（不会回退到环境变量）。
	HTTPSProxy string
	// NoProxy 是不走代理的主机列表（逗号分隔，支持域名 / IP / CIDR）。
	NoProxy string
}

// ProxySource 是「能提供出站代理三项取值」的最小接口，用于让 utils 主动
// 从配置侧取值，而不是由配置包把取值灌回 utils 类型。
//
// *config.ProxyConfig 直接满足该接口（字段名不变、空值即未配置），
// 因此装配层只需 utils.ProxySettings.FromConfig(cfg.Proxy)，
// 无需在 utils 里声明别名字段或依赖配置包类型。
type ProxySource interface {
	// OutboundProxy 返回 http_proxy / https_proxy / no_proxy 三项取值。
	OutboundProxy() (httpProxy, httpsProxy, noProxy string)
}

// FromConfig 从配置侧读取代理设置；src 为 nil 时返回零值（即直连）。
//
// 这是推荐的构造方式：代理行为与取值都收敛在 utils，配置包只保留字段。
func (ProxySettings) FromConfig(src ProxySource) ProxySettings {
	if src == nil {
		return ProxySettings{}
	}
	httpProxy, httpsProxy, noProxy := src.OutboundProxy()
	return ProxySettings{
		HTTPProxy:  httpProxy,
		HTTPSProxy: httpsProxy,
		NoProxy:    noProxy,
	}
}

// IsEmpty 报告是否未配置任何代理。
// 仅配置了 no_proxy、却没有配置 http_proxy / https_proxy 时同样视为未配置代理。
func (s ProxySettings) IsEmpty() bool {
	return strings.TrimSpace(s.HTTPProxy) == "" && strings.TrimSpace(s.HTTPSProxy) == ""
}

// ProxyFunc 返回按目标 URL 选择代理的函数。
//
// 行为完全由 s 决定：http_proxy / https_proxy / no_proxy。
// 未配置代理时返回的函数一律返回 nil（直连），不读取环境变量。
func (s ProxySettings) ProxyFunc() func(*url.URL) (*url.URL, error) {
	if s.IsEmpty() {
		return directProxy
	}
	proxyCfg := httpproxy.Config{
		HTTPProxy:  strings.TrimSpace(s.HTTPProxy),
		HTTPSProxy: strings.TrimSpace(s.HTTPSProxy),
		NoProxy:    strings.TrimSpace(s.NoProxy),
	}
	return proxyCfg.ProxyFunc()
}

// NewHTTPTransport 返回套用了代理配置的 Transport。
// 可在需要自定义超时 / 连接池时作为任意出站 http.Client 的基础。
func (s ProxySettings) NewHTTPTransport() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	proxyFunc := s.ProxyFunc()
	transport.Proxy = func(req *http.Request) (*url.URL, error) {
		return proxyFunc(req.URL)
	}
	return transport
}

// NewHTTPClient 返回套用了代理配置的 http.Client。
func (s ProxySettings) NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: s.NewHTTPTransport()}
}

// directProxy 表示直连：不经过任何代理。
func directProxy(*url.URL) (*url.URL, error) { return nil, nil }

// SystemProxySettings 返回从进程环境变量解析出的代理配置。
//
// 与 ProxySettings 的「忽略环境变量」语义相反，它专门用于需要识别
// 宿主机系统代理的场景（例如判断某个地址是否就是系统代理本身）。
func SystemProxySettings() ProxySettings {
	cfg := httpproxy.FromEnvironment()
	return ProxySettings{
		HTTPProxy:  cfg.HTTPProxy,
		HTTPSProxy: cfg.HTTPSProxy,
		NoProxy:    cfg.NoProxy,
	}
}

// IsProxyHost 报告 host（可带端口，如 10.110.1.11:7890）是否为 s 中配置的代理主机。
func (s ProxySettings) IsProxyHost(host string) bool {
	for _, proxyURL := range []string{s.HTTPProxy, s.HTTPSProxy} {
		proxyURL = strings.TrimSpace(proxyURL)
		if proxyURL == "" {
			continue
		}
		if parsed, err := url.Parse(proxyURL); err == nil && parsed.Host == host {
			return true
		}
	}
	return false
}
