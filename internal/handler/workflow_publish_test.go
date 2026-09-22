package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/exportcodec"
	exportcodecv1 "github.com/biox-dev/gobrave/internal/exportcodec/v1"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
	git "github.com/go-git/go-git/v5"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestCopyDirReplaceExcluding 覆盖发布时把脚本目录快照进 workflow 目录的核心行为：
// 内容整体替换 dst，同时跳过任意层级的 .git，避免把脚本仓库塞进 workflow 仓库。
func TestCopyDirReplaceExcluding(t *testing.T) {
	src := t.TempDir()
	writeTestFile(t, filepath.Join(src, "main.R"), "print('hi')\n")
	writeTestFile(t, filepath.Join(src, "script.json"), `{"script_id":"s1"}`)
	writeTestFile(t, filepath.Join(src, ".git", "HEAD"), "ref: refs/heads/main\n")
	writeTestFile(t, filepath.Join(src, "sub", "keep.txt"), "keep\n")
	writeTestFile(t, filepath.Join(src, "sub", ".git", "HEAD"), "nested\n")

	dst := filepath.Join(t.TempDir(), "dst")
	if err := utils.CopyDirReplace(src, dst, ".git"); err != nil {
		t.Fatalf("utils.CopyDirReplace: %v", err)
	}

	for _, rel := range []string{"main.R", "script.json", "sub/keep.txt"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel))); err != nil {
			t.Fatalf("%s not copied: %v", rel, err)
		}
	}
	for _, rel := range []string{".git", "sub/.git"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Fatalf("%s must not be copied, stat err = %v", rel, err)
		}
	}

	// dst 已有内容时必须被完全替换，而不是叠加。
	writeTestFile(t, filepath.Join(dst, "stale.txt"), "stale\n")
	if err := utils.CopyDirReplace(src, dst, ".git"); err != nil {
		t.Fatalf("utils.CopyDirReplace second run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale file should be removed, stat err = %v", err)
	}

	// 不传 exclude 时保持旧行为（.git 一并复制）。
	if err := utils.CopyDirReplace(src, dst); err != nil {
		t.Fatalf("utils.CopyDirReplace: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git", "HEAD")); err != nil {
		t.Fatalf(".git should be copied without exclusion: %v", err)
	}

	// 源目录不存在时按幂等处理（发布/安装时脚本目录可能还没建）。
	missing := filepath.Join(t.TempDir(), "missing")
	if err := utils.CopyDirReplace(missing, dst); err != nil {
		t.Fatalf("utils.CopyDirReplace with missing source: %v", err)
	}
	if err := utils.CopyDirReplace(missing, dst, ".git"); err != nil {
		t.Fatalf("utils.CopyDirReplace with missing source and exclude: %v", err)
	}
}

// TestReadInstalledJSONFromDir 校验发布/安装共用的读盘逻辑：
// 命中时能解析出 id，缺失时返回可被 os.IsNotExist 识别的错误（安装时映射为 404）。
func TestReadInstalledJSONFromDir(t *testing.T) {
	h := newTestWorkflowHandler()

	workflowDir := t.TempDir()
	writeTestFile(t, filepath.Join(workflowDir, exportcodec.WorkflowJSONFileName), `{"workflow_id":"wf-1","workflow":{},"scripts":[]}`)

	workflowCodec, workflowRaw, err := h.readWorkflowRawFromDir(workflowDir)
	if err != nil {
		t.Fatalf("readWorkflowRawFromDir: %v", err)
	}
	workflowPayload, err := workflowCodec.DecodeWorkflow(workflowRaw)
	if err != nil {
		t.Fatalf("DecodeWorkflow: %v", err)
	}
	if workflowPayload.WorkflowID != "wf-1" {
		t.Fatalf("workflow_id = %q, want wf-1", workflowPayload.WorkflowID)
	}

	scriptDir := t.TempDir()
	writeTestFile(t, filepath.Join(scriptDir, exportcodec.ScriptJSONFileName), `{"script_id":"s-1","script":{}}`)

	scriptCodec, scriptRaw, err := h.readScriptRawFromDir(scriptDir)
	if err != nil {
		t.Fatalf("readScriptRawFromDir: %v", err)
	}
	scriptPayload, err := scriptCodec.DecodeScript(scriptRaw)
	if err != nil {
		t.Fatalf("DecodeScript: %v", err)
	}
	if scriptPayload.ScriptID != "s-1" {
		t.Fatalf("script_id = %q, want s-1", scriptPayload.ScriptID)
	}

	emptyDir := t.TempDir()
	if _, _, err := h.readWorkflowRawFromDir(emptyDir); !os.IsNotExist(err) {
		t.Fatalf("missing workflow.json err = %v, want os.IsNotExist", err)
	}
	if _, _, err := h.readScriptRawFromDir(emptyDir); !os.IsNotExist(err) {
		t.Fatalf("missing script.json err = %v, want os.IsNotExist", err)
	}

	// 非法 JSON 必须报错，而不是返回零值。
	badDir := t.TempDir()
	writeTestFile(t, filepath.Join(badDir, exportcodec.WorkflowJSONFileName), `{`)
	if _, _, err := h.readWorkflowRawFromDir(badDir); err == nil || os.IsNotExist(err) {
		t.Fatalf("invalid workflow.json err = %v, want parse error", err)
	}
}

// commitTestRepo 在 dir 下建仓库并把 files（仓库内相对路径 -> 内容）提交为一个 commit。
func commitTestRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()

	repo, err := utils.EnsureGitRepo(dir)
	if err != nil {
		t.Fatalf("utils.EnsureGitRepo: %v", err)
	}
	for rel, content := range files {
		writeTestFile(t, filepath.Join(dir, filepath.FromSlash(rel)), content)
	}
	if _, err := utils.CommitAll(repo, "test", utils.GitIdentity{Name: "test", Email: "test@example.com"}); err != nil {
		t.Fatalf("utils.CommitAll: %v", err)
	}
	if _, err := repo.Head(); err != nil {
		t.Fatalf("test repository has no commit: %v", err)
	}
}

// TestReadStoreExportJSONFromBareRepo 校验 store 导出文件的读取口径：
// store 是裸仓库（没有工作区文件），workflow.json / script.json 必须从 HEAD 提交里读；
// 根目录优先，其次任意层级，两者都没有时报错。
func TestReadStoreExportJSONFromBareRepo(t *testing.T) {
	src := t.TempDir()
	commitTestRepo(t, src, map[string]string{
		exportcodec.WorkflowJSONFileName:                    `{"workflow_id":"wf-1","workflow":{"name":"wf","category":"cat","message":"msg"}}`,
		exportcodec.ScriptJSONFileName:                      `{"script_id":"root"}`,
		"script/s1/" + exportcodec.ScriptJSONFileName:       `{"script_id":"nested"}`,
		"script/s1/sub/" + exportcodec.WorkflowJSONFileName: `{"workflow_id":"nested"}`,
	})

	// 把源仓库发布成裸仓库（与 publish 一致），再裸克隆成 store。
	origin := filepath.Join(t.TempDir(), "origin")
	if _, err := utils.EnsureBareGitRepo(origin); err != nil {
		t.Fatalf("utils.EnsureBareGitRepo: %v", err)
	}
	if _, err := utils.PushDirToRepo(context.Background(), src, origin); err != nil {
		t.Fatalf("utils.PushDirToRepo: %v", err)
	}
	// 裸克隆：与 DownloadStore 的产物完全一致（目录即 git 目录，没有工作区文件）。
	// 源用裸仓库（publish 到 store 的产物形态）：进程内 file 传输只服务裸仓库。
	bareStore := filepath.Join(t.TempDir(), "store")
	if _, err := git.PlainCloneContext(context.Background(), bareStore, true, &git.CloneOptions{URL: origin}); err != nil {
		t.Fatalf("bare clone: %v", err)
	}

	workflowRaw, err := readStoreExportJSON(bareStore, exportcodec.WorkflowJSONFileName)
	if err != nil {
		t.Fatalf("readStoreExportJSON workflow: %v", err)
	}
	if !strings.Contains(string(workflowRaw), `"wf-1"`) {
		t.Fatalf("workflow.json = %s, want root file", workflowRaw)
	}

	// 根目录优先：即使嵌套目录里也有同名文件，也命中根目录那个。
	scriptRaw, err := readStoreExportJSON(bareStore, exportcodec.ScriptJSONFileName)
	if err != nil {
		t.Fatalf("readStoreExportJSON script: %v", err)
	}
	if !strings.Contains(string(scriptRaw), `"root"`) {
		t.Fatalf("script.json = %s, want root file", scriptRaw)
	}

	// 只有嵌套文件时回退到目录查找。
	nested := t.TempDir()
	commitTestRepo(t, nested, map[string]string{
		"a/b/" + exportcodec.ScriptJSONFileName: `{"script_id":"nested"}`,
	})
	nestedRaw, err := readStoreExportJSON(nested, exportcodec.ScriptJSONFileName)
	if err != nil {
		t.Fatalf("readStoreExportJSON nested: %v", err)
	}
	if !strings.Contains(string(nestedRaw), `"nested"`) {
		t.Fatalf("nested script.json = %s", nestedRaw)
	}

	// 文件不存在、空仓库（没有任何提交）以及不是仓库的目录都要报错，而不是返回空内容。
	if missing, err := readStoreExportJSON(nested, exportcodec.WorkflowJSONFileName); err == nil {
		t.Fatalf("missing workflow.json should fail, got %s", missing)
	}
	emptyBare := filepath.Join(t.TempDir(), "empty")
	if _, err := utils.EnsureBareGitRepo(emptyBare); err != nil {
		t.Fatalf("utils.EnsureBareGitRepo: %v", err)
	}
	if _, err := readStoreExportJSON(emptyBare, exportcodec.ScriptJSONFileName); err == nil {
		t.Fatal("readStoreExportJSON on commit-less bare repo should fail")
	}
	if _, err := readStoreExportJSON(filepath.Join(t.TempDir(), "not-exists"), exportcodec.ScriptJSONFileName); err == nil {
		t.Fatal("readStoreExportJSON on non-repository should fail")
	}
}

// ---- 发布到 store 的测试 stub：只实现 PublishScript 会走到的链路 ----------------
// 其余方法由内嵌接口占位（调用会 nil panic），能跑到说明链路被改动过、需要补实现。

type publishStubWorkflowService struct {
	interfaces.WorkflowService
	script *types.Script
}

func (s publishStubWorkflowService) GetScriptByID(_ context.Context, _ int64) (*types.Script, error) {
	return s.script, nil
}

// UpdateScript 复用同一个 script 指针：接口返回的就是 handler 改写过的对象，
// 第二次发布因此能看到第一次写入的 store_id。
func (s publishStubWorkflowService) UpdateScript(_ context.Context, _ *types.Script) error {
	return nil
}

// GenerateScriptJSONByScriptID 每次返回完全相同的 payload：重复发布不会产生新的文件改动，
// 从而走到 store push「没有内容可推」的分支。
func (s publishStubWorkflowService) GenerateScriptJSONByScriptID(_ context.Context, _ int64) (*types.ScriptJSONExportResponse, error) {
	return &types.ScriptJSONExportResponse{
		ScriptID: s.script.ScriptID,
		Script:   map[string]any{"script_id": s.script.ScriptID, "name": s.script.ComponentName},
	}, nil
}

type publishStubProjectService struct {
	interfaces.ProjectService
	project *types.Project
}

func (s publishStubProjectService) GetActiveProjectByUserID(_ context.Context, _ string) (*types.Project, error) {
	return s.project, nil
}

func (s publishStubProjectService) GetProjectByID(_ context.Context, _ int64) (*types.Project, error) {
	return s.project, nil
}

type publishStubStoreService struct {
	interfaces.StoreService
	store  *types.Store
	nextID int64
}

func (s *publishStubStoreService) CreateStore(_ context.Context, item *types.Store) error {
	s.nextID++
	item.ID = s.nextID
	s.store = item
	return nil
}

func (s *publishStubStoreService) GetStoreByID(_ context.Context, _ int64) (*types.Store, error) {
	return s.store, nil
}

func (s *publishStubStoreService) UpdateStore(_ context.Context, item *types.Store) error {
	s.store = item
	return nil
}

// publishResponse 只声明断言用得到的字段，避免测试与响应结构强耦合。
type publishResponse struct {
	Message   string `json:"message"`
	Pushed    *bool  `json:"pushed"`
	StorePath string `json:"store_path"`
}

// servePublishScript 直接挂载 handler（绕过认证中间件），返回响应记录器。
func servePublishScript(t *testing.T, h *WorkflowHandler, body string) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.POST("/api/v1/workflow/publish-script", func(c *gin.Context) {
		c.Set(types.UserIDContextKey.String(), "test-user")
		h.PublishScript(c)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/workflow/publish-script", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	return recorder
}

func decodePublishResponse(t *testing.T, recorder *httptest.ResponseRecorder) publishResponse {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var resp publishResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, recorder.Body.String())
	}
	return resp
}

// TestPublishScriptAlreadyUpToDate 锁定「重复发布」的语义：
// 第二次发布时脚本目录没有新的提交，store 的 push 返回 "already up-to-date"，
// 接口应回 200 + pushed=false + message="already up-to-date"，而不是报 500。
func TestPublishScriptAlreadyUpToDate(t *testing.T) {
	baseDir := t.TempDir()
	scriptDir := utils.GetScriptFileDir(baseDir, "p1", "s1")
	writeTestFile(t, filepath.Join(scriptDir, "main.R"), "print('v1')\n")
	mustGitRepo(t, scriptDir)

	wfService := publishStubWorkflowService{script: &types.Script{ID: 1, ScriptID: "s1", ProjectID: 7, ComponentName: "comp"}}
	reg := exportcodec.NewRegistry()
	reg.Register(exportcodecv1.NewCodec(wfService, nil, utils.GitIdentity{Name: "test", Email: "test@example.com"}))

	h := &WorkflowHandler{
		workflowService: wfService,
		projectService:  publishStubProjectService{project: &types.Project{ID: 7, ProjectID: "p1"}},
		storeService:    &publishStubStoreService{},
		exportCodecs:    reg,
		cfg:             &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}},
	}

	// 首次发布：脚本目录产生了新提交，push 应报告 pushed=true。
	first := decodePublishResponse(t, servePublishScript(t, h, `{"script_id":"1","message":"first"}`))
	if first.Pushed == nil || !*first.Pushed {
		t.Fatalf("first publish pushed = %v, want true (body should be a real response)", first.Pushed)
	}
	if first.Message != "success" {
		t.Fatalf("first publish message = %q, want %q", first.Message, "success")
	}
	if first.StorePath == "" {
		t.Fatal("first publish store_path is empty")
	}

	// 再次发布且脚本没有任何改动：远端已是同一个提交，应提示而不是报错。
	second := decodePublishResponse(t, servePublishScript(t, h, `{"script_id":"1","message":"second"}`))
	if second.Pushed == nil || *second.Pushed {
		t.Fatalf("second publish pushed = %v, want false", second.Pushed)
	}
	if second.Message != "already up-to-date" {
		t.Fatalf("second publish message = %q, want %q", second.Message, "already up-to-date")
	}
	if second.StorePath != first.StorePath {
		t.Fatalf("store_path drifted: %q -> %q", first.StorePath, second.StorePath)
	}
}
