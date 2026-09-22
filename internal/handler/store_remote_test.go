package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// newPublishStoreRemoteFixture 在临时 base_dir 下准备发布到远程所需的两个仓库：
//
//   - storeDir：store 裸仓库（生产布局 base_dir/store/<path_name>），且已经有一个提交，
//     模拟「组件已发布到 store」；
//   - 返回的 remoteDir：一个空的裸仓库目录，充当本次发布的「远程仓库」。地址用本地路径，
//     go-git 的 file 协议由进程内 server 实现，所以测试执行的是真实 push，不走网络。
//
// 返回 store 记录、handler、store 目录与远程裸仓库目录。
func newPublishStoreRemoteFixture(t *testing.T) (*types.Store, *StoreHandler, string, string) {
	t.Helper()

	baseDir := t.TempDir()
	store := &types.Store{ID: 12, StoreType: "script", Name: "demo", PathName: "test-store"}
	storeDir := utils.GetWorkflowOrScriptStoreDir(baseDir, store.PathName)
	seedStoreBareRepo(t, storeDir)

	remoteDir := newRemoteBareRepo(t)

	svc := &stubStoreRemoteService{store: store}
	handler := &StoreHandler{
		storeService: svc,
		cfg:          &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}},
	}
	return store, handler, storeDir, remoteDir
}

// seedStoreBareRepo 让 storeDir 成为一个「已发布过内容」的裸仓库：建一个带提交的普通仓库，
// 再 force push 到 storeDir（与 PublishScript 的路径一致），使 store 的 HEAD 指向真实提交，
// 这样它才能被推送到远程。
func seedStoreBareRepo(t *testing.T, storeDir string) {
	t.Helper()

	if _, err := utils.EnsureBareGitRepo(storeDir); err != nil {
		t.Fatalf("EnsureBareGitRepo(store): %v", err)
	}

	srcDir := t.TempDir()
	repo, err := utils.EnsureGitRepo(srcDir)
	if err != nil {
		t.Fatalf("EnsureGitRepo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "README.md"), []byte("demo store"), 0o644); err != nil {
		t.Fatalf("write README.md: %v", err)
	}
	identity := utils.GitIdentity{Name: "test", Email: "test@example.com"}
	if _, err := utils.CommitAll(repo, "seed store", identity); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
	if _, err := utils.PushDirToRepo(context.Background(), srcDir, storeDir); err != nil {
		t.Fatalf("PushDirToRepo: %v", err)
	}
}

// newRemoteBareRepo 建一个空的裸仓库目录，作为「远程仓库」（本地路径本身就是合法 git 地址）。
func newRemoteBareRepo(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), "remote.git")
	if _, err := utils.EnsureBareGitRepo(dir); err != nil {
		t.Fatalf("EnsureBareGitRepo(remote): %v", err)
	}
	return dir
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

// 首次发布：url（trim 后）被写成 store 裸仓库的 remote（本地路径 -> 名 "remote"），
// 并把 store 当前分支 push 到该远程仓库；published / pushed 如实反映推送结果。
func TestPublishStoreRemoteAddsRemoteAndPushes(t *testing.T) {
	_, handler, storeDir, remoteDir := newPublishStoreRemoteFixture(t)
	svc := handler.storeService.(*stubStoreRemoteService)

	recorder := postPublishStoreRemote(t, handler, fmt.Sprintf(`{"store_id":"12","url":"  %s  "}`, remoteDir))

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
		Branch      string            `json:"branch"`
		Published   bool              `json:"published"`
		Pushed      bool              `json:"pushed"`
		UpToDate    bool              `json:"up_to_date"`
		Remotes     []utils.GitRemote `json:"remotes"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.StoreID != "12" {
		t.Fatalf("resp store_id = %q, want \"12\"", resp.StoreID)
	}
	if resp.URL != remoteDir {
		t.Fatalf("resp url = %q, want the trimmed url %q", resp.URL, remoteDir)
	}
	if resp.Remote != "remote" {
		t.Fatalf("resp remote = %q, want remote", resp.Remote)
	}
	if !resp.RemoteAdded {
		t.Fatal("resp remote_added = false, want true on first publish")
	}
	if resp.Branch != "main" {
		t.Fatalf("resp branch = %q, want main", resp.Branch)
	}
	if !resp.Published || !resp.Pushed || resp.UpToDate {
		t.Fatalf("resp published=%v pushed=%v up_to_date=%v, want true/true/false", resp.Published, resp.Pushed, resp.UpToDate)
	}

	// 仓库配置里应该真的多了这个 remote，且与响应一致。
	remotes := utils.ReadGitRemotes(storeDir)
	if len(remotes) != 1 || remotes[0].Name != "remote" || remotes[0].URLs[0] != remoteDir {
		t.Fatalf("store remotes = %+v, want a single remote pointing at %q", remotes, remoteDir)
	}
	if len(resp.Remotes) != 1 || resp.Remotes[0].Name != "remote" {
		t.Fatalf("resp remotes = %+v, want the newly added remote", resp.Remotes)
	}

	// 远程裸仓库真的收到了 store 的内容（README.md 来自 seedStoreBareRepo）。
	content, err := utils.ReadFileFromGitRepo(remoteDir, "README.md")
	if err != nil {
		t.Fatalf("read pushed file from remote: %v", err)
	}
	if string(content) != "demo store" {
		t.Fatalf("remote README.md = %q, want %q", content, "demo store")
	}
}

// 同一个 url 再次发布：跳过添加（remote_added=false）但仍执行 push，且已是最新
// （pushed=false、up_to_date=true），remote 数量不变；
// 另一个地址则新增第二个 remote（同名冲突追加 -2），一次发布到多个远端。
func TestPublishStoreRemoteRePublishAndMultipleRemotes(t *testing.T) {
	_, handler, storeDir, remoteDir := newPublishStoreRemoteFixture(t)
	body := fmt.Sprintf(`{"store_id":"12","url":%q}`, remoteDir)

	if recorder := postPublishStoreRemote(t, handler, body); recorder.Code != http.StatusOK {
		t.Fatalf("first publish status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	recorder := postPublishStoreRemote(t, handler, body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("second publish status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var resp struct {
		RemoteAdded bool `json:"remote_added"`
		Published   bool `json:"published"`
		Pushed      bool `json:"pushed"`
		UpToDate    bool `json:"up_to_date"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.RemoteAdded {
		t.Fatal("resp remote_added = true, want false when the url is already configured")
	}
	if !resp.Published {
		t.Fatal("resp published = false, want true (push reuses the existing remote)")
	}
	if resp.Pushed || !resp.UpToDate {
		t.Fatalf("resp pushed=%v up_to_date=%v, want false/true when nothing changed", resp.Pushed, resp.UpToDate)
	}
	if remotes := utils.ReadGitRemotes(storeDir); len(remotes) != 1 {
		t.Fatalf("store remotes = %+v, want still 1 remote", remotes)
	}

	// 第二个远端（另一个本地裸仓库）：新增 remote，名称为 remote-2。
	second := newRemoteBareRepo(t)
	recorder = postPublishStoreRemote(t, handler, fmt.Sprintf(`{"store_id":12,"url":%q}`, second))
	if recorder.Code != http.StatusOK {
		t.Fatalf("second remote publish status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	remotes := utils.ReadGitRemotes(storeDir)
	if len(remotes) != 2 {
		t.Fatalf("store remotes = %+v, want two remotes", remotes)
	}
	if remotes[0].Name != "remote" || remotes[1].Name != "remote-2" {
		t.Fatalf("store remotes = %+v, want remote then remote-2 (sorted by name)", remotes)
	}

	// 两个远端都收到了内容。
	for _, dir := range []string{remoteDir, second} {
		if content, err := utils.ReadFileFromGitRepo(dir, "README.md"); err != nil {
			t.Fatalf("read pushed file from %q: %v", dir, err)
		} else if string(content) != "demo store" {
			t.Fatalf("remote %q README.md = %q, want %q", dir, content, "demo store")
		}
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
			_, handler, storeDir, _ := newPublishStoreRemoteFixture(t)
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

// store 目录已存在但裸仓库里还没有任何提交（只建了 store、没发布过组件）时返回 409：
// 没有内容可推送，提示先发布组件。
func TestPublishStoreRemoteRepositoryWithoutCommit(t *testing.T) {
	baseDir := t.TempDir()
	store := &types.Store{ID: 12, StoreType: "script", PathName: "empty-store"}
	storeDir := utils.GetWorkflowOrScriptStoreDir(baseDir, store.PathName)
	if _, err := utils.EnsureBareGitRepo(storeDir); err != nil {
		t.Fatalf("EnsureBareGitRepo: %v", err)
	}

	handler := &StoreHandler{
		storeService: &stubStoreRemoteService{store: store},
		cfg:          &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}},
	}

	remoteDir := newRemoteBareRepo(t)
	recorder := postPublishStoreRemote(t, handler, fmt.Sprintf(`{"store_id":"12","url":%q}`, remoteDir))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", recorder.Code, recorder.Body.String())
	}
}

// store 不存在时按 404 返回（handleDataError 识别 gorm.ErrRecordNotFound）。
func TestPublishStoreRemoteStoreNotFound(t *testing.T) {
	_, handler, _, _ := newPublishStoreRemoteFixture(t)

	recorder := postPublishStoreRemote(t, handler, `{"store_id":"999","url":"https://github.com/owner/repo.git"}`)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", recorder.Code, recorder.Body.String())
	}
}
