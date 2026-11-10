//go:build gtk3

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
	agenttypes "github.com/biox-dev/gobrave/internal/agent/types"
	apprepo "github.com/biox-dev/gobrave/internal/application/repository"
	"github.com/biox-dev/gobrave/internal/appsrv"
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
}

// NewDesktopService 构造服务。
func NewDesktopService(manager *desktopbackend.Manager, backend *appsrv.Server) *DesktopService {
	return &DesktopService{manager: manager, backend: backend}
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
	port := flag.Int("port", desktopbackend.DefaultPort, "内嵌后端监听端口（0 表示随机空闲端口）")
	flag.Parse()

	// 0) 桌面版为单机模式：关闭 API 权限校验，所有请求以内置 ADMIN 用户身份执行。
	//    用户延迟解析（容器在 appsrv.New 后才就绪），由 step 1 完成落库。
	middleware.SetBypassUserProvider(func() *types.User { return adminUser })

	// 1) 以库函数方式启动后端：加载配置 → 构建 DI 容器 → 监听端口。
	//    Address 用 127.0.0.1:<port>，避免把服务暴露到外网。
	srv, err := appsrv.New(appsrv.Options{
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
	cfg := desktopbackend.Config{
		Scheme: "http",
		Host:   desktopbackend.DefaultHost,
		Port:   portOf(srv.Address()),
	}
	manager := desktopbackend.NewManager(cfg)
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
	if pageURL == "" {
		local, err := desktopbackend.StartLocalServer(desktopbackend.NewHandler(desktopbackend.HandlerOptions{
			Assets:         resolveWebFS(),
			BackendBaseURL: manager.BaseURL(),
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
	}

	app := application.New(application.Options{
		Name:        "Brave Desktop",
		Description: "Brave 数据分析工作台桌面客户端",
		Services: []application.Service{
			application.NewService(NewDesktopService(manager, srv)),
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
		URL:              pageURL,
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
