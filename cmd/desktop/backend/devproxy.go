package backend

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"
)

// DevProxy 是「开发模式」下的小型前置代理。
//
// 背景：`-dev-url http://localhost:5174/` 时 WebView 直接加载 Vite dev server，
// 页面 origin 就是 Vite，而不是我们自己的 http.Server。此时前端发出的
// 请求会打到 Vite 上，导致两类问题：
//
//  1. `POST /wails/runtime` → Vite 不认这个路径，404，原生窗口打不开；
//  2. `/api/...`、`/c/analysis/apps/<id>/` 等后端路径 → 要么 404，要么被
//     Vite 代理到**另一个**后端（vite.config.ts 里写死的 localhost:8084），
//     而不是本进程内嵌的那个后端。
//
// 解决方式：在 Vite 前面再加一层本地代理（本文件），由它按路径分流：
//
//	WebView → DevProxy(127.0.0.1:随机端口)
//	            ├── /wails/*            → Wails transport 中间件
//	            ├── /api、/c/analysis/apps/… → 内嵌后端（appHandler）
//	            └── 其余（页面、静态资源、HMR）→ Vite
//
// 这样开发模式与生产模式（本地 http.Server）行为一致：后端相关请求始终打到
// **本进程内嵌后端**，前端资源与热更新始终来自 Vite。
type DevProxy struct {
	srv      *http.Server
	listener net.Listener
	addr     string
}

// StartDevProxy 启动开发模式代理。
//
//   - target 是 Vite 的地址（如 http://localhost:5174/）；
//   - wailsHandler 是 Wails transport 中间件（处理 /wails/*）；
//   - appHandler 是后端 handler，负责静态资源/反代后端/SPA 回退，
//     与生产模式的本地 http.Server 完全相同，用于服务**后端路径**，
//     同时在 Vite 不可用时兼作兜底（避免白屏）。
//
// 返回的 URL 应当作为 WebView 的加载地址。
func StartDevProxy(
	target string,
	wailsHandler func(next http.Handler) http.Handler,
	appHandler http.Handler,
) (*DevProxy, error) {
	upstream, err := url.Parse(strings.TrimSpace(target))
	if err != nil || upstream.Host == "" {
		return nil, fmt.Errorf("无效的 dev-url: %q", target)
	}

	proxy := httputil.NewSingleHostReverseProxy(upstream)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		// Vite 的 allowedHosts 校验基于 Host，这里保持原始 Host 不改写，
		// 只在请求头里补上 X-Forwarded-* 便于排查。
		req.Header.Set("X-Forwarded-Host", req.Host)
		req.Header.Set("X-Forwarded-Proto", "http")
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		// Vite 未启动时给出清晰提示，而不是白屏。
		http.Error(w,
			"开发服务器不可用: "+e.Error()+"\n请确认已运行 `npm run dev` 且地址为 "+upstream.String(),
			http.StatusBadGateway,
		)
	}

	// Vite 的 HMR 走 WebSocket，httputil.ReverseProxy 不支持 Upgrade，
	// 这里手动做一次双向流量搬运，否则改代码不会热更新。
	proxy.FlushInterval = -1 // 立即 flush，保证 SSE / HMR 推送及时到达。

	// 后端路径：与生产模式完全同一套 handler（反代内嵌后端 + SPA 回退）。
	// 应用会话（/c/analysis/apps/<id>/）就由这里的反向代理转发到容器。
	var backendHandler http.Handler
	if appHandler != nil {
		backendHandler = appHandler
	}

	appHandlerFn := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if backendHandler != nil && shouldRouteToBackend(r.URL.Path) {
			// 让内嵌后端知道"客户端看到的地址"是本代理，而不是它自己的监听端口。
			// 后端的反代（internal/handler/proxy.go）会据此改写 Location 等
			// 绝对 URL，否则应用容器返回的 302 会把浏览器导到 127.0.0.1:18099
			// （后端端口）从而绕过代理、丢失同源与 /wails 能力。
			if r.Header.Get("X-Forwarded-Host") == "" {
				r.Header.Set("X-Forwarded-Host", r.Host)
			}
			if r.Header.Get("X-Forwarded-Proto") == "" {
				r.Header.Set("X-Forwarded-Proto", "http")
			}
			backendHandler.ServeHTTP(w, r)
			return
		}

		// HMR / WebSocket 必须直连 Vite，不能回退到静态资源。
		if isUpgradeRequest(r) {
			proxyUpgrade(w, r, upstream)
			return
		}

		// 其余请求（页面、/src/*、@vite/* 等）交给 Vite；
		// Vite 不可用时退回本地静态资源，便于离线调试。
		if appHandler == nil {
			proxy.ServeHTTP(w, r)
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		proxy.ServeHTTP(rec, r)
		if rec.status == http.StatusBadGateway {
			appHandler.ServeHTTP(w, r)
		}
	})

	// Wails 中间件包在最外层：只接管 /wails/*，其余交给 appHandlerFn。
	var handler http.Handler = appHandlerFn
	if wailsHandler != nil {
		handler = wailsHandler(appHandlerFn)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("监听开发代理端口失败: %w", err)
	}

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	p := &DevProxy{
		srv:      srv,
		listener: ln,
		addr:     "http://" + ln.Addr().String(),
	}

	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			fmt.Printf("[desktop] 开发代理退出: %v\n", serveErr)
		}
	}()

	return p, nil
}

// URL 返回代理根地址，例如 http://127.0.0.1:41234。
func (p *DevProxy) URL() string { return p.addr }

// Shutdown 关闭代理。
func (p *DevProxy) Shutdown() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return p.srv.Shutdown(ctx)
}

// statusRecorder 记录写出的状态码，用于判断 Vite 是否可用（502 时回退）。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// isUpgradeRequest 判断是否为 WebSocket / HTTP Upgrade 请求。
func isUpgradeRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") ||
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// shouldRouteToBackend 判断路径是否应交给内嵌后端 handler 而不是 Vite。
//
// 直接复用生产模式的 ProxyPrefixes：dev 与生产共用同一张白名单，
// 避免两处配置漂移（例如改 config.yml 的 route.apps_prefix 时漏改）。
//
// 注意 ProxyPrefixes 里已经包含 `/apps` 与 `/c/analysis/apps`，
// 因此应用会话（`/c/analysis/apps/<id>/`）在两种模式下走的是同一个判断。
func shouldRouteToBackend(p string) bool {
	return shouldProxy(p)
}

// proxyUpgrade 把 Upgrade 连接双向搬运到 Vite（用于 HMR WebSocket）。
//
// httputil.ReverseProxy 不处理 Upgrade，这里用 Hijack 手工接管：
// 先在客户端侧完成握手透传，再把两侧连接对拷。
func proxyUpgrade(w http.ResponseWriter, r *http.Request, upstream *url.URL) {
	upstreamConn, err := net.DialTimeout("tcp", upstream.Host, 10*time.Second)
	if err != nil {
		http.Error(w, "无法连接开发服务器: "+err.Error(), http.StatusBadGateway)
		return
	}

	// 把原始请求（含 Upgrade 头）写给 Vite。
	if err := r.Write(upstreamConn); err != nil {
		_ = upstreamConn.Close()
		http.Error(w, "转发 Upgrade 请求失败: "+err.Error(), http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = upstreamConn.Close()
		http.Error(w, "当前 ResponseWriter 不支持 Hijack", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		_ = upstreamConn.Close()
		return
	}

	// 双向对拷；任意一侧结束即整体结束。
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(clientConn, upstreamConn)
		done <- struct{}{}
	}()

	// clientBuf 里可能已经缓存了客户端发来的字节（握手之前就到了），
	// 必须原样发给 Vite，否则 WebSocket 首帧会丢。
	if clientBuf != nil && clientBuf.Reader.Buffered() > 0 {
		if _, err := io.CopyN(upstreamConn, clientBuf, int64(clientBuf.Reader.Buffered())); err != nil {
			_ = clientConn.Close()
			_ = upstreamConn.Close()
			return
		}
	}

	go func() {
		_, _ = io.Copy(upstreamConn, clientConn)
		done <- struct{}{}
	}()

	<-done
	_ = clientConn.Close()
	_ = upstreamConn.Close()
}
