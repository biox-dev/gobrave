package backend

import (
	"io/fs"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"
)

// debugProxy 为 true 时打印每个被转发的请求，便于排查前端请求是否到达外壳。
// 通过 BRAVE_DESKTOP_DEBUG=1 开启。
var debugProxy = os.Getenv("BRAVE_DESKTOP_DEBUG") == "1"

// ProxyPrefixes 是需要转发到后端（或由后端处理的）路径前缀。
//
// 该清单与 gobrave 的 internal/router/router.go 中 mountFrontendStatic
// 的白名单保持一致：凡是后端负责的路径，桌面外壳都不应该在本地前端资源里找文件。
var ProxyPrefixes = []string{
	"/api",
	"/health",
	"/swagger",
	"/docs",
	"/audio",
	"/images",
	"/images-data",
	"/brave-api",
	"/container",
	"/apps",
	"/onlyoffice",
	"/go-onlyoffice",
	"/data-analysis",
	"/data-project",
	"/c/analysis/apps",
}

// HandlerOptions 描述前端静态资源与后端地址。
type HandlerOptions struct {
	// Assets 是以 index.html 所在目录为根的前端文件系统。
	Assets fs.FS
	// BackendBaseURL 是反向代理的目标地址，例如 http://127.0.0.1:8084。
	BackendBaseURL string
	// WailsHandler 是 Wails 运行时的中间件（由 HTTPTransport.Handler() 提供）。
	//
	// 为什么必须注入：
	// Wails v3 的 HTTP transport 只提供「中间件」，它本该挂在 Wails 自己的
	// AssetServer 上；而桌面端为了让 POST body / WebSocket 正常工作，WebView
	// 加载的是我们自己的本地 http.Server，请求根本不经过 Wails 的 AssetServer。
	// 如果不在本地 server 上显式挂载它，`/wails/runtime`、`/wails/runtime.js`
	// 会一路穿透到 SPA 回退，前端拿到的是一段 index.html 而不是绑定结果，
	// 表现为「调用没有报错但什么也没发生」。
	WailsHandler func(next http.Handler) http.Handler
}

// NewHandler 返回一个 http.Handler：
//   - `/wails/*` 交给 Wails 运行时中间件（runtime.js / 绑定调用）；
//   - 命中 ProxyPrefixes 的请求反向代理到后端；
//   - 其余请求从 Assets 里取静态文件；
//   - 找不到且看起来像路由（无扩展名）时回退到 index.html，支持 SPA 路由。
func NewHandler(opts HandlerOptions) http.Handler {
	assets := opts.Assets

	target, err := url.Parse(opts.BackendBaseURL)
	if err != nil || target.Host == "" {
		// baseURL 由 Config 构造，正常不会失败；退化为纯静态服务。
		target = &url.URL{}
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		// 记下客户端看到的地址，稍后写进 X-Forwarded-*。
		clientHost := req.Host
		originalDirector(req)

		// http.Transport 用 req.Host 作为 Host 头；这里是本机回环转发，
		// 保留客户端 Host 更接近真实浏览器语义（后端不依赖它做虚拟主机）。
		req.Host = clientHost

		// 关键：后端的应用会话反代（internal/handler/proxy.go 的
		// rewriteRedirectLocation）会读取 X-Forwarded-Host 改写 302 的
		// Location。必须传「客户端实际访问的地址」，否则应用容器返回的
		// 绝对重定向会把浏览器导到后端端口（绕过外壳，丢失同源与 /wails）。
		req.Header.Set("X-Forwarded-Host", clientHost)
		req.Header.Set("X-Forwarded-Proto", "http")
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		http.Error(w, "后端服务不可用: "+e.Error(), http.StatusBadGateway)
	}

	fileServer := http.FileServer(http.FS(assets))

	// 所有请求的最终处理：静态资源 + 反代 + SPA 回退。
	appHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path

		if shouldProxy(p) {
			if debugProxy {
				log.Printf("[proxy] %s %s -> %s (ContentLength=%d, TransferEncoding=%v, HasBody=%v)",
					r.Method, p, target.String()+p, r.ContentLength, r.TransferEncoding, r.Body != nil)
			}
			proxy.ServeHTTP(w, r)
			return
		}

		// 尝试命中真实静态文件。
		name := strings.TrimPrefix(path.Clean(p), "/")
		if name != "" {
			if info, statErr := fs.Stat(assets, name); statErr == nil && !info.IsDir() {
				setFrontendCacheHeaders(w, p)
				fileServer.ServeHTTP(w, r)
				return
			}
		}

		// 目录或根路径交给 FileServer（会返回 index.html）。
		if name == "" || strings.HasSuffix(p, "/") {
			setFrontendCacheHeaders(w, "/index.html")
			fileServer.ServeHTTP(w, r)
			return
		}

		// 形如 /a/b/c 的路由：回退到 index.html；带扩展名的资源则保持 404。
		if path.Ext(p) == "" {
			serveIndex(w, r, assets)
			return
		}

		http.NotFound(w, r)
	})

	// Wails 运行时中间件要包在最外层：它只接管 /wails/runtime 与
	// /wails/runtime.js、/wails/transport.js，其余请求 next.ServeHTTP 继续走
	// 上面的 appHandler。没有注入时退化为纯 appHandler。
	if opts.WailsHandler != nil {
		return opts.WailsHandler(appHandler)
	}
	return appHandler
}

// shouldProxy 判断路径是否必须转发到后端。
//
// 例外：`/wails/*` 是 Wails 运行时自己的端点（runtime.js / transport.js /
// 绑定调用 POST /wails/runtime），必须留给 Wails 的资产服务处理；
// 一旦被反代到 gin 后端，前端所有 `DesktopService.*` 调用都会 404。
func shouldProxy(p string) bool {
	if p == "/wails" || strings.HasPrefix(p, "/wails/") {
		return false
	}
	for _, prefix := range ProxyPrefixes {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

func serveIndex(w http.ResponseWriter, r *http.Request, assets fs.FS) {
	data, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		http.Error(w, "index.html 不存在，请先构建前端", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
	_, _ = w.Write(data)
}

// setFrontendCacheHeaders 与后端静态服务保持一致：
// Vite 产物 /assets/* 文件名带 hash，可长期缓存；其余每次都需要 revalidate。
func setFrontendCacheHeaders(w http.ResponseWriter, p string) {
	if strings.HasPrefix(p, "/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	w.Header().Set("Cache-Control", "no-cache, must-revalidate")
}
