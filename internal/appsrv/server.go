// Package appsrv 负责以「库函数调用」方式启动 gobrave 后端服务。
//
// 它把原 main.go 里的启动逻辑（加载配置 → 构建依赖注入容器 → 监听端口 →
// 优雅退出）抽取为可复用的 Server 类型，使命令行入口（main.go）与
// 桌面入口（cmd/desktop）共用同一套代码：
//
//	srv, err := appsrv.New(appsrv.Options{})
//	if err != nil { ... }
//	if err := srv.Start(ctx); err != nil { ... }
//	...
//	srv.Shutdown(context.Background())
//
// 这样桌面端无需拉起子进程，后端与窗口在同一进程内运行，
// 退出时也能保证顺序关闭（先关窗口，再停服务）。
package appsrv

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"time"

	cmd "github.com/biox-dev/gobrave/cmd/server"
	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/container"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/runtime"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
)

// 默认监听参数，与 config.yml 中 server 段的缺省值保持一致。
const (
	// DefaultHost 是未配置 server.host 时的监听地址。
	DefaultHost = "127.0.0.1"
	// DefaultPort 是未配置 server.port 时的监听端口。
	DefaultPort = 8084
	// DefaultShutdownTimeout 是优雅退出的默认超时时间。
	DefaultShutdownTimeout = 30 * time.Second

	defaultListenRetries = 10
	defaultRetryDelay    = 300 * time.Millisecond
)

// Options 控制服务启动方式。
type Options struct {
	// Config 为 nil 时按默认规则加载 config.yml（CLI --config > 环境变量 > 当前目录）。
	// 桌面端可传入预加载的配置，避免重复解析。
	Config *config.Config
	// Address 非空时直接使用该监听地址（如 "127.0.0.1:0"），
	// 否则使用 config 中的 server.host:server.port。
	Address string
	// ListenRetries / RetryDelay 控制端口占用时的重试；0 表示使用默认值。
	ListenRetries int
	RetryDelay    time.Duration
	// HandleSignals 为 true 时服务自行监听 SIGINT/SIGTERM 并优雅退出，
	// 命令行模式使用；嵌入式（桌面）模式应设为 false，由宿主控制生命周期。
	HandleSignals bool
	// InitSnowflake 为 true 时调用 utils.InitSnowflake(1)（进程内只能初始化一次）。
	// 命令行模式使用。
	InitSnowflake bool
}

// Server 是运行中的后端服务实例。
type Server struct {
	cfg     *config.Config
	httpSrv *http.Server
	addr    string
	opts    Options

	mu       sync.Mutex
	started  bool
	stopping bool
	doneCh   chan struct{}
	errCh    chan error
}

// New 加载配置并构建依赖注入容器，但暂不监听端口。
//
// 注意：依赖注入容器是包级全局（internal/runtime），一个进程只能构建一次。
func New(opts Options) (*Server, error) {
	cfg := opts.Config
	if cfg == nil {
		loaded, err := config.LoadConfig()
		if err != nil {
			return nil, fmt.Errorf("failed to load config: %w", err)
		}
		cfg = loaded
	}

	if opts.InitSnowflake {
		utils.InitSnowflake(1)
	}

	// Gin 运行模式直接取自 config.yml 的 server.mode（--gin-mode 已合并进 cfg）。
	// 留空时不设置，交给 gin 自身的 GIN_MODE 环境变量 / 默认值处理。
	if cfg.Server != nil && cfg.Server.Mode != "" {
		gin.SetMode(cfg.Server.Mode)
	}

	container.BuildContainer(runtime.GetContainer(), cfg)

	return &Server{
		cfg:    cfg,
		opts:   opts,
		doneCh: make(chan struct{}),
		errCh:  make(chan error, 1),
	}, nil
}

// Address 返回配置中的监听地址（形如 127.0.0.1:8084）。
func (s *Server) Address() string {
	if s.addr != "" {
		return s.addr
	}
	return listenAddress(s.cfg)
}

// Config 返回已加载的配置。
func (s *Server) Config() *config.Config { return s.cfg }

// Start 启动 HTTP 服务并立即返回。
//
// 监听失败会直接返回错误；成功后服务在后台运行，
// 停止请调用 Shutdown，等待失败退出可调用 Wait。
func (s *Server) Start() error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("server already started")
	}
	s.mu.Unlock()

	var router *gin.Engine
	if err := runtime.GetContainer().Invoke(func(r *gin.Engine) error {
		router = r
		return nil
	}); err != nil {
		return fmt.Errorf("failed to resolve router: %w", err)
	}

	addr := s.opts.Address
	if addr == "" {
		addr = listenAddress(s.cfg)
	}

	retries := s.opts.ListenRetries
	if retries <= 0 {
		retries = defaultListenRetries
	}
	delay := s.opts.RetryDelay
	if delay <= 0 {
		delay = defaultRetryDelay
	}

	listener, err := cmd.ListenWithRetry(addr, retries, delay)
	if err != nil {
		return fmt.Errorf("failed to start server: %w", err)
	}

	s.mu.Lock()
	s.httpSrv = &http.Server{
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	s.addr = listener.Addr().String()
	s.started = true
	s.mu.Unlock()

	logger.Infof(context.Background(), "Server is running at %s", s.addr)

	go func() {
		serveErr := s.httpSrv.Serve(listener)
		if serveErr != nil && serveErr != http.ErrServerClosed {
			logger.Errorf(context.Background(), "server error: %v", serveErr)
			s.errCh <- fmt.Errorf("server error: %w", serveErr)
			return
		}
		close(s.doneCh)
	}()

	if s.opts.HandleSignals {
		go s.handleSignals()
	}

	return nil
}

// handleSignals 在收到中断信号时优雅退出（命令行模式）。
func (s *Server) handleSignals() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, cmd.ShutdownSignals...)
	defer signal.Stop(signals)

	sig := <-signals
	logger.Infof(context.Background(), "Received signal: %v, starting server shutdown...", sig)

	// 第二次信号 → 立即强制关闭所有连接。
	go func() {
		forceSig := <-signals
		logger.Warnf(context.Background(), "Received second signal: %v, forcing shutdown...", forceSig)
		s.mu.Lock()
		if s.httpSrv != nil {
			_ = s.httpSrv.Close()
		}
		s.mu.Unlock()
	}()

	timeout := DefaultShutdownTimeout
	if s.cfg != nil && s.cfg.Server != nil && s.cfg.Server.ShutdownTimeout > 0 {
		timeout = s.cfg.Server.ShutdownTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	s.Shutdown(ctx)
	logger.Info(context.Background(), "Server has exited")
}

// Shutdown 优雅关闭服务。可重复调用（幂等）。
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	srv := s.httpSrv
	if srv == nil || s.stopping {
		s.mu.Unlock()
		return nil
	}
	s.stopping = true
	s.mu.Unlock()

	if ctx == nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.Background(), DefaultShutdownTimeout)
		defer cancel()
	}

	if err := srv.Shutdown(ctx); err != nil {
		logger.Errorf(context.Background(), "Server forced to shutdown: %v", err)
		_ = srv.Close()
		return err
	}
	return nil
}

// Done 返回服务正常结束（或 Shutdown 完成）时关闭的通道。
func (s *Server) Done() <-chan struct{} { return s.doneCh }

// Err 返回服务异常退出时的错误通道（带 1 个缓冲）。
func (s *Server) Err() <-chan error { return s.errCh }

// Wait 阻塞直到服务结束或 ctx 取消。
// 服务异常退出时返回该错误；ctx 取消时返回 ctx.Err()。
func (s *Server) Wait(ctx context.Context) error {
	select {
	case <-s.doneCh:
		return nil
	case err := <-s.errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// listenAddress 根据配置计算监听地址，缺省 127.0.0.1:8084。
func listenAddress(cfg *config.Config) string {
	host, port := DefaultHost, DefaultPort
	if cfg != nil && cfg.Server != nil {
		if h := cfg.Server.Host; h != "" {
			host = h
		}
		if cfg.Server.Port > 0 {
			port = cfg.Server.Port
		}
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// ParseCLIFlags 解析命令行参数（--config/--port/...）并登记为全局覆盖项。
//
// 命令行入口在 flag.Parse() 之后调用它；桌面入口不需要调用，
// 直接使用 Options.Config 或默认配置文件即可。
func ParseCLIFlags() *config.CLIFlags {
	cliFlags := config.ParseCLIFlags()
	flag.Parse()
	config.SetCLIFlags(cliFlags)
	return cliFlags
}
