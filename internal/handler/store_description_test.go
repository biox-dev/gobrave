package handler

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/exportcodec"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
)

// newStoreRepoWithFiles 在临时目录下建一个裸仓库，内容为 files（仓库内相对路径 -> 内容），
// 形态与 publish / DownloadStore 产出的 store 仓库一致（没有工作区文件）。
func newStoreRepoWithFiles(t *testing.T, files map[string]string) string {
	t.Helper()

	src := t.TempDir()
	commitTestRepo(t, src, files)

	storeDir := filepath.Join(t.TempDir(), "store")
	if _, err := utils.EnsureBareGitRepo(storeDir); err != nil {
		t.Fatalf("utils.EnsureBareGitRepo: %v", err)
	}
	if _, err := utils.PushDirToRepo(context.Background(), src, storeDir); err != nil {
		t.Fatalf("utils.PushDirToRepo: %v", err)
	}
	return storeDir
}

// TestReadStoreDescriptionFromStoreRepo 校验 description 的读取口径：
// 按 store_type 选 workflow.json / script.json，并从对应子对象里取 description；
// 类型不支持、目录缺失、文件缺失、JSON 损坏时都返回空串（列表接口不能因此失败）。
func TestReadStoreDescriptionFromStoreRepo(t *testing.T) {
	workflowStore := newStoreRepoWithFiles(t, map[string]string{
		exportcodec.WorkflowJSONFileName: `{"version":"v1","workflow_id":"wf-1","workflow":{"name":"wf","description":"  工作流描述  "}}`,
	})
	if got := exportcodec.StoreDescription(workflowStore, "workflow"); got != "工作流描述" {
		t.Fatalf("workflow description = %q, want 工作流描述", got)
	}
	// store_type 大小写与空白不敏感。
	if got := exportcodec.StoreDescription(workflowStore, " Workflow "); got != "工作流描述" {
		t.Fatalf("workflow description (mixed case) = %q, want 工作流描述", got)
	}
	// 仓库里没有 script.json 时，按 script 读取应返回空串而不是报错。
	if got := exportcodec.StoreDescription(workflowStore, "script"); got != "" {
		t.Fatalf("script description = %q, want empty", got)
	}

	scriptStore := newStoreRepoWithFiles(t, map[string]string{
		exportcodec.ScriptJSONFileName: `{"version":"v1","script_id":"s-1","script":{"component_name":"s","description":"脚本描述"}}`,
	})
	if got := exportcodec.StoreDescription(scriptStore, "script"); got != "脚本描述" {
		t.Fatalf("script description = %q, want 脚本描述", got)
	}

	if got := exportcodec.StoreDescription(scriptStore, "unknown"); got != "" {
		t.Fatalf("unsupported store_type description = %q, want empty", got)
	}
	if got := exportcodec.StoreDescription("", "workflow"); got != "" {
		t.Fatalf("empty store dir description = %q, want empty", got)
	}
	if got := exportcodec.StoreDescription(filepath.Join(t.TempDir(), "not-exists"), "workflow"); got != "" {
		t.Fatalf("missing store dir description = %q, want empty", got)
	}

	badStore := newStoreRepoWithFiles(t, map[string]string{
		exportcodec.WorkflowJSONFileName: `{`,
	})
	if got := exportcodec.StoreDescription(badStore, "workflow"); got != "" {
		t.Fatalf("invalid workflow.json description = %q, want empty", got)
	}
}

// TestFillStorePathForPageFillsDescription 校验 PageStore 的回填行为：
// 每条 store 的 store_path 由 PathName 推导，description 从该 store 自己的仓库读取；
// 目录不存在的那条只返回空 description，不影响其它记录。
func TestFillStorePathForPageFillsDescription(t *testing.T) {
	baseDir := t.TempDir()
	cfg := &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}}

	storeDir := utils.GetWorkflowOrScriptStoreDir(baseDir, "wf-path")
	src := t.TempDir()
	commitTestRepo(t, src, map[string]string{
		exportcodec.WorkflowJSONFileName: `{"version":"v1","workflow_id":"wf-1","workflow":{"name":"wf","description":"来自仓库的描述"}}`,
	})
	if _, err := utils.EnsureBareGitRepo(storeDir); err != nil {
		t.Fatalf("utils.EnsureBareGitRepo: %v", err)
	}
	if _, err := utils.PushDirToRepo(context.Background(), src, storeDir); err != nil {
		t.Fatalf("utils.PushDirToRepo: %v", err)
	}

	existing := &types.StoreDTO{Store: types.Store{ID: 1, StoreType: "workflow", PathName: "wf-path"}}
	missing := &types.StoreDTO{Store: types.Store{ID: 2, StoreType: "workflow", PathName: "no-such-path"}}
	result := &types.PageResult{Data: []*types.StoreDTO{existing, missing, nil}}

	fillStorePathForPage(cfg, result)

	if existing.StorePath != storeDir {
		t.Fatalf("store_path = %q, want %q", existing.StorePath, storeDir)
	}
	if existing.Description != "来自仓库的描述" {
		t.Fatalf("description = %q, want 来自仓库的描述", existing.Description)
	}
	if missing.StorePath != utils.GetWorkflowOrScriptStoreDir(baseDir, "no-such-path") {
		t.Fatalf("missing store_path = %q", missing.StorePath)
	}
	if missing.Description != "" {
		t.Fatalf("missing description = %q, want empty", missing.Description)
	}

	// nil result / 非 store DTO 的 data 必须安全返回。
	fillStorePathForPage(cfg, nil)
	fillStorePathForPage(cfg, &types.PageResult{Data: []*types.Workflow{}})
}
