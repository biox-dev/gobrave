package htmlreport

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
