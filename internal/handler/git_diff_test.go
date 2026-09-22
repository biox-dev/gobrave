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
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/gin-gonic/gin"
)

// 组件目录的 git-diff 接口只走「按主键查组件 -> 查项目 -> 推导目录」这一条链路，
// 所以这里用最小 stub（其余方法由内嵌接口占位，不会被调用）。

type stubGitWorkflowService struct {
	interfaces.WorkflowService
	script   *types.Script
	workflow *types.Workflow
}

func (s stubGitWorkflowService) GetScriptByID(_ context.Context, _ int64) (*types.Script, error) {
	return s.script, nil
}

func (s stubGitWorkflowService) GetWorkflowByID(_ context.Context, _ int64) (*types.Workflow, error) {
	return s.workflow, nil
}

type stubGitProjectService struct {
	interfaces.ProjectService
	project *types.Project
}

func (s stubGitProjectService) GetProjectByID(_ context.Context, _ int64) (*types.Project, error) {
	return s.project, nil
}

// gitDiffTestResponse 只声明断言用得到的字段，避免测试与响应结构强耦合。
type gitDiffTestResponse struct {
	Entity      string              `json:"entity"`
	ID          string              `json:"id"`
	RepoDir     string              `json:"repo_dir"`
	Initialized bool                `json:"initialized"`
	HasChanges  bool                `json:"has_changes"`
	Worktree    gitDiffTestSection  `json:"worktree"`
	Unpublished *gitDiffTestSection `json:"unpublished"`
	GitState    *utils.GitSyncState `json:"git_state"`
}

type gitDiffTestSection struct {
	BaseCommit   string `json:"base_commit"`
	TargetCommit string `json:"target_commit"`
	HasChanges   bool   `json:"has_changes"`
	FileCount    int    `json:"file_count"`
	Patch        string `json:"patch"`
}

// serveGitDiff 直接挂载 handler（绕过认证中间件），返回响应记录器。
func serveGitDiff(t *testing.T, serve func(c *gin.Context), route, requestPath string) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET(route, func(c *gin.Context) {
		c.Set(types.UserIDContextKey.String(), "test-user")
		serve(c)
	})

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, requestPath, nil))
	return recorder
}

func decodeGitDiffResponse(t *testing.T, recorder *httptest.ResponseRecorder) gitDiffTestResponse {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var resp gitDiffTestResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v (body=%s)", err, recorder.Body.String())
	}
	return resp
}

func mustGitRepo(t *testing.T, dir string) {
	t.Helper()
	repo, err := utils.EnsureGitRepo(dir)
	if err != nil {
		t.Fatalf("EnsureGitRepo: %v", err)
	}
	if _, err := utils.CommitAll(repo, "init", utils.GitIdentity{Name: "gobrave", Email: "gobrave@123.com"}); err != nil {
		t.Fatalf("CommitAll: %v", err)
	}
}

func mustWriteScriptFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// GetScriptDiff 应把脚本目录（project.project_id + script_id 推导）作为本地仓库，
// 返回工作区未提交改动，并带上 entity/id/git_state。
func TestGetScriptGitDiff(t *testing.T) {
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

	recorder := serveGitDiff(t, h.GetScriptDiff,
		"/api/v1/script/:scriptId/git-diff",
		"/api/v1/script/1/git-diff",
	)
	resp := decodeGitDiffResponse(t, recorder)

	if resp.Entity != "script" || resp.ID != "s1" {
		t.Fatalf("entity/id = %q/%q, want script/s1", resp.Entity, resp.ID)
	}
	if resp.RepoDir != scriptDir {
		t.Fatalf("repo_dir = %q, want %q", resp.RepoDir, scriptDir)
	}
	if !resp.Initialized || !resp.HasChanges || !resp.Worktree.HasChanges {
		t.Fatalf("diff = %+v, want initialized worktree changes", resp)
	}
	if resp.Worktree.FileCount != 1 || !strings.Contains(resp.Worktree.Patch, "-print(1)") ||
		!strings.Contains(resp.Worktree.Patch, "+print(2)") {
		t.Fatalf("worktree = %+v, want main.py modification", resp.Worktree)
	}
	if resp.Unpublished != nil {
		t.Fatalf("unpublished = %+v, want nil (never published)", resp.Unpublished)
	}
	if resp.GitState == nil || !resp.GitState.LocalDirty || resp.GitState.LocalDir != scriptDir {
		t.Fatalf("git_state = %+v, want local dirty at %q", resp.GitState, scriptDir)
	}
}

// GetWorkflowDiff 在已发布（store 是裸仓库）且本地又有新提交时，
// 除工作区外还应返回 http://store 与本地 HEAD 之间的未发布提交差异。
func TestGetWorkflowGitDiffUnpublished(t *testing.T) {
	baseDir := t.TempDir()
	workflowDir := utils.GetWorkflowFileDir(baseDir, "p1", "w1")
	mainPath := filepath.Join(workflowDir, "workflow.json")
	mustWriteScriptFile(t, mainPath, "{}\n")
	mustGitRepo(t, workflowDir)

	// 发布：本地目录 push 到 store 裸仓库。
	storeDir := utils.GetWorkflowOrScriptStoreDir(baseDir, "w-1")
	if _, err := utils.EnsureBareGitRepo(storeDir); err != nil {
		t.Fatalf("EnsureBareGitRepo: %v", err)
	}
	if _, err := utils.PushDirToRepo(t.Context(), workflowDir, storeDir); err != nil {
		t.Fatalf("PushDirToRepo: %v", err)
	}

	// 发布后再提交一次：工作区干净，但本地领先 store 一个提交。
	mustWriteScriptFile(t, mainPath, "{\"v\":2}\n")
	mustGitRepo(t, workflowDir)

	h := &GitHandler{
		workflowService: stubGitWorkflowService{workflow: &types.Workflow{ID: 2, WorkflowID: "w1", ProjectID: 7, StoreID: 3}},
		projectService:  stubGitProjectService{project: &types.Project{ID: 7, ProjectID: "p1"}},
		storeService:    stubStoreService{store: &types.Store{ID: 3, PathName: "w-1"}},
		cfg:             &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}},
	}

	recorder := serveGitDiff(t, h.GetWorkflowDiff,
		"/api/v1/workflow/:workflowId/git-diff",
		"/api/v1/workflow/2/git-diff",
	)
	resp := decodeGitDiffResponse(t, recorder)

	if resp.Entity != "workflow" || resp.ID != "w1" || resp.RepoDir != workflowDir {
		t.Fatalf("entity/id/repo = %q/%q/%q, want workflow/w1/%q", resp.Entity, resp.ID, resp.RepoDir, workflowDir)
	}
	if resp.Worktree.HasChanges {
		t.Fatalf("worktree = %+v, want clean after commit", resp.Worktree)
	}
	if resp.Unpublished == nil || !resp.Unpublished.HasChanges {
		t.Fatalf("unpublished = %+v, want changes", resp.Unpublished)
	}
	if !strings.Contains(resp.Unpublished.Patch, "+{\"v\":2}") {
		t.Fatalf("unpublished patch missing new content:\n%s", resp.Unpublished.Patch)
	}
	if !resp.HasChanges {
		t.Fatalf("has_changes = false, want true for unpublished commit")
	}
	if resp.GitState == nil || !resp.GitState.LocalAhead || !resp.GitState.StoreInitialized {
		t.Fatalf("git_state = %+v, want local ahead with initialized store", resp.GitState)
	}
}
