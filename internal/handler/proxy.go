package handler

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/route"
	"github.com/gin-gonic/gin"
)

const (
	defaultBraveAPITarget   = "http://localhost:5000"
	defaultContainerTarget  = "http://localhost:8089"
	defaultOnlyOfficeTarget = "http://localhost:8080"
)

type ProxyHandler struct {
	braveAPIProxy   *httputil.ReverseProxy
	containerProxy  *httputil.ReverseProxy
	onlyOfficeProxy *httputil.ReverseProxy
	pathResolver    route.PathRouteResolver
}

func NewProxyHandler(cfg *config.Config, resolver route.PathRouteResolver) (*ProxyHandler, error) {
	braveAPITarget := defaultBraveAPITarget
	containerTarget := defaultContainerTarget
	onlyOfficeTarget := defaultOnlyOfficeTarget
	if cfg != nil && cfg.Proxy != nil {
		if v := strings.TrimSpace(cfg.Proxy.BraveAPI); v != "" {
			braveAPITarget = v
		}
		if v := strings.TrimSpace(cfg.Proxy.Container); v != "" {
			containerTarget = v
		}
		if v := strings.TrimSpace(cfg.Proxy.OnlyOffice); v != "" {
			onlyOfficeTarget = v
		}
	}

	braveAPIProxy, err := buildReverseProxy(braveAPITarget)
	if err != nil {
		return nil, err
	}
	containerProxy, err := buildReverseProxy(containerTarget)
	if err != nil {
		return nil, err
	}
	onlyOfficeProxy, err := buildReverseProxy(onlyOfficeTarget)
	if err != nil {
		return nil, err
	}

	return &ProxyHandler{
		braveAPIProxy:   braveAPIProxy,
		containerProxy:  containerProxy,
		onlyOfficeProxy: onlyOfficeProxy,
		pathResolver:    resolver,
	}, nil
}

func buildReverseProxy(target string) (*httputil.ReverseProxy, error) {
	targetURL, err := url.Parse(target)
	if err != nil {
		return nil, err
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	// FlushInterval = -1：立即 flush。应用会话（code-server / jupyter / rstudio）
	// 与终端 WebSocket 都是流式场景，默认的缓冲会把小包攒起来，在短请求风暴
	// （编辑器打开文件树、终端高频输出）下堆出大量 in-flight 连接，
	// 拖垮 WebKit 的网络进程 → 报 Network process crashed 并整页重载。
	proxy.FlushInterval = -1
	// 复用同一条带连接池的 Transport：默认每个 ReverseProxy 都会新建
	// 一个 Transport，空闲连接只保留极少数，反复建连会明显放大负载。
	proxy.Transport = sharedProxyTransport()
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originHost := req.Host
		originProto := requestScheme(req)
		incomingProto := firstForwardedValue(req.Header.Get("X-Forwarded-Proto"))
		if incomingProto != "" {
			originProto = incomingProto
		}

		originalDirector(req)
		req.Header.Set("X-Forwarded-Host", firstNonEmpty(firstForwardedValue(req.Header.Get("X-Forwarded-Host")), originHost))
		req.Header.Set("X-Forwarded-Proto", originProto)
		req.Header.Set("X-Forwarded-Port", firstNonEmpty(firstForwardedValue(req.Header.Get("X-Forwarded-Port")), defaultPortForScheme(originProto)))
		req.Header.Set("X-Forwarded-Uri", req.URL.RequestURI())
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		headers := resp.Header
		headers.Del("Access-Control-Allow-Origin")
		headers.Del("Access-Control-Allow-Credentials")
		headers.Del("Access-Control-Allow-Headers")
		headers.Del("Access-Control-Allow-Methods")
		headers.Del("Access-Control-Expose-Headers")
		headers.Del("Access-Control-Max-Age")
		rewriteRedirectLocation(resp)
		rewriteSetCookiePath(resp)
		return nil
	}

	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, err error) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusBadGateway)
		_, _ = rw.Write([]byte(`{"error":"proxy request failed"}`))
	}

	return proxy, nil
}

// sharedProxyTransport 返回进程内共享的、带连接池的 Transport。
//
// 默认情况下 httputil.ReverseProxy 会自己 new 一个 http.Transport，每个
// 反向代理各自一套连接池（且 MaxIdleConnsPerHost 只有 2）。应用会话、终端、
// code-server 这些流式场景会并发大量短请求，反复建连会明显放大 WebKit
// 网络进程的负担，因此统一复用同一个 Transport。
func sharedProxyTransport() *http.Transport {
	sharedProxyTransportOnce.Do(func() {
		t := http.DefaultTransport.(*http.Transport).Clone()
		// 一个宿主（容器 / code-server）经常需要保持几十条连接，
		// 默认的每宿主 2 条空闲连接会导致频繁建连。
		t.MaxIdleConns = 512
		t.MaxIdleConnsPerHost = 64
		t.MaxConnsPerHost = 0 // 不限制，交给对端
		t.IdleConnTimeout = 90 * time.Second
		// 与本地/内网容器交互，关闭对 HTTP/2 的强制升级，避免 WebSocket
		// 升级与 SSE 在 h2 下降级路径上多绕一层。
		t.ForceAttemptHTTP2 = false
		sharedProxyTransportInstance = t
	})
	return sharedProxyTransportInstance
}

var (
	sharedProxyTransportOnce     sync.Once
	sharedProxyTransportInstance *http.Transport
)

func rewriteRedirectLocation(resp *http.Response) {
	if resp == nil || resp.Request == nil {
		return
	}
	location := strings.TrimSpace(resp.Header.Get("Location"))
	if location == "" {
		return
	}

	parsedLocation, err := url.Parse(location)
	if err != nil {
		return
	}

	clientProto := firstNonEmpty(firstForwardedValue(resp.Request.Header.Get("X-Forwarded-Proto")), requestScheme(resp.Request))
	clientPort := firstNonEmpty(firstForwardedValue(resp.Request.Header.Get("X-Forwarded-Port")), defaultPortForScheme(clientProto))
	clientHost := firstNonEmpty(firstForwardedValue(resp.Request.Header.Get("X-Forwarded-Host")), resp.Request.Host)
	clientHost = appendPortIfMissing(clientHost, clientPort, clientProto)
	clientPrefix := normalizeForwardedPrefix(firstForwardedValue(resp.Request.Header.Get("X-Forwarded-Prefix")))

	if parsedLocation.IsAbs() {
		if parsedLocation.Scheme == "http" || parsedLocation.Scheme == "https" {
			if clientProto != "" {
				parsedLocation.Scheme = clientProto
			}
			if clientHost != "" {
				parsedLocation.Host = clientHost
			}
			if clientPrefix != "" {
				parsedLocation.Path = prependPathPrefix(parsedLocation.Path, clientPrefix)
			}
			resp.Header.Set("Location", parsedLocation.String())
		}
		return
	}

	if strings.HasPrefix(parsedLocation.Path, "/") && clientPrefix != "" {
		parsedLocation.Path = prependPathPrefix(parsedLocation.Path, clientPrefix)
		resp.Header.Set("Location", parsedLocation.String())
	}
}

func rewriteSetCookiePath(resp *http.Response) {
	if resp == nil || resp.Request == nil {
		return
	}
	prefix := normalizeForwardedPrefix(firstForwardedValue(resp.Request.Header.Get("X-Forwarded-Prefix")))
	if prefix == "" {
		return
	}

	cookies := resp.Header.Values("Set-Cookie")
	if len(cookies) == 0 {
		return
	}

	resp.Header.Del("Set-Cookie")
	for _, raw := range cookies {
		resp.Header.Add("Set-Cookie", rewriteOneCookiePath(raw, prefix))
	}
}

func rewriteOneCookiePath(rawCookie, prefix string) string {
	parts := strings.Split(rawCookie, ";")
	if len(parts) <= 1 {
		return rawCookie
	}

	for i := 1; i < len(parts); i++ {
		attr := strings.TrimSpace(parts[i])
		if len(attr) < 5 || !strings.EqualFold(attr[:5], "path=") {
			continue
		}
		pathValue := strings.TrimSpace(attr[5:])
		if pathValue == "" {
			pathValue = "/"
		}
		parts[i] = " Path=" + prependPathPrefix(pathValue, prefix)
		return strings.Join(parts, ";")
	}

	return rawCookie
}

func firstForwardedValue(v string) string {
	if v == "" {
		return ""
	}
	parts := strings.Split(v, ",")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func requestScheme(req *http.Request) string {
	if req != nil && req.TLS != nil {
		return "https"
	}
	return "http"
}

func defaultPortForScheme(scheme string) string {
	if strings.EqualFold(scheme, "https") {
		return "443"
	}
	return "80"
}

func normalizeForwardedPrefix(prefix string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return ""
	}
	prefix = "/" + strings.Trim(prefix, "/")
	if prefix == "/" {
		return ""
	}
	return prefix
}

func prependPathPrefix(path, prefix string) string {
	prefix = normalizeForwardedPrefix(prefix)
	if prefix == "" {
		return path
	}
	if path == "" {
		return prefix + "/"
	}
	if path == prefix || strings.HasPrefix(path, prefix+"/") {
		return path
	}
	if strings.HasPrefix(path, "/") {
		return prefix + path
	}
	return prefix + "/" + path
}

func appendPortIfMissing(host, port, scheme string) string {
	host = strings.TrimSpace(host)
	port = strings.TrimSpace(port)
	if host == "" || port == "" {
		return host
	}
	if hasExplicitPort(host) || port == defaultPortForScheme(scheme) {
		return host
	}

	plainHost := strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	if plainHost == "" {
		return host
	}
	return net.JoinHostPort(plainHost, port)
}

func hasExplicitPort(host string) bool {
	parsed, err := url.Parse("//" + strings.TrimSpace(host))
	if err != nil {
		return false
	}
	return parsed.Port() != ""
}

func (h *ProxyHandler) BraveAPIProxy(c *gin.Context) {
	h.braveAPIProxy.ServeHTTP(c.Writer, c.Request)
	c.Abort()
}

func (h *ProxyHandler) ContainerProxy(c *gin.Context) {
	h.containerProxy.ServeHTTP(c.Writer, c.Request)
	c.Abort()
}

func (h *ProxyHandler) AppSessionProxy(c *gin.Context) {
	if h.pathResolver == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "app route resolver is not enabled"})
		c.Abort()
		return
	}

	reg, matchedPrefix, ok := h.pathResolver.ResolveByPath(c.Request.URL.Path)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "app route not found"})
		c.Abort()
		return
	}

	target := fmt.Sprintf("http://%s:%d", strings.TrimSpace(reg.Backend.Host), reg.Backend.Port)
	// proxy, err := buildReverseProxy(target)
	proxy, err := buildReverseProxy(target)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid app backend target"})
		c.Abort()
		return
	}
	if reg.IsTrimPrefix {
		trimProxyPrefix(c.Request, matchedPrefix)
	}
	c.Request.Header.Set("X-Gateway-Route-Key", reg.RouteKey)
	c.Request.Header.Set("X-Gateway-Backend", net.JoinHostPort(reg.Backend.Host, strconv.Itoa(reg.Backend.Port)))

	proxy.ServeHTTP(c.Writer, c.Request)
	c.Abort()
}

func (h *ProxyHandler) OnlyOfficeProxy(c *gin.Context) {
	trimProxyPrefix(c.Request, "/onlyoffice")
	h.onlyOfficeProxy.ServeHTTP(c.Writer, c.Request)
	c.Abort()
}

func (h *ProxyHandler) FallbackProxy(c *gin.Context) {
	// if !strings.HasPrefix(c.Request.URL.Path, "/api/v1/") {
	// 	c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	// 	return
	// }

	h.braveAPIProxy.ServeHTTP(c.Writer, c.Request)
	c.Abort()
}

func trimProxyPrefix(req *http.Request, prefix string) {
	if req == nil {
		return
	}
	prefix = normalizeForwardedPrefix(prefix)
	if prefix == "" {
		return
	}

	originalPath := req.URL.Path
	trimmedPath := strings.TrimPrefix(originalPath, prefix)
	if trimmedPath == "" {
		trimmedPath = "/"
	}
	if !strings.HasPrefix(trimmedPath, "/") {
		trimmedPath = "/" + trimmedPath
	}
	req.URL.Path = trimmedPath

	if req.URL.RawPath != "" {
		trimmedRawPath := strings.TrimPrefix(req.URL.RawPath, prefix)
		if trimmedRawPath == "" {
			trimmedRawPath = "/"
		}
		if !strings.HasPrefix(trimmedRawPath, "/") {
			trimmedRawPath = "/" + trimmedRawPath
		}
		req.URL.RawPath = trimmedRawPath
	}

	existingPrefix := firstForwardedValue(req.Header.Get("X-Forwarded-Prefix"))
	req.Header.Set("X-Forwarded-Prefix", firstNonEmpty(existingPrefix, prefix))
}
