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

	"github.com/biox-dev/gobrave/internal/appsrv"
	"github.com/biox-dev/gobrave/internal/logger"
)

// main 是命令行入口：以独立进程方式启动后端服务。
// 启动逻辑与桌面端（cmd/desktop）共用 internal/appsrv，
// 因此两边的路由、中间件、优雅退出行为完全一致。
func main() {
	// 解析命令行参数（覆盖 config.yml），必须在加载配置前完成。
	appsrv.ParseCLIFlags()

	srv, err := appsrv.New(appsrv.Options{
		HandleSignals: true,
		InitSnowflake: true,
	})
	if err != nil {
		logger.Fatalf(context.Background(), "Failed to run application: %v", err)
	}

	if err := srv.Start(); err != nil {
		logger.Fatalf(context.Background(), "Failed to run application: %v", err)
	}

	// 阻塞直到服务退出（收到信号优雅关闭，或异常退出）。
	if err := srv.Wait(context.Background()); err != nil {
		logger.Fatalf(context.Background(), "Failed to run application: %v", err)
	}
}
