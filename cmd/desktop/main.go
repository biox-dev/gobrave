// Command desktop 是 brave 的 Wails v3 桌面客户端入口。
//
// 它复用 gobrave 的前端产物（frontend/web），并在**同一进程内**
// 通过 golang 类库调用启动后端（internal/appsrv），无需额外进程；
// 本地再起一个真实的 http.Server 提供静态文件与反向代理，
// WebView 直接加载 http://127.0.0.1:<随机端口>/，
// 因此 POST body、SSE、WebSocket 行为与浏览器一致。
//
// 构建/运行（必须带 gtk3 标签，宿主 GTK 4.6.9 低于 Wails 默认要求的 4.10）：
//
//	go run -tags "gtk3 embed_frontend" ./cmd/desktop
//	./cmd/desktop/build.sh
//
// 不带 gtk3 时由 main_no_gtk3.go 提供占位 main，保证 `go build ./...` 可用。
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	desktopbackend "github.com/biox-dev/gobrave/cmd/desktop/backend"
	desktopwindows "github.com/biox-dev/gobrave/cmd/desktop/windows"
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	apprepo "github.com/biox-dev/gobrave/internal/application/repository"
	"github.com/biox-dev/gobrave/internal/appsrv"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/handler"
	"github.com/biox-dev/gobrave/internal/middleware"
	"github.com/biox-dev/gobrave/internal/runtime"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/wailsapp/wails/v3/pkg/application"
)

// 桌面版内置 ADMIN 用户。
const (
	adminUsername = "admin"
	adminEmail    = "admin@desktop.local"
)

// adminUser 是注入到请求上下文的当前用户（由 ensureAdminUser 初始化）。
var adminUser *types.User

// desktopMarkerJS 在页面加载完成后设置一个全局标记，供前端识别「当前跑在
// Wails 桌面壳里」。
//
// 为什么不用 window._wails 判断：那是 Wails runtime.js 定义的，而桌面端为了
// 保证 POST body / WebSocket 正常，WebView 加载的是本文件启动的本地
// http.Server，页面里并不会自动引入 runtime.js，所以 _wails 永远不存在。
// WebviewWindowOptions.JS 在 WindowLoadFinished 时执行，早于 React 挂载，
// 因此前端读取该标记一定是安全的。
const desktopMarkerJS = `window.__BRAVE_DESKTOP__ = true;`

// ensureAdminUser 保证数据库中存在内置 ADMIN 用户，并为首次创建的账号执行初始化。
// 免认证模式下每次请求都会读取该用户，因此它必须是持久化记录，
// 否则依赖 userID 外键的业务（项目、数据集、会话）会失败。
func ensureAdminUser() (*types.User, error) {
	var userRepo interfaces.UserRepository
	var authHandler *handler.AuthHandler
	if err := runtime.GetContainer().Invoke(func(
		r interfaces.UserRepository,
		h *handler.AuthHandler,
	) error {
		userRepo = r
		authHandler = h
		return nil
	}); err != nil {
		return nil, err
	}

	ctx := context.Background()

	user, err := userRepo.GetUserByUsername(ctx, adminUsername)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, apprepo.ErrUserNotFound) {
		return nil, err
	}

	// 首次启动：写入固定 BASIC 用户；密码校验在免认证模式下不生效。
	user = &types.User{
		ID:          utils.GenerateID(),
		Username:    adminUsername,
		Email:       adminEmail,
		IsActive:    true,
		AgentConfig: agenttypes.DefaultUserAgentConfig(),
	}
	if err := userRepo.CreateUser(ctx, user); err != nil {
		return nil, err
	}

	authHandler.InitUser(ctx, user)
	return user, nil
}

// DesktopService 暴露给前端调用的桌面能力。
type DesktopService struct {
	manager *desktopbackend.Manager
	backend *appsrv.Server
	windows *desktopwindows.Manager
}

// NewDesktopService 构造服务。
//
// windows 在 app.Run() 之前无法创建窗口，因此这里只持有构造好的管理器，
// 真正创建窗口发生在 app.Run() 之后（由前端发起调用时）。
func NewDesktopService(
	manager *desktopbackend.Manager,
	backend *appsrv.Server,
	windows *desktopwindows.Manager,
) *DesktopService {
	return &DesktopService{manager: manager, backend: backend, windows: windows}
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

// --- 多窗口能力（前端 `window.DesktopService.*`） ---
//
// 前端统一通过 `openWindow(url)` 调用 OpenWindow：
//   - 本应用页面（同源）→ 新建原生窗口；已打开过则还原并置顶；
//   - 外部 http/https → 交给系统默认浏览器（混合模式）。

// OpenWindow 打开 URL：同源页面开原生窗口，外部链接走系统浏览器。
func (d *DesktopService) OpenWindow(rawURL string) (desktopwindows.OpenResult, error) {
	if d.windows == nil {
		return desktopwindows.OpenResult{}, errors.New("窗口管理器未初始化")
	}
	return d.windows.Open(rawURL)
}

// OpenExternal 强制用系统默认浏览器打开（即使同源）。
func (d *DesktopService) OpenExternal(rawURL string) error {
	if d.windows == nil {
		return errors.New("窗口管理器未初始化")
	}
	return d.windows.OpenExternal(rawURL)
}

// FocusWindow 激活已打开的同源窗口，返回是否命中。
func (d *DesktopService) FocusWindow(rawURL string) (bool, error) {
	if d.windows == nil {
		return false, errors.New("窗口管理器未初始化")
	}
	return d.windows.Focus(rawURL)
}

// CloseWindow 关闭指定 URL 对应的窗口（不存在时静默成功）。
func (d *DesktopService) CloseWindow(rawURL string) error {
	if d.windows == nil {
		return errors.New("窗口管理器未初始化")
	}
	return d.windows.Close(rawURL)
}

// ListWindows 返回当前打开的应用窗口地址列表（调试用）。
func (d *DesktopService) ListWindows() []string {
	if d.windows == nil {
		return nil
	}
	return d.windows.List()
}

func main() {
	devURL := flag.String("dev-url", "", "开发模式下直接加载的前端地址（如 http://localhost:5173）")
	port := flag.Int("port", desktopbackend.DefaultPort, "内嵌后端监听端口（0 表示随机空闲端口）")
	flag.Parse()

	// 0) 桌面版为单机模式：关闭 API 权限校验，所有请求以内置 ADMIN 用户身份执行。
	//    用户延迟解析（容器在 appsrv.New 后才就绪），由 step 1 完成落库。
	middleware.SetBypassUserProvider(func() *types.User { return adminUser })

	// 1) 以库函数方式启动后端：加载配置 → 构建 DI 容器 → 监听端口。
	//    Address 用 127.0.0.1:<port>，避免把服务暴露到外网。
	//    桌面端传入 LoadOptions{Desktop: true}：Windows 上默认开启运行时预置
	//    （container.docker.provision.enabled），并使用 host_port 网络模式；
	//    config.yml 中的显式配置仍然优先生效。
	cfg, err := config.LoadConfigWith(config.LoadOptions{Desktop: true})
	if err != nil {
		log.Fatalf("[desktop] 加载配置失败: %v", err)
	}
	srv, err := appsrv.New(appsrv.Options{
		Config:        cfg,
		Address:       net.JoinHostPort(desktopbackend.DefaultHost, strconv.Itoa(*port)),
		InitSnowflake: true,
		HandleSignals: false, // 生命周期由本进程控制
	})
	if err != nil {
		log.Fatalf("[desktop] 后端初始化失败: %v", err)
	}

	// 容器就绪：确保内置 ADMIN 用户已持久化并完成初始化（默认项目）。
	if adminUser, err = ensureAdminUser(); err != nil {
		log.Fatalf("[desktop] 初始化内置用户失败: %v", err)
	}
	log.Printf("[desktop] 已关闭 API 认证，当前用户 %d (%s)", adminUser.ID, adminUser.Username)

	if err := srv.Start(); err != nil {
		log.Fatalf("[desktop] 后端启动失败: %v", err)
	}
	log.Printf("[desktop] 内嵌后端已启动: http://%s", srv.Address())

	// 后端地址以实际监听地址为准（--port=0 时是随机端口）。
	backendCfg := desktopbackend.Config{
		Scheme: "http",
		Host:   desktopbackend.DefaultHost,
		Port:   portOf(srv.Address()),
	}
	manager := desktopbackend.NewManager(backendCfg)
	// 让 /health 探针与反代都指向真实地址。
	manager.SetBaseURL("http://" + srv.Address())

	// 2) 停止后端：窗口关闭 / 进程退出时调用，保证端口与容器资源释放。
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if stopErr := srv.Shutdown(ctx); stopErr != nil {
			log.Printf("[desktop] 后端关闭异常: %v", stopErr)
		}
	}()

	pageURL := *devURL

	// 共享的 HTTP transport：
	//   - 交给 application.New，由它负责 Start()（绑定 messageProcessor）；
	//   - 同时取出它的中间件挂到本地 http.Server 上（见下面的 NewHandler）。
	// 二者必须是**同一个实例**：中间件依赖 Start() 注入的 messageProcessor，
	// 另建一个未 Start 的 transport 调用 Handler() 会因为 messageProcessor
	// 为 nil 而 panic。
	transport := application.NewHTTPTransport()

	app := application.New(application.Options{
		Name:        "Brave Desktop",
		Description: "Brave 数据分析工作台桌面客户端",
		Transport:   transport,
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	// 本地页面服务：静态资源 + 反代后端 + SPA 回退，并挂载 Wails 运行时中间件。
	//
	// 为什么必须挂：Wails 的 HTTP transport 只提供「中间件」，本该挂在 Wails
	// 自己的 AssetServer 上；但桌面端为了让 POST body / WebSocket 正常工作，
	// WebView 加载的是本文件启动的 http.Server，请求不经过 AssetServer。
	// 不挂的话 `/wails/runtime`、`/wails/runtime.js` 会被 SPA 回退吞掉，
	// 前端拿到一段 index.html（表现为「点了没反应 / 只请求到静态资源」）。
	//
	// 开发模式（-dev-url）下同理：WebView 的 origin 变成 Vite，`/wails/runtime`
	// 会打到 Vite（404）。因此这里在 Vite 前面再加一层 DevProxy，由它接管
	// `/wails/*` 并透传其余请求给 Vite（含 HMR 的 WebSocket）。
	var local *desktopbackend.LocalServer
	var devProxy *desktopbackend.DevProxy
	if pageURL == "" {
		var err error
		local, err = desktopbackend.StartLocalServer(desktopbackend.NewHandler(desktopbackend.HandlerOptions{
			Assets:         resolveWebFS(),
			BackendBaseURL: manager.BaseURL(),
			WailsHandler:   transport.Handler(),
		}))
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("[desktop] 本地页面服务: %s", local.URL())
		pageURL = local.URL() + "/"

		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = local.Shutdown(shutdownCtx)
		}()
	} else {
		var err error
		devProxy, err = desktopbackend.StartDevProxy(
			pageURL,
			transport.Handler(),
			desktopbackend.NewHandler(desktopbackend.HandlerOptions{
				Assets:         resolveWebFS(),
				BackendBaseURL: manager.BaseURL(),
			}),
		)
		if err != nil {
			log.Fatalf("[desktop] 开发代理启动失败: %v", err)
		}
		log.Printf("[desktop] 开发代理已启动: %s -> %s", devProxy.URL(), pageURL)
		// WebView 加载代理地址：页面 origin 与 Wails 运行时一致，
		// `/wails/runtime` 才能被 transport 中间件处理。
		pageURL = devProxy.URL() + "/"

		defer func() {
			_ = devProxy.Shutdown()
		}()
	}

	// 窗口管理器需要在 application.New 之后创建（依赖 app 实例；
	// 但真正开窗发生在 app.Run 之后，由前端调用触发，此时已就绪）。
	winManager := desktopwindows.NewManager(desktopwindows.Options{
		App:           app,
		ShellURL:      pageURL,
		TitlePrefix:   "Brave",
		MarkerJS:      desktopMarkerJS,
		DefaultWidth:  1200,
		DefaultHeight: 800,
	})

	// 服务需要在窗口管理器就绪后注册，因此单独 RegisterService
	// （必须在 app.Run 之前调用，之后注册会被丢弃）。
	app.RegisterService(application.NewService(NewDesktopService(manager, srv, winManager)))

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:   "main",
		Title:  "Brave",
		Width:  1440,
		Height: 900,
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 40,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
		BackgroundColour: application.NewRGB(6, 7, 15),
		URL:              pageURL,
		JS:               desktopMarkerJS,
	})

	go handleSignals(app)

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// handleSignals 在收到中断信号时退出应用。
func handleSignals(app *application.App) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	<-signals
	log.Printf("[desktop] 收到退出信号，正在关闭窗口...")
	app.Quit()
}

// portOf 从 "host:port" 中取出端口，解析失败回退默认端口。
func portOf(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return desktopbackend.DefaultPort
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 {
		return desktopbackend.DefaultPort
	}
	return p
}
