// Command desktop 是 brave 的 Wails v3 桌面客户端入口。
//
// 它复用 gobrave 的前端产物（frontend/web）：
// 本地起一个真实的 http.Server 提供静态文件与反向代理，
// WebView 直接加载 http://127.0.0.1:<随机端口>/，
// 因此 POST body、SSE、WebSocket 行为与浏览器一致。
//
// 当前阶段只负责「打开窗口并正常显示」，后端服务需另行启动（默认 :8084）；
// 后续将改为在 golang 内以类库方式启动后端，届时替换 backend.Manager 的实现即可。
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	desktopbackend "github.com/biox-dev/gobrave/cmd/desktop/backend"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// DesktopService 暴露给前端调用的桌面能力。
// 目前只有后端连接信息查询；后续可加入打开文件、读取本地配置等。
type DesktopService struct {
	manager *desktopbackend.Manager
}

// NewDesktopService 构造服务。
func NewDesktopService(manager *desktopbackend.Manager) *DesktopService {
	return &DesktopService{manager: manager}
}

// BackendStatus 返回后端连接状态，前端可轮询用于展示「后端已连接/未连接」。
func (d *DesktopService) BackendStatus() desktopbackend.Status {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return d.manager.Health(ctx)
}

// BackendBaseURL 返回后端根地址。
func (d *DesktopService) BackendBaseURL() string {
	return d.manager.BaseURL()
}

func main() {
	devURL := flag.String("dev-url", "", "开发模式下直接加载的前端地址（如 http://localhost:5173）")
	flag.Parse()

	cfg := desktopbackend.LoadConfig()
	manager := desktopbackend.NewManager(cfg)

	// 启动时探测一次，仅用于日志提示；连接失败不阻塞窗口打开，
	// 前端仍可展示界面，并通过 BackendStatus 查询实际状态。
	if st := manager.Health(context.Background()); st.Healthy {
		log.Printf("[desktop] 后端连接正常: %s", st.BaseURL)
	} else {
		log.Printf("[desktop] 后端暂不可用(%s): %s", st.BaseURL, st.Message)
	}

	pageURL := *devURL
	if pageURL == "" {
		// 在真实 HTTP 端口上提供页面，而不是走 Wails 的 wails:// 自定义协议。
		// 原因：WebKitGTK 的自定义协议 handler 不传递 POST 请求体。
		local, err := desktopbackend.StartLocalServer(desktopbackend.NewHandler(desktopbackend.HandlerOptions{
			Assets:         resolveWebFS(),
			BackendBaseURL: manager.BaseURL(),
		}))
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("[desktop] 本地页面服务: %s", local.URL())
		pageURL = local.URL() + "/"

		// 应用退出后关闭本地 HTTP 服务。
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = local.Shutdown(shutdownCtx)
		}()
	}

	app := application.New(application.Options{
		Name:        "Brave Desktop",
		Description: "Brave 数据分析工作台桌面客户端",
		Services: []application.Service{
			application.NewService(NewDesktopService(manager)),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:  "Brave",
		Width:  1440,
		Height: 900,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 40,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(6, 7, 15),
		// 指向本地 HTTP 服务，前端因此处于 http:// origin，
		// getPathname() 会得到正常地址，POST body/SSE/WS 均可用。
		URL: pageURL,
	})

	// 强制 Ctrl+C 退出：部分平台 WebView 会抢占终端信号。
	go handleSignals(app)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// handleSignals 在收到中断信号时退出应用，
// 避免本地 HTTP 服务残留（defer 在 normal exit 时执行）。
func handleSignals(app *application.App) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	<-signals
	log.Printf("[desktop] 收到退出信号，正在关闭窗口...")
	app.Quit()
}
