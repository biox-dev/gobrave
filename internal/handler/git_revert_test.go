package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/middleware"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
)

// serveGitRevert 直接挂载 handler（绕过认证中间件），以 POST + JSON body 请求。
func serveGitRevert(t *testing.T, serve func(c *gin.Context), route, requestPath, body string) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.POST(route, func(c *gin.Context) {
		c.Set(types.UserIDContextKey.String(), "test-user")
		serve(c)
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, requestPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, req)
	return recorder
}

// RevertScriptFile 应把脚本目录里指定文件的未提交改动回退到本地 HEAD。
func TestRevertScriptGitFile(t *testing.T) {
	baseDir := t.TempDir()
	scriptDir := utils.GetScriptFileDir(baseDir, "p1", "s1")
	mainPath := filepath.Join(scriptDir, "main.py")
	mustWriteScriptFile(t, mainPath, "print(1)\n")
	mustGitRepo(t, scriptDir)
	// 提交后再改一次：工作区留下未提交改动。
	mustWriteScriptFile(t, mainPath, "print(2)\n")

	h := &GitHandler{
		workflowService: stubGitWorkflowService{script: &types.Script{ID: 1, ScriptID: "s1", ProjectID: 7}},
		projectService:  stubGitProjectService{project: &types.Project{ID: 7, ProjectID: "p1"}},
		storeService:    stubStoreService{},
		cfg:             &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}},
	}

	recorder := serveGitRevert(t, h.RevertScriptFile,
		"/api/v1/script/:scriptId/git-revert-file",
		"/api/v1/script/1/git-revert-file",
		`{"path":"main.py"}`,
	)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var resp gitRevertFileResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, recorder.Body.String())
	}
	if resp.Entity != "script" || resp.ID != "s1" || resp.Path != "main.py" || !resp.Reverted {
		t.Fatalf("response = %+v, want script/s1/main.py reverted", resp)
	}

	content, err := os.ReadFile(mainPath)
	if err != nil {
		t.Fatalf("read main.py: %v", err)
	}
	if string(content) != "print(1)\n" {
		t.Fatalf("content after revert = %q, want HEAD content", string(content))
	}
	if diff := utils.ReadGitDiff(scriptDir, ""); diff.Worktree.HasChanges {
		t.Fatalf("worktree still dirty after revert: %+v", diff.Worktree)
	}
}

// 路径缺失 / 无可撤回内容 / 非法 body 应分别转成 404 / 404 / 400。
func TestRevertScriptGitFileErrors(t *testing.T) {
	baseDir := t.TempDir()
	scriptDir := utils.GetScriptFileDir(baseDir, "p1", "s1")
	mustWriteScriptFile(t, filepath.Join(scriptDir, "main.py"), "print(1)\n")
	mustGitRepo(t, scriptDir)

	h := &GitHandler{
		workflowService: stubGitWorkflowService{script: &types.Script{ID: 1, ScriptID: "s1", ProjectID: 7}},
		projectService:  stubGitProjectService{project: &types.Project{ID: 7, ProjectID: "p1"}},
		storeService:    stubStoreService{},
		cfg:             &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}},
	}

	cases := []struct {
		name string
		body string
		want int
	}{
		{"missing file", `{"path":"missing.py"}`, http.StatusNotFound},
		{"empty path", `{"path":"   "}`, http.StatusBadRequest},
		{"invalid body", `not-json`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := serveGitRevert(t, h.RevertScriptFile,
				"/api/v1/script/:scriptId/git-revert-file",
				"/api/v1/script/1/git-revert-file",
				tc.body,
			)
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d (body=%s)", recorder.Code, tc.want, recorder.Body.String())
			}
		})
	}
}
