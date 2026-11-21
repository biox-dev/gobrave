package utils

import (
	"net/http"
	"net/url"
	"testing"
)

// mustParseURL 是测试辅助：解析失败直接终止用例。
func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	return u
}

// TestProxySettingsIsEmpty 校验「只看是否有 http/https 代理」的判定语义。
func TestProxySettingsIsEmpty(t *testing.T) {
	cases := []struct {
		name string
		in   ProxySettings
		want bool
	}{
		{"zero", ProxySettings{}, true},
		{"blank values", ProxySettings{HTTPProxy: "  ", HTTPSProxy: "\t"}, true},
		{"only no_proxy", ProxySettings{NoProxy: "internal.example.com"}, true},
		{"http only", ProxySettings{HTTPProxy: "http://10.0.0.1:7890"}, false},
		{"https only", ProxySettings{HTTPSProxy: "http://10.0.0.1:7890"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.in.IsEmpty(); got != c.want {
				t.Errorf("IsEmpty() = %v, want %v", got, c.want)
			}
		})
	}
}

// TestProxySettingsProxyFunc 校验 http/https 分别取对应代理，no_proxy 命中时直连。
func TestProxySettingsProxyFunc(t *testing.T) {
	s := ProxySettings{
		HTTPProxy:  "http://10.110.1.11:7890",
		HTTPSProxy: "http://10.110.1.11:7890",
		NoProxy:    "internal.example.com",
	}
	proxyFunc := s.ProxyFunc()

	cases := []struct {
		target string
		want   string // 空串表示直连
	}{
		{"http://example.com/a", "http://10.110.1.11:7890"},
		{"https://example.com/a", "http://10.110.1.11:7890"},
		{"http://internal.example.com/a", ""},
	}
	for _, c := range cases {
		got, err := proxyFunc(mustParseURL(t, c.target))
		if err != nil {
			t.Fatalf("proxyFunc(%s): %v", c.target, err)
		}
		if c.want == "" {
			if got != nil {
				t.Errorf("proxyFunc(%s) = %v, want direct", c.target, got)
			}
			continue
		}
		if got == nil || got.String() != c.want {
			t.Errorf("proxyFunc(%s) = %v, want %s", c.target, got, c.want)
		}
	}
}

// TestProxySettingsEmptyIsDirect 校验未配置代理时一律直连，
// 与配置包「忽略环境变量」的语义保持一致。
func TestProxySettingsEmptyIsDirect(t *testing.T) {
	t.Setenv("http_proxy", "http://env-proxy:8080")
	t.Setenv("https_proxy", "http://env-proxy:8080")

	var s ProxySettings
	for _, target := range []string{"http://example.com", "https://example.com"} {
		got, err := s.ProxyFunc()(mustParseURL(t, target))
		if err != nil {
			t.Fatalf("proxyFunc(%s): %v", target, err)
		}
		if got != nil {
			t.Errorf("proxyFunc(%s) = %v, want direct", target, got)
		}
	}
}

// TestProxySettingsIsProxyHost 校验代理主机识别（含带端口的匹配形式）。
func TestProxySettingsIsProxyHost(t *testing.T) {
	s := ProxySettings{
		HTTPProxy:  "http://10.110.1.11:7890",
		HTTPSProxy: "http://10.110.1.12:7890",
	}
	cases := []struct {
		host string
		want bool
	}{
		{"10.110.1.11:7890", true},
		{"10.110.1.12:7890", true},
		{"10.110.1.13:7890", false},
		{"", false},
	}
	for _, c := range cases {
		if got := s.IsProxyHost(c.host); got != c.want {
			t.Errorf("IsProxyHost(%q) = %v, want %v", c.host, got, c.want)
		}
	}

	// 空配置不应把任何主机当成代理。
	if (ProxySettings{}).IsProxyHost("10.110.1.11:7890") {
		t.Error("空配置不应识别出代理主机")
	}
}

// TestProxySettingsNewTransport 校验 Transport 使用的是同一套代理选择逻辑。
func TestProxySettingsNewTransport(t *testing.T) {
	s := ProxySettings{HTTPSProxy: "http://10.110.1.11:7890"}
	transport := s.NewHTTPTransport()
	if transport.Proxy == nil {
		t.Fatal("transport.Proxy 不应为 nil")
	}

	req := &http.Request{URL: mustParseURL(t, "https://example.com")}
	got, err := transport.Proxy(req)
	if err != nil {
		t.Fatalf("transport.Proxy: %v", err)
	}
	if got == nil || got.String() != "http://10.110.1.11:7890" {
		t.Errorf("transport.Proxy = %v, want http://10.110.1.11:7890", got)
	}
}

// fakeProxySource 是 ProxySource 的测试替身。
type fakeProxySource struct {
	httpProxy, httpsProxy, noProxy string
}

func (f fakeProxySource) OutboundProxy() (string, string, string) {
	return f.httpProxy, f.httpsProxy, f.noProxy
}

// TestProxySettingsFromConfig 校验 utils 主动从配置侧取值构造 ProxySettings，
// 以及 nil 来源回退为零值（直连）。
func TestProxySettingsFromConfig(t *testing.T) {
	got := (ProxySettings{}).FromConfig(fakeProxySource{
		httpProxy:  "http://10.110.1.11:7890",
		httpsProxy: "http://10.110.1.12:7890",
		noProxy:    "internal.example.com",
	})
	want := ProxySettings{
		HTTPProxy:  "http://10.110.1.11:7890",
		HTTPSProxy: "http://10.110.1.12:7890",
		NoProxy:    "internal.example.com",
	}
	if got != want {
		t.Errorf("FromConfig = %+v, want %+v", got, want)
	}

	if got := (ProxySettings{}).FromConfig(nil); !got.IsEmpty() {
		t.Errorf("FromConfig(nil) = %+v, want empty", got)
	}
}
