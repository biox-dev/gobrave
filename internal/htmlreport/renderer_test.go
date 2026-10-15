package htmlreport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderInlinesDataRelativeImage(t *testing.T) {
	baseDir := t.TempDir()
	// 报告正文里的图片以数据目录为根（/data-analysis 前缀由前端补），
	// 导出 HTML 时必须仍能定位到 <baseDir>/data 下的真实文件。
	writeTestImage(t, filepath.Join(baseDir, "data", "34", "analysis_node", "100", "200", "output", "a.png"), "png-bytes")

	doc, err := New().Render(Options{
		Title:        "Report",
		Markdown:     "![a](/34/analysis_node/100/200/output/a.png)",
		InlineImages: true,
		BaseDir:      baseDir,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !strings.Contains(string(doc), "data:image/png;base64,") {
		t.Fatalf("expected data-relative image to be inlined as data URI, got:\n%s", string(doc))
	}
}

func TestRenderKeepsOriginalURLsWhenNotInlining(t *testing.T) {
	doc, err := New().Render(Options{
		Title:        "Report",
		Markdown:     "![a](/data-analysis/analysis/x.png)",
		InlineImages: false,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := string(doc)
	if !strings.Contains(html, `src="/data-analysis/analysis/x.png"`) {
		t.Fatalf("expected original image URL to be preserved, got:\n%s", html)
	}
	if strings.Contains(html, "data:image") {
		t.Fatalf("did not expect inlined data URI, got:\n%s", html)
	}
}

func TestRenderInlinesLocalImage(t *testing.T) {
	baseDir := t.TempDir()
	writeTestImage(t, filepath.Join(baseDir, "data", "analysis", "x.png"), "png-bytes")

	doc, err := New().Render(Options{
		Title:        "Report",
		Markdown:     "![a](/data-analysis/analysis/x.png)",
		InlineImages: true,
		BaseDir:      baseDir,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if !strings.Contains(string(doc), "data:image/png;base64,") {
		t.Fatalf("expected image to be inlined as data URI, got:\n%s", string(doc))
	}
}

func TestRenderLeavesRemoteAndMissingImagesUntouched(t *testing.T) {
	baseDir := t.TempDir()

	doc, err := New().Render(Options{
		Title:        "Report",
		Markdown:     "![r](https://example.com/a.png)\n\n![m](/data-analysis/missing.png)",
		InlineImages: true,
		BaseDir:      baseDir,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	html := string(doc)
	if !strings.Contains(html, `src="https://example.com/a.png"`) {
		t.Fatalf("remote image should be untouched, got:\n%s", html)
	}
	if !strings.Contains(html, `src="/data-analysis/missing.png"`) {
		t.Fatalf("missing local image should keep its URL, got:\n%s", html)
	}
	if strings.Contains(html, "data:image") {
		t.Fatalf("did not expect any inlined data URI, got:\n%s", html)
	}
}

func TestRenderBlocksPathTraversal(t *testing.T) {
	baseDir := t.TempDir()

	doc, err := New().Render(Options{
		Title:        "Report",
		Markdown:     "![x](/data-analysis/../../../../etc/hostname)",
		InlineImages: true,
		BaseDir:      baseDir,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	if strings.Contains(string(doc), "data:image") {
		t.Fatalf("path traversal should not be inlined, got:\n%s", string(doc))
	}
}

func writeTestImage(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
