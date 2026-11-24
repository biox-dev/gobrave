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
