package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// newTestFileContentService writes files into a temp base dir and returns a
// service rooted there so path resolution works without a full config.
func newTestFileContentService(t *testing.T, files map[string]string) *fileContentService {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return &fileContentService{baseDir: dir}
}

// TestReadFileContentOversizedLineStillAdvances verifies that a single line
// wider than the byte budget does not produce an empty page (which would make
// Offset-based paging loop forever).
func TestReadFileContentOversizedLineStillAdvances(t *testing.T) {
	file := strings.Repeat("A", 100) + "\nsecond\n"
	svc := newTestFileContentService(t, map[string]string{"big.log": file})
	ctx := context.Background()

	first, err := svc.ReadFileContent(ctx, interfaces.FileContentInput{
		FilePath: "big.log",
		MaxBytes: 16,
	})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if first.LineCount == 0 {
		t.Fatalf("expected at least one line, got none (content=%q)", first.Content)
	}
	if first.NextOffset <= 0 {
		t.Fatalf("expected next_offset > 0, got %d", first.NextOffset)
	}
	if !first.HasMore || !first.Truncated {
		t.Fatalf("expected has_more and truncated, got has_more=%v truncated=%v", first.HasMore, first.Truncated)
	}
	if len(first.Content) > 16 {
		t.Fatalf("content exceeded budget: %d bytes", len(first.Content))
	}

	// Continuing from next_offset must progress and terminate.
	second, err := svc.ReadFileContent(ctx, interfaces.FileContentInput{
		FilePath: "big.log",
		Offset:   first.NextOffset,
		MaxBytes: 16,
	})
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if second.LineCount != 1 || second.Content != "second" {
		t.Fatalf("expected second line, got line_count=%d content=%q", second.LineCount, second.Content)
	}
	if second.HasMore {
		t.Fatalf("expected has_more=false at EOF, got true")
	}
}

// TestReadFileContentCSVOversizedRecordStillAdvances covers the table path,
// where a single record larger than the budget previously yielded line_count=0.
func TestReadFileContentCSVOversizedRecordStillAdvances(t *testing.T) {
	record := strings.Repeat("B", 100)
	svc := newTestFileContentService(t, map[string]string{"big.csv": record + "\n" + "x,y\n"})
	ctx := context.Background()

	first, err := svc.ReadFileContent(ctx, interfaces.FileContentInput{
		FilePath: "big.csv",
		MaxBytes: 8,
	})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if first.LineCount != 1 {
		t.Fatalf("expected one truncated row, got %d (content=%q)", first.LineCount, first.Content)
	}
	if first.NextOffset != 1 {
		t.Fatalf("expected next_offset=1, got %d", first.NextOffset)
	}
	if !first.Truncated {
		t.Fatalf("expected truncated=true")
	}

	second, err := svc.ReadFileContent(ctx, interfaces.FileContentInput{
		FilePath: "big.csv",
		Offset:   first.NextOffset,
		MaxBytes: 8,
	})
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if second.LineCount != 1 || second.Content != "x,y\n" {
		t.Fatalf("expected second record, got line_count=%d content=%q", second.LineCount, second.Content)
	}
}

// TestReadFileContentOffsetBeyondEOF checks the terminating case still reports
// that the file has been fully read instead of looping.
func TestReadFileContentOffsetBeyondEOF(t *testing.T) {
	svc := newTestFileContentService(t, map[string]string{"small.txt": "a\nb\n"})
	ctx := context.Background()

	res, err := svc.ReadFileContent(ctx, interfaces.FileContentInput{
		FilePath: "small.txt",
		Offset:   10,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if res.LineCount != 0 || res.HasMore {
		t.Fatalf("expected empty page, got line_count=%d has_more=%v", res.LineCount, res.HasMore)
	}
	if res.Message == "" {
		t.Fatalf("expected an out-of-range message")
	}
}

// TestReadFileContentDirectoryReturnsTree verifies that a directory path returns
// a file tree (for the preview UI) instead of an "is a directory" error.
func TestReadFileContentDirectoryReturnsTree(t *testing.T) {
	svc := newTestFileContentService(t, map[string]string{
		"data/b.txt":     "b",
		"data/a.txt":     "aa",
		"data/sub/c.txt": "ccc",
	})
	ctx := context.Background()

	res, err := svc.ReadFileContent(ctx, interfaces.FileContentInput{FilePath: "data"})
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if res.Kind != interfaces.FilePreviewKindTree {
		t.Fatalf("expected kind=tree, got %q", res.Kind)
	}
	if len(res.Tree) != 3 {
		t.Fatalf("expected 3 root entries, got %d (%+v)", len(res.Tree), res.Tree)
	}
	// Directories come first, then files in name order.
	if !res.Tree[0].IsDir || res.Tree[0].Name != "sub" {
		t.Fatalf("expected dir 'sub' first, got %+v", res.Tree[0])
	}
	if len(res.Tree[0].Children) != 1 || res.Tree[0].Children[0].Path != "/sub/c.txt" {
		t.Fatalf("unexpected children: %+v", res.Tree[0].Children)
	}
	if res.Tree[1].Name != "a.txt" || res.Tree[1].Size == nil || *res.Tree[1].Size != 2 {
		t.Fatalf("unexpected second node: %+v", res.Tree[1])
	}
	if res.Truncated {
		t.Fatalf("expected truncated=false for a small dir")
	}
	if !strings.Contains(res.Content, "sub/") || !strings.Contains(res.Content, "a.txt") {
		t.Fatalf("expected indented text listing, got %q", res.Content)
	}
	if res.Message == "" {
		t.Fatalf("expected an entry-count message")
	}
}

// TestReadFileContentDirectoryDepthLimit verifies the tree stops expanding past
// fileTreeMaxDepth and marks the deepest directory as truncated.
func TestReadFileContentDirectoryDepthLimit(t *testing.T) {
	deep := "d1/d2/d3/d4/leaf.txt"
	svc := newTestFileContentService(t, map[string]string{"root/" + deep: "x"})
	ctx := context.Background()

	res, err := svc.ReadFileContent(ctx, interfaces.FileContentInput{FilePath: "root"})
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(res.Tree) != 1 || res.Tree[0].Name != "d1" {
		t.Fatalf("expected root entry d1, got %+v", res.Tree)
	}
	d2 := res.Tree[0].Children
	if len(d2) != 1 || d2[0].Name != "d2" {
		t.Fatalf("expected d2, got %+v", d2)
	}
	d3 := d2[0].Children
	if len(d3) != 1 || d3[0].Name != "d3" {
		t.Fatalf("expected d3, got %+v", d3)
	}
	if !d3[0].Truncated {
		t.Fatalf("expected d3 to be marked truncated at the depth limit")
	}
	if len(d3[0].Children) != 0 {
		t.Fatalf("expected d3 children not expanded, got %+v", d3[0].Children)
	}
}
