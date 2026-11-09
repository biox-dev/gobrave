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
}

// NewHandler 返回一个 http.Handler：
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
		originalDirector(req)
		// 浏览器带上的是本地外壳的 Host，转发时应改成后端地址。
		req.Host = target.Host
		req.Header.Set("X-Forwarded-Host", req.Host)
		req.Header.Set("X-Forwarded-Proto", "http")
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, e error) {
		http.Error(w, "后端服务不可用: "+e.Error(), http.StatusBadGateway)
	}

	fileServer := http.FileServer(http.FS(assets))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
}

func shouldProxy(p string) bool {
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
