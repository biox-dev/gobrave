package backend

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// LocalServer 在 127.0.0.1 上启动一个真实 HTTP 服务，供 WebView 加载页面。
//
// 为什么不直接用 Wails 的 AssetServer（wails:// 自定义协议）：
// WebKitGTK 的 URI-scheme handler 不会把 POST 请求体传给 handler
// （见 Wails internal/assetserver/assetserver_webview.go），
// 导致所有 POST 请求到达后端时 ContentLength=0，
// 服务端 ShouldBindJSON 读不到数据而返回 EOF。
//
// 改用真实 HTTP 端口后，POST/PUT body、SSE、WebSocket 全部走标准 HTTP，
// 前端也处于正常的 http:// origin 下，行为与浏览器一致。
type LocalServer struct {
	srv      *http.Server
	listener net.Listener
	addr     string
}

// StartLocalServer 启动本机 HTTP 服务，返回监听地址（如 http://127.0.0.1:39217）。
func StartLocalServer(handler http.Handler) (*LocalServer, error) {
	// 监听 127.0.0.1 的随机空闲端口，避免与其他程序冲突。
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("监听本地端口失败: %w", err)
	}

	// 必须显式设置连接超时：WebKitGTK 会把空闲的 keep-alive 连接放进自己的
	// 复用池，若服务端（Go）不做主动回收，WebKit 可能复用一个对端已经静默
	// 关闭的连接，读到 EOF 后触发 WebLoaderStrategy::internallyFailedLoadTimerFired，
	// 表现为「页面偶发自我刷新」。
	//
	// ReadTimeout / WriteTimeout 保持 0：SSE、WebSocket 是长连接，设了会被
	// 按整条连接计时而中断。IdleTimeout 只作用于「空闲连接」，由服务端在
	// 30s 后主动发 Connection: close 并关闭，早于 WebKit 自己的空闲回收，
	// 从而避免复用失效连接。
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       0,
		WriteTimeout:      0,
		IdleTimeout:       30 * time.Second,
	}

	ls := &LocalServer{
		srv:      srv,
		listener: ln,
		addr:     "http://" + ln.Addr().String(),
	}

	go func() {
		if serveErr := srv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			// 端口在应用关闭时正常返回 ErrServerClosed，其余情况仅记录。
			fmt.Printf("[desktop] 本地 HTTP 服务退出: %v\n", serveErr)
		}
	}()

	return ls, nil
}

// URL 返回服务根地址，例如 http://127.0.0.1:39217。
func (s *LocalServer) URL() string { return s.addr }

// Shutdown 关闭本地服务。
func (s *LocalServer) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}
