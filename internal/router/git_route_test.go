package router

import (
	"testing"

	"github.com/biox-dev/gobrave/internal/handler"
	"github.com/gin-gonic/gin"
)

// TestRegisterGitRoutes 只做「路由能注册」的回归：gin 在同一层级混用静态段与参数段
// （如 /script/delete/:scriptId 与 /script/:scriptId/git-diff）时，参数名不一致会在
// 注册期直接 panic。这里把脚本/工作流路由与本组 git 路由注册到同一个 group，
// 保证服务启动不会因路由冲突失败，并确认两个新接口确实挂上了。
func TestRegisterGitRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	v1 := engine.Group("/api/v1")

	RegisterWorkflowRoutes(v1, &handler.WorkflowHandler{})
	RegisterGitRoutes(v1, &handler.GitHandler{})

	want := map[string]bool{
		"/api/v1/script/:scriptId/git-diff":     false,
		"/api/v1/workflow/:workflowId/git-diff": false,
	}
	for _, route := range engine.Routes() {
		if _, ok := want[route.Path]; ok {
			want[route.Path] = true
		}
	}
	for path, found := range want {
		if !found {
			t.Fatalf("route %s is not registered", path)
		}
	}
}
