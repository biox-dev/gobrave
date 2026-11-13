package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/biox-dev/gobrave/internal/agent/tool"
	"github.com/biox-dev/gobrave/internal/middleware"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/gin-gonic/gin"
)

// demoFileIDInput 是测试工具入参。
type demoFileIDInput struct {
	FileID string `json:"file_id"`
}

// newToolCatalogRegistry 构造测试用的工具注册表。
func newToolCatalogRegistry() *tool.Registry {
	return tool.NewRegistryWith(
		tool.NewFunc("read_file_by_id", "按 ID 读取文件",
			tool.Schema("", map[string]any{"file_id": tool.StringProperty("文件 ID")}, "file_id"),
			func(_ context.Context, _ demoFileIDInput) (any, error) { return nil, nil }),
		tool.NewFunc("glob", "按模式查找文件",
			tool.Schema("", map[string]any{"pattern": tool.StringProperty("glob 模式")}, "pattern"),
			func(_ context.Context, _ struct {
				Pattern string `json:"pattern"`
			}) (any, error) {
				return nil, nil
			}),
		tool.NewFunc("mcp_demo_search", "MCP 搜索", tool.Schema("", map[string]any{}),
			func(_ context.Context, _ struct{}) (any, error) { return nil, nil }),
	)
}

// serveToolCatalog 直接挂载 handler（绕过认证中间件），注入登录用户。
func serveToolCatalog(t *testing.T, h *AgentHandler, method, target string) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.GET("/api/v1/agent/tool/list", func(c *gin.Context) {
		c.Set(types.UserIDContextKey.String(), int64(1))
		h.ListTools(c)
	})
	engine.GET("/api/v1/agent/tool/get", func(c *gin.Context) {
		c.Set(types.UserIDContextKey.String(), int64(1))
		h.GetTool(c)
	})

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(method, target, nil))
	return recorder
}

func TestAgentHandlerListTools(t *testing.T) {
	h := &AgentHandler{tools: newToolCatalogRegistry()}

	recorder := serveToolCatalog(t, h, http.MethodGet, "/api/v1/agent/tool/list")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", recorder.Code, recorder.Body.String())
	}

	var got []agentToolInfo
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v (body = %s)", err, recorder.Body.String())
	}
	if len(got) != 3 {
		t.Fatalf("tool count = %d, want 3 (body = %s)", len(got), recorder.Body.String())
	}
	// 按名称升序：glob / mcp_demo_search / read_file_by_id。
	wantNames := []string{"glob", "mcp_demo_search", "read_file_by_id"}
	for i, item := range got {
		if item.Name != wantNames[i] {
			t.Fatalf("tool[%d].name = %q, want %q", i, item.Name, wantNames[i])
		}
		if item.Description == "" {
			t.Fatalf("tool %q description is empty", item.Name)
		}
		if len(item.InputSchema) == 0 {
			t.Fatalf("tool %q input_schema is empty", item.Name)
		}
	}
}

func TestAgentHandlerListToolsWithoutRegistry(t *testing.T) {
	h := &AgentHandler{}

	recorder := serveToolCatalog(t, h, http.MethodGet, "/api/v1/agent/tool/list")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if body := recorder.Body.String(); body != "[]" {
		t.Fatalf("body = %q, want empty array", body)
	}
}

func TestAgentHandlerGetTool(t *testing.T) {
	h := &AgentHandler{tools: newToolCatalogRegistry()}

	recorder := serveToolCatalog(t, h, http.MethodGet, "/api/v1/agent/tool/get?id=read_file_by_id")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", recorder.Code, recorder.Body.String())
	}

	var got agentToolInfo
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode body: %v (body = %s)", err, recorder.Body.String())
	}
	if got.Name != "read_file_by_id" {
		t.Fatalf("tool name = %q, want read_file_by_id", got.Name)
	}
	if got.Description == "" {
		t.Fatalf("description is empty")
	}
	if _, ok := got.InputSchema["properties"]; !ok {
		t.Fatalf("input_schema = %v, want properties", got.InputSchema)
	}
}

func TestAgentHandlerGetToolNotFound(t *testing.T) {
	h := &AgentHandler{tools: newToolCatalogRegistry()}

	recorder := serveToolCatalog(t, h, http.MethodGet, "/api/v1/agent/tool/get?id=missing_tool")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", recorder.Code, recorder.Body.String())
	}
}
