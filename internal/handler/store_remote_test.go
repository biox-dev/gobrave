package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/middleware"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// stubStoreRemoteService 让 GetStoreByID 返回预置 store，其余方法由内嵌接口占位（不会被调用）。
type stubStoreRemoteService struct {
	interfaces.StoreService
	store    *types.Store
	getCalls int
}

func (s *stubStoreRemoteService) GetStoreByID(_ context.Context, id int64) (*types.Store, error) {
	s.getCalls++
	if s.store == nil || s.store.ID != id {
		return nil, gorm.ErrRecordNotFound
	}
	return s.store, nil
}

// newPublishStoreRemoteFixture 在临时 base_dir 下准备一个 store 裸仓库：
// 目录布局与生产一致（base_dir/store/<path_name>），并返回 store 记录与 handler。
func newPublishStoreRemoteFixture(t *testing.T) (*types.Store, *StoreHandler, string) {
	t.Helper()

	baseDir := t.TempDir()
	store := &types.Store{ID: 12, StoreType: "script", Name: "demo", PathName: "test-store"}
	storeDir := utils.GetWorkflowOrScriptStoreDir(baseDir, store.PathName)
	if _, err := utils.EnsureBareGitRepo(storeDir); err != nil {
		t.Fatalf("EnsureBareGitRepo: %v", err)
	}

	svc := &stubStoreRemoteService{store: store}
	handler := &StoreHandler{
		storeService: svc,
		cfg:          &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}},
	}
	return store, handler, storeDir
}

// postPublishStoreRemote 直接挂载 handler（绕过认证中间件），返回响应记录器。
func postPublishStoreRemote(t *testing.T, h *StoreHandler, body string) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.POST("/api/v1/store/publish-remote", func(c *gin.Context) {
		c.Set(types.UserIDContextKey.String(), "test-user")
		h.PublishStoreRemote(c)
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/store/publish-remote", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, req)
	return recorder
}

// 首次发布：url（trim 后）被写成 store 裸仓库的 remote（按主机名命名为 github），
// 不再落库，且如实返回 published=false（push 未实现）。
func TestPublishStoreRemoteAddsGitRemote(t *testing.T) {
	_, handler, storeDir := newPublishStoreRemoteFixture(t)
	svc := handler.storeService.(*stubStoreRemoteService)

	recorder := postPublishStoreRemote(t, handler, `{"store_id":"12","url":"  https://github.com/owner/repo.git  "}`)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", recorder.Code, recorder.Body.String())
	}
	if svc.getCalls != 1 {
		t.Fatalf("GetStoreByID calls = %d, want 1", svc.getCalls)
	}

	var resp struct {
		StoreID     string            `json:"store_id"`
		URL         string            `json:"url"`
		Remote      string            `json:"remote"`
		RemoteAdded bool              `json:"remote_added"`
		Published   bool              `json:"published"`
		Remotes     []utils.GitRemote `json:"remotes"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.StoreID != "12" {
		t.Fatalf("resp store_id = %q, want \"12\"", resp.StoreID)
	}
	if resp.URL != "https://github.com/owner/repo.git" {
		t.Fatalf("resp url = %q, want the trimmed url", resp.URL)
	}
	if resp.Remote != "github" {
		t.Fatalf("resp remote = %q, want github", resp.Remote)
	}
	if !resp.RemoteAdded {
		t.Fatal("resp remote_added = false, want true on first publish")
	}
	if resp.Published {
		t.Fatal("resp published = true, want false (push not implemented yet)")
	}

	// 仓库配置里应该真的多了这个 remote。
	remotes := utils.ReadGitRemotes(storeDir)
	if len(remotes) != 1 || remotes[0].Name != "github" || remotes[0].URLs[0] != "https://github.com/owner/repo.git" {
		t.Fatalf("store remotes = %+v, want a single github remote", remotes)
	}
	if len(resp.Remotes) != 1 || resp.Remotes[0].Name != "github" {
		t.Fatalf("resp remotes = %+v, want the newly added github remote", resp.Remotes)
	}
}

// 同一个 url 再次发布：跳过添加（remote_added=false），remote 数量不变；
// 不同主机（gitee）则新增第二个 remote，支撑「一次发布到多个远端」。
func TestPublishStoreRemoteSkipsExistingRemoteAndAddsMultiple(t *testing.T) {
	_, handler, storeDir := newPublishStoreRemoteFixture(t)

	if recorder := postPublishStoreRemote(t, handler, `{"store_id":"12","url":"https://github.com/owner/repo.git"}`); recorder.Code != http.StatusOK {
		t.Fatalf("first publish status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder := postPublishStoreRemote(t, handler, `{"store_id":"12","url":"https://github.com/owner/repo.git"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("second publish status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var resp struct {
		RemoteAdded bool `json:"remote_added"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.RemoteAdded {
		t.Fatal("resp remote_added = true, want false when the url is already configured")
	}
	if remotes := utils.ReadGitRemotes(storeDir); len(remotes) != 1 {
		t.Fatalf("store remotes = %+v, want still 1 remote", remotes)
	}

	// 另一个远端（gitee ssh）：新增 remote，名称为 gitee。
	recorder = postPublishStoreRemote(t, handler, `{"store_id":12,"url":"git@gitee.com:owner/repo.git"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("gitee publish status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	remotes := utils.ReadGitRemotes(storeDir)
	if len(remotes) != 2 {
		t.Fatalf("store remotes = %+v, want github + gitee", remotes)
	}
	if remotes[0].Name != "gitee" || remotes[1].Name != "github" {
		t.Fatalf("store remotes = %+v, want gitee then github (sorted by name)", remotes)
	}
}

// 入参不合法（缺 store_id / 缺 url / 不是 <owner>/<repo> 形态）时返回 400，且不查库、不建 remote。
func TestPublishStoreRemoteRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "missing store_id", body: `{"url":"https://github.com/owner/repo.git"}`},
		{name: "zero store_id", body: `{"store_id":"0","url":"https://github.com/owner/repo.git"}`},
		{name: "missing url", body: `{"store_id":"12"}`},
		{name: "blank url", body: `{"store_id":"12","url":"   "}`},
		{name: "not a git url", body: `{"store_id":"12","url":"not-a-git-url"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, handler, storeDir := newPublishStoreRemoteFixture(t)
			svc := handler.storeService.(*stubStoreRemoteService)

			recorder := postPublishStoreRemote(t, handler, tc.body)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", recorder.Code, recorder.Body.String())
			}
			if svc.getCalls != 0 {
				t.Fatalf("GetStoreByID calls = %d, want 0", svc.getCalls)
			}
			if remotes := utils.ReadGitRemotes(storeDir); len(remotes) != 0 {
				t.Fatalf("store remotes = %+v, want none", remotes)
			}
		})
	}
}

// store 还没有对应裸仓库（尚未发布过）时返回 409，而不是默默建库。
func TestPublishStoreRemoteMissingRepository(t *testing.T) {
	store := &types.Store{ID: 12, StoreType: "script", PathName: "missing-store"}
	svc := &stubStoreRemoteService{store: store}
	handler := &StoreHandler{
		storeService: svc,
		cfg:          &config.Config{Storage: &config.StorageConfig{BaseDir: t.TempDir()}},
	}

	recorder := postPublishStoreRemote(t, handler, `{"store_id":"12","url":"https://github.com/owner/repo.git"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", recorder.Code, recorder.Body.String())
	}
}

// store 不存在时按 404 返回（handleDataError 识别 gorm.ErrRecordNotFound）。
func TestPublishStoreRemoteStoreNotFound(t *testing.T) {
	_, handler, _ := newPublishStoreRemoteFixture(t)

	recorder := postPublishStoreRemote(t, handler, `{"store_id":"999","url":"https://github.com/owner/repo.git"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", recorder.Code, recorder.Body.String())
	}
}
