package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
)

// stubStoreService 只实现本测试用到的 GetStoreByID，其余方法由内嵌接口占位（不会被调用）。
type stubStoreService struct {
	interfaces.StoreService
	store *types.Store
}

func (s stubStoreService) GetStoreByID(_ context.Context, _ int64) (*types.Store, error) {
	return s.store, nil
}

func mustWriteFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("PNG-BYTES"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// getStoreImage 直接挂载 handler（绕过认证中间件），返回响应记录器。
// baseDir 为 storage.base_dir，store 目录统一为 <baseDir>/store/<path_name>。
func getStoreImage(t *testing.T, baseDir string, store *types.Store) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	h := &StoreHandler{
		storeService: stubStoreService{store: store},
		cfg:          &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}},
	}
	engine.GET("/api/v1/store/:storeId/image", func(c *gin.Context) {
		c.Set(types.UserIDContextKey.String(), "test-user")
		h.GetStoreImage(c)
	})

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/store/1/image", nil))
	return recorder
}

// store 封面位置固定为 {baseDir}/store/{path_name}/{store_type}/{img}。
func TestGetStoreImageFixedPath(t *testing.T) {
	baseDir := t.TempDir()
	storeDir := utils.GetWorkflowOrScriptStoreDir(baseDir, "wf-1")
	mustWriteFile(t, filepath.Join(storeDir, "workflow", "image.png"))

	recorder := getStoreImage(t, baseDir, &types.Store{ID: 1, PathName: "wf-1", StoreType: "workflow", Img: "image.png"})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	if body := recorder.Body.String(); body != "PNG-BYTES" {
		t.Fatalf("body = %q, want the stored file content", body)
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "image/png") {
		t.Fatalf("content-type = %q, want image/png", contentType)
	}
}

// 文件不存在、Img/PathName 为空、路径越界时都退化成占位图（200 + svg），而不是 404 或读到 base 外的文件。
func TestGetStoreImageFallsBackToPlaceholder(t *testing.T) {
	baseDir := t.TempDir()
	storeDir := utils.GetWorkflowOrScriptStoreDir(baseDir, "wf-1")
	mustWriteFile(t, filepath.Join(storeDir, "workflow", "image.png"))

	cases := map[string]*types.Store{
		"missing file": {ID: 1, PathName: "wf-1", StoreType: "workflow", Img: "missing.png"},
		"empty img":    {ID: 1, PathName: "wf-1", StoreType: "workflow"},
		"empty path":   {ID: 1, StoreType: "workflow", Img: "image.png"},
		"traversal":    {ID: 1, PathName: "wf-1", StoreType: "workflow", Img: "../../etc/passwd"},
	}

	for name, store := range cases {
		t.Run(name, func(t *testing.T) {
			recorder := getStoreImage(t, baseDir, store)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", recorder.Code)
			}
			if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "image/svg+xml") {
				t.Fatalf("content-type = %q, want placeholder svg", contentType)
			}
			if body := recorder.Body.String(); !strings.Contains(body, "No Image") {
				t.Fatalf("body is not the placeholder: %q", body)
			}
		})
	}
}
