package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
	"github.com/biox-dev/gobrave/internal/utils"
	"gorm.io/gorm"
)

type packageStubAnalysisRepo struct {
	interfaces.AnalysisRepository
	nodes    map[int64]*types.AnalysisNode
	analyses map[int64]*types.Analysis
}

func (r *packageStubAnalysisRepo) GetAnalysisNodeByID(_ context.Context, id int64) (*types.AnalysisNode, error) {
	if node, ok := r.nodes[id]; ok {
		return node, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func (r *packageStubAnalysisRepo) GetAnalysisByID(_ context.Context, id int64) (*types.Analysis, error) {
	if analysis, ok := r.analyses[id]; ok {
		return analysis, nil
	}
	return nil, gorm.ErrRecordNotFound
}

type packageStubSummaryRepo struct {
	interfaces.AISummaryRepository
	summaries map[int64]*types.AISummary
}

func (r *packageStubSummaryRepo) GetAISummaryByID(_ context.Context, id int64) (*types.AISummary, error) {
	if summary, ok := r.summaries[id]; ok {
		return summary, nil
	}
	return nil, gorm.ErrRecordNotFound
}

func writePackageTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func newPackageTestService(t *testing.T, baseDir string, repo *stubProjectRepo, analysisRepo interfaces.AnalysisRepository, summaryRepo interfaces.AISummaryRepository) *projectService {
	t.Helper()
	return &projectService{
		projectRepo:  repo,
		analysisRepo: analysisRepo,
		summaryRepo:  summaryRepo,
		cfg:          &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}},
	}
}

// TestPackageProjectReportItemsBuildsTree 验证打包目录层级、目录名（标题）以及各 owner_type 的内容落盘方式。
func TestPackageProjectReportItemsBuildsTree(t *testing.T) {
	baseDir := t.TempDir()
	nodeOutput := filepath.Join(baseDir, "data", "7", "analysis_node", "100", "output")
	writePackageTestFile(t, filepath.Join(nodeOutput, "result.txt"), "node output")
	writePackageTestFile(t, filepath.Join(baseDir, "data", "7", "AiSummary", "200", "summary.md"), "# summary")
	// 报告自身正文：应被拷贝到打包目录根部。
	writePackageTestFile(t, utils.GetProjectReportOutputFile(filepath.Join(baseDir, "data", "7", "report", "42")), "# report")

	repo := &stubProjectRepo{
		report: &types.ProjectReport{ID: 42, ProjectID: 7},
		items: []*types.ProjectReportItem{
			{ID: 1, ProjectReportID: 42, OwnerType: types.ProjectReportItemOwnerCustom, Title: "Intro", Content: "# intro", SortOrder: 0},
			{ID: 2, ProjectReportID: 42, OwnerType: types.ProjectReportItemOwnerAnalysisNode, OwnerID: 100, SortOrder: 1},
			{ID: 3, ProjectReportID: 42, ParentID: 2, OwnerType: types.ProjectReportItemOwnerAISummary, OwnerID: 200, SortOrder: 0},
			{ID: 4, ProjectReportID: 42, OwnerType: types.ProjectReportItemOwnerAnalysis, OwnerID: 300, SortOrder: 2},
		},
	}
	svc := newPackageTestService(t, baseDir, repo,
		&packageStubAnalysisRepo{
			nodes:    map[int64]*types.AnalysisNode{100: {ID: 100, NodeName: "Node A", OutputDir: nodeOutput}},
			analyses: map[int64]*types.Analysis{300: {ID: 300, AnalysisName: "Analysis A"}},
		},
		&packageStubSummaryRepo{summaries: map[int64]*types.AISummary{200: {ID: 200, ProjectID: 7, Title: "Summary A"}}},
	)

	dir, err := svc.PackageProjectReportItems(context.Background(), 1, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantDir := filepath.Join(baseDir, "data", "7", "report", "42", "results")
	if dir != wantDir {
		t.Fatalf("results dir = %q, want %q", dir, wantDir)
	}

	assertPackageFile(t, filepath.Join(wantDir, "Intro", "README.md"), "# intro")
	assertPackageFile(t, filepath.Join(wantDir, "Node A", "result.txt"), "node output")
	assertPackageFile(t, filepath.Join(wantDir, "Node A", "Summary A", "summary.md"), "# summary")
	assertPackageFile(t, utils.GetProjectReportOutputFile(wantDir), "# report")

	// analysis 类型暂不处理，仅创建目录。
	info, err := os.Stat(filepath.Join(wantDir, "Analysis A"))
	if err != nil {
		t.Fatalf("analysis dir should exist: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("analysis path should be a directory")
	}
}

// TestPackageProjectReportItemsDeduplicatesAndSanitizesTitles 验证同级重名目录会追加序号，
// 且标题中的路径分隔符不会逃逸出结果目录。
func TestPackageProjectReportItemsDeduplicatesAndSanitizesTitles(t *testing.T) {
	baseDir := t.TempDir()
	repo := &stubProjectRepo{
		report: &types.ProjectReport{ID: 42, ProjectID: 7},
		items: []*types.ProjectReportItem{
			{ID: 1, ProjectReportID: 42, OwnerType: types.ProjectReportItemOwnerCustom, Title: "Same", Content: "a"},
			{ID: 2, ProjectReportID: 42, OwnerType: types.ProjectReportItemOwnerCustom, Title: "Same", Content: "b"},
			{ID: 3, ProjectReportID: 42, OwnerType: types.ProjectReportItemOwnerCustom, Title: "../escape", Content: "c"},
		},
	}
	svc := newPackageTestService(t, baseDir, repo,
		&packageStubAnalysisRepo{},
		&packageStubSummaryRepo{})

	dir, err := svc.PackageProjectReportItems(context.Background(), 1, 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertPackageFile(t, filepath.Join(dir, "Same", "README.md"), "a")
	assertPackageFile(t, filepath.Join(dir, "Same-2", "README.md"), "b")
	assertPackageFile(t, filepath.Join(dir, "_escape", "README.md"), "c")

	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape")); !os.IsNotExist(err) {
		t.Fatalf("sanitized title must not create a sibling directory")
	}
}

func assertPackageFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("content of %s = %q, want %q", path, string(data), want)
	}
}
