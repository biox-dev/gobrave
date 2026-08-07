// @title           AiAgentGo API
// @version         1.0
// @description     AiAgentGo 是一个基于 Go 语言构建的 AI Agent 框架，旨在提供高性能、可扩展的解决方案，帮助开发者快速构建智能代理应用。
// @termsOfService  http://swagger.io/terms/
//
// @contact.name   AiAgentGo Github
// @contact.url    https://github.com/minebiome/ai-agent-go
//
// @BasePath  /api/v1
//
// @securityDefinitions.apikey Bearer
// @in header
// @name Authorization
// @description 用户登录认证：输入 Bearer {token} 格式的 JWT 令牌
//
// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name X-API-Key
// @description 租户身份认证：输入 sk- 开头的 API Key
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gobravedev/gobrave/internal/config"
	"github.com/gobravedev/gobrave/internal/container"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/runtime"
	"github.com/gobravedev/gobrave/internal/utils"
)

func main() {
	// Parse command-line flags (overrides config.yml).
	cliFlags := config.ParseCLIFlags()
	flag.Parse()
	config.SetCLIFlags(cliFlags)

	utils.InitSnowflake(1)
	if os.Getenv("GIN_MODE") == "release" {
		gin.SetMode(gin.ReleaseMode)
	} else {
		gin.SetMode(gin.DebugMode)
	}
	c := container.BuildContainer(runtime.GetContainer())

	err := c.Invoke(func(
		cfg *config.Config,
		router *gin.Engine,
		// grpcServer *grpc.Server,

	) error {

		// mixedHandler := grpcHandlerFunc(grpcServer, router)
		// server := &http.Server{
		// 	Handler: h2c.NewHandler(mixedHandler, &http2.Server{}),
		// }
		server := &http.Server{
			Handler: router,
		}

		addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
		listener, err := listenWithRetry(addr, 10, 300*time.Millisecond)
		if err != nil {
			return fmt.Errorf("failed to start server: %v", err)
		}

		ctx, done := context.WithCancel(context.Background())

		signals := make(chan os.Signal, 1)
		signal.Notify(signals, shutdownSignals...)

		go func() {
			sig := <-signals
			logger.Infof(context.Background(), "Received signal: %v, starting server shutdown...", sig)

			// Close listener first to release port immediately,
			// so the next process can bind during our graceful drain.
			listener.Close()

			shutdownTimeout := cfg.Server.ShutdownTimeout
			if shutdownTimeout == 0 {
				shutdownTimeout = 30 * time.Second
			}
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer shutdownCancel()

			// Second signal → force close all connections immediately
			go func() {
				sig := <-signals
				logger.Warnf(context.Background(), "Received second signal: %v, forcing shutdown...", sig)
				server.Close()
			}()

			if err := server.Shutdown(shutdownCtx); err != nil {
				logger.Errorf(context.Background(), "Server forced to shutdown: %v", err)
				server.Close()
			}

			logger.Info(context.Background(), "Cleaning up resources...")
			// errs := resourceCleaner.Cleanup(shutdownCtx)
			// if len(errs) > 0 {
			// 	logger.Errorf(context.Background(), "Errors occurred during resource cleanup: %v", errs)
			// }
			logger.Info(context.Background(), "Server has exited")
			done()
		}()

		logger.Infof(context.Background(), "Server is running at %s", addr)
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			return fmt.Errorf("server error: %v", err)
		}

		<-ctx.Done()

		return nil

	})
	if err != nil {
		logger.Fatalf(context.Background(), "Failed to run application: %v", err)
	}

	// port := os.Getenv("PORT")
	// if port == "" {
	// 	port = "8082"
	// }

	// webFS, err := fs.Sub(static.Files, "web")
	// if err != nil {
	// 	log.Fatalf("failed to load embedded web assets: %v", err)
	// }

	// http.Handle("/", http.FileServer(http.FS(webFS)))

	// addr := ":" + port
	// log.Printf("server listening on %s", addr)
	// if err := http.ListenAndServe(addr, nil); err != nil {
	// 	log.Fatalf("server stopped: %v", err)
	// }
}
