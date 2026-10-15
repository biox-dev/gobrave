package dag

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/biox-dev/gobrave/internal/config"
	"github.com/biox-dev/gobrave/internal/types"
)

// 节点成功后，output.md 里的相对图片路径会被改写成以数据目录为根的绝对路径，
// 这样报告聚合/HTML 导出/前端渲染都不用再各自维护图片前缀。
func TestRewriteNodeOutputMarkdownImages(t *testing.T) {
	baseDir := t.TempDir()
	outputDir := filepath.Join(baseDir, "data", "34", "analysis_node", "100", "200", "output")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	path := filepath.Join(outputDir, types.DefaultProjectReportFilename)
	if err := os.WriteFile(path, []byte("![a](plot.png)\n\n![b](/images/x.png)\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	c := &NodeCompletionCoordinator{cfg: &config.Config{Storage: &config.StorageConfig{BaseDir: baseDir}}}
	node := &types.AnalysisNode{OutputDir: outputDir}
	c.rewriteNodeOutputMarkdownImages(context.Background(), node)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := "![a](/34/analysis_node/100/200/output/plot.png)\n\n![b](/images/x.png)\n"
	if string(got) != want {
		t.Fatalf("output.md = %q, want %q", string(got), want)
	}

	// 幂等：再次执行不会重复拼接前缀。
	c.rewriteNodeOutputMarkdownImages(context.Background(), node)
	again, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(again) != want {
		t.Fatalf("output.md after second run = %q, want %q", string(again), want)
	}
}

// 没有 output.md、目录为空或 cfg 缺失时都必须静默跳过，不能影响节点结论。
func TestRewriteNodeOutputMarkdownImagesSkipsMissingInputs(t *testing.T) {
	baseDir := t.TempDir()
	outputDir := filepath.Join(baseDir, "data", "34", "output")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	node := &types.AnalysisNode{OutputDir: outputDir}
	(&NodeCompletionCoordinator{}).rewriteNodeOutputMarkdownImages(context.Background(), node)
	(&NodeCompletionCoordinator{}).rewriteNodeOutputMarkdownImages(context.Background(), nil)
	if _, err := os.Stat(filepath.Join(outputDir, types.DefaultProjectReportFilename)); !os.IsNotExist(err) {
		t.Fatalf("expected no output.md to be created, stat err = %v", err)
	}
}

// storage.base_dir 缺失时保留相对路径（绝不能把本机磁盘绝对路径写进 output.md）。
func TestRewriteNodeOutputMarkdownImagesSkipsWithoutBaseDir(t *testing.T) {
	outputDir := t.TempDir()
	path := filepath.Join(outputDir, types.DefaultProjectReportFilename)
	const original = "![a](plot.png)\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	node := &types.AnalysisNode{OutputDir: outputDir}
	(&NodeCompletionCoordinator{}).rewriteNodeOutputMarkdownImages(context.Background(), node)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != original {
		t.Fatalf("output.md = %q, want unchanged %q", string(got), original)
	}
}
