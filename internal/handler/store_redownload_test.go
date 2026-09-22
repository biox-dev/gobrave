package handler

import (
	"bytes"
	"context"
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
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"gorm.io/gorm"
)

const reDownloadStoreID = 12

// stubReDownloadStoreService 让 GetStoreByID 返回预置 store，
// 并接住 ReDownloadStore 末尾的 UpdateStore 回写（记录调用次数）。
type stubReDownloadStoreService struct {
	interfaces.StoreService
	store       *types.Store
	updateCalls int
}

func (s *stubReDownloadStoreService) GetStoreByID(_ context.Context, id int64) (*types.Store, error) {
	if s.store == nil || s.store.ID != id {
		return nil, gorm.ErrRecordNotFound
	}
	return s.store, nil
}

func (s *stubReDownloadStoreService) UpdateStore(_ context.Context, store *types.Store) error {
	s.updateCalls++
	s.store = store
	return nil
}

// repoHeadOf 返回仓库 HEAD 的 commit hash。
func repoHeadOf(t *testing.T, dir string) string {
	t.Helper()
	repo, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatalf("open %s: %v", dir, err)
	}
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head %s: %v", dir, err)
	}
	return head.Hash().String()
}

// newBareRepoWithFile 造一个含单个提交的裸仓库：临时工作区里写入 files 提交后推送进去。
func newBareRepoWithFile(t *testing.T, root, bareName string, files map[string]string, message string) string {
	t.Helper()

	ctx := context.Background()
	bareDir := filepath.Join(root, bareName)
	if _, err := utils.EnsureBareGitRepo(bareDir); err != nil {
		t.Fatalf("EnsureBareGitRepo %s: %v", bareDir, err)
	}

	workDir := filepath.Join(root, bareName+"-work")
	if _, err := utils.EnsureGitRepo(workDir); err != nil {
		t.Fatalf("EnsureGitRepo %s: %v", workDir, err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(workDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	workRepo, err := git.PlainOpen(workDir)
	if err != nil {
		t.Fatalf("open %s: %v", workDir, err)
	}
	if _, err := utils.CommitAll(workRepo, message, utils.GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}); err != nil {
		t.Fatalf("CommitAll %s: %v", message, err)
	}
	// PushDirToRepo 会调用 ensureLocalGitTransport，裸仓库之间的同步不依赖外部 git 可执行文件。
	if _, err := utils.PushDirToRepo(ctx, workDir, bareDir); err != nil {
		t.Fatalf("PushDirToRepo %s: %v", bareDir, err)
	}
	return bareDir
}

// newReDownloadFixture 准备一个「远程下载」形态的 store：
// store 裸仓库从 upstream clone 而来（origin = upstream），并额外挂了一个 mirror remote；
// 两个上游各有一个提交、内容不同，便于断言到底是从哪个 remote 拉取的。
func newReDownloadFixture(t *testing.T) (handler *StoreHandler, storeDir, upstreamDir, mirrorDir string) {
	t.Helper()

	// file 协议走进程内 go-git server：测试环境不依赖外部 git 可执行文件。
	t.Setenv("PATH", t.TempDir())

	root := t.TempDir()
	baseDir := filepath.Join(root, "storage")
	// 上游要带上 script.json：ReDownloadStore 拉取后会用它回填 store 元数据（hydrateStoreMetadataFromStoreFiles）。
	upstreamDir = newBareRepoWithFile(t, root, "upstream", map[string]string{
		"main.R":      "print('upstream')\n",
		"script.json": `{"version":"v1","script":{"component_name":"upstream-demo"}}`,
	}, "upstream v1")
	mirrorDir = newBareRepoWithFile(t, root, "mirror", map[string]string{
		"main.R":      "print('mirror')\n",
		"script.json": `{"version":"v1","script":{"component_name":"mirror-demo"}}`,
	}, "mirror v1")

	store := &types.Store{ID: reDownloadStoreID, StoreType: "script", Name: "demo", PathName: "test-store"}
	storeDir = utils.GetWorkflowOrScriptStoreDir(baseDir, store.PathName)
	if _, err := git.PlainCloneContext(context.Background(), storeDir, true, &git.CloneOptions{URL: upstreamDir}); err != nil {
		t.Fatalf("bare clone store: %v", err)
	}
	storeRepo, err := git.PlainOpen(storeDir)
	if err != nil {
		t.Fatalf("open store repo: %v", err)
	}
	if _, err := storeRepo.CreateRemote(&gitconfig.RemoteConfig{Name: "mirror", URLs: []string{mirrorDir}}); err != nil {
		t.Fatalf("CreateRemote mirror: %v", err)
	}

	handler = &StoreHandler{
		storeService: &stubReDownloadStoreService{store: store},
		cfg:          &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}},
	}
	return handler, storeDir, upstreamDir, mirrorDir
}

// postReDownloadStore 直接挂载 handler（绕过认证中间件），返回响应记录器。
func postReDownloadStore(t *testing.T, h *StoreHandler, body string) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.POST("/api/v1/store/redownload", func(c *gin.Context) {
		c.Set(types.UserIDContextKey.String(), "test-user")
		h.ReDownloadStore(c)
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/store/redownload", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(recorder, req)
	return recorder
}

// decodeReDownloadResponse 解析 /store/redownload 的成功响应。
func decodeReDownloadResponse(t *testing.T, recorder *httptest.ResponseRecorder) (storeID int64, remote string, upToDate bool) {
	t.Helper()

	var resp struct {
		StoreID  int64  `json:"store_id"`
		Remote   string `json:"remote"`
		UpToDate bool   `json:"up_to_date"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response %s: %v", recorder.Body.String(), err)
	}
	return resp.StoreID, resp.Remote, resp.UpToDate
}

// remote_name 为空时回退 origin（下载 store 时的默认上游），并如实报告 remote / up_to_date。
func TestReDownloadStoreDefaultsToOrigin(t *testing.T) {
	handler, storeDir, upstreamDir, _ := newReDownloadFixture(t)

	recorder := postReDownloadStore(t, handler, `{"id":"12"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", recorder.Code, recorder.Body.String())
	}
	storeID, remote, upToDate := decodeReDownloadResponse(t, recorder)
	if storeID != reDownloadStoreID || remote != "origin" {
		t.Fatalf("resp store_id = %d, remote = %q, want %d / \"origin\"", storeID, remote, reDownloadStoreID)
	}
	if !upToDate {
		t.Fatal("resp up_to_date = false, want true (store is already at upstream head)")
	}
	if got, want := repoHeadOf(t, storeDir), repoHeadOf(t, upstreamDir); got != want {
		t.Fatalf("store head = %s, want upstream head %s", got, want)
	}
}

// remote_name 指定时从该 remote 拉取（store 裸仓库可以有多个远端），
// 本地分支跟到该远端的最新提交；再次拉取报告已是最新。
func TestReDownloadStoreUsesRequestedRemote(t *testing.T) {
	handler, storeDir, _, mirrorDir := newReDownloadFixture(t)

	// remote 名两端空白应被忽略。
	recorder := postReDownloadStore(t, handler, `{"id":"12","remote_name":"  mirror  "}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", recorder.Code, recorder.Body.String())
	}
	storeID, remote, upToDate := decodeReDownloadResponse(t, recorder)
	if storeID != reDownloadStoreID || remote != "mirror" {
		t.Fatalf("resp store_id = %d, remote = %q, want %d / \"mirror\"", storeID, remote, reDownloadStoreID)
	}
	if upToDate {
		t.Fatal("resp up_to_date = true, want false (mirror has a commit the store does not have)")
	}
	if got, want := repoHeadOf(t, storeDir), repoHeadOf(t, mirrorDir); got != want {
		t.Fatalf("store head = %s, want mirror head %s", got, want)
	}
	content, err := utils.ReadFileFromGitRepo(storeDir, "main.R")
	if err != nil {
		t.Fatalf("ReadFileFromGitRepo: %v", err)
	}
	if string(content) != "print('mirror')\n" {
		t.Fatalf("main.R after fetch = %q, want the mirror content", string(content))
	}

	// 再拉一次：已是最新。
	recorder = postReDownloadStore(t, handler, `{"id":"12","remote_name":"mirror"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("second status = %d, want 200, body = %s", recorder.Code, recorder.Body.String())
	}
	if _, remote, upToDate = decodeReDownloadResponse(t, recorder); remote != "mirror" || !upToDate {
		t.Fatalf("second resp remote = %q, up_to_date = %v, want mirror / true", remote, upToDate)
	}
}

// remote 没配置在 store 仓库上：返回 400 可读错误，而不是让 go-git 报底层错误（500），
// 且仓库不发生任何变化。
func TestReDownloadStoreRejectsUnknownRemote(t *testing.T) {
	handler, storeDir, _, _ := newReDownloadFixture(t)

	before := repoHeadOf(t, storeDir)
	recorder := postReDownloadStore(t, handler, `{"id":"12","remote_name":"nope"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "not configured") {
		t.Fatalf("body = %s, want a readable \"not configured\" message", recorder.Body.String())
	}
	if got := repoHeadOf(t, storeDir); got != before {
		t.Fatalf("store head = %s, want unchanged %s", got, before)
	}
}

// 仓库上没有任何 remote（例如只在本地发布过）：返回 400 并给出可读提示。
func TestReDownloadStoreWithoutRemote(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	baseDir := t.TempDir()
	store := &types.Store{ID: reDownloadStoreID, StoreType: "script", PathName: "local-store"}
	storeDir := utils.GetWorkflowOrScriptStoreDir(baseDir, store.PathName)
	if _, err := utils.EnsureBareGitRepo(storeDir); err != nil {
		t.Fatalf("EnsureBareGitRepo: %v", err)
	}
	handler := &StoreHandler{
		storeService: &stubReDownloadStoreService{store: store},
		cfg:          &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}},
	}

	recorder := postReDownloadStore(t, handler, `{"id":"12"}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "no remote") {
		t.Fatalf("body = %s, want a readable \"no remote\" message", recorder.Body.String())
	}
}
