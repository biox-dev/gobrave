package trace

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func readTrace(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read trace %s: %v", path, err)
	}
	body := strings.TrimSuffix(string(raw), "\n")
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

func openTestTracer(t *testing.T, path string) *Tracer {
	t.Helper()
	tracer, err := Open(path, NodeSchema())
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	if tracer == nil {
		t.Fatalf("Open(%s) returned a nil tracer", path)
	}
	return tracer
}

func TestNodeSchemaLayout(t *testing.T) {
	want := []string{
		ColumnTimestamp, ColumnLevel, ColumnEvent,
		ColumnAnalysisNodeID, ColumnNodeID, ColumnNodeName, ColumnStatus,
		ColumnSampleID, ColumnScriptID, ColumnRerunReason, ColumnError,
	}
	got := NodeSchema().Columns()
	if len(got) != len(want) {
		t.Fatalf("columns = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("column %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestNewSchemaDropsBlanksAndDuplicates(t *testing.T) {
	got := NewSchema(" a ", "", "b", "a", "b")
	if strings.Join(got.Columns(), ",") != "a,b" {
		t.Fatalf("columns = %v, want [a b]", got.Columns())
	}
	if got.Header() != "a"+ColumnSeparator+"b" {
		t.Fatalf("header = %q", got.Header())
	}
}

func TestOpenCreatesParentDirectories(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs", "42", "trace.log")
	tracer := openTestTracer(t, path)
	if err := tracer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("stat trace: %v", err)
	}
}

func TestOpenRequiresSchema(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "trace.log"), nil); err == nil {
		t.Fatal("Open with a nil schema must fail")
	}
}

func TestOpenBlankPathIsUntracedNotAnError(t *testing.T) {
	tracer, err := Open("   ", NodeSchema())
	if err != nil || tracer != nil {
		t.Fatalf("Open(blank) = (%v, %v), want (nil, nil)", tracer, err)
	}
}

func TestOpenWritesHeaderAndRowsInSchemaOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.log")
	tracer := openTestTracer(t, path)
	tracer.Log(New(LevelInfo, "node.submit").
		Set(ColumnAnalysisNodeID, "node-1").
		Set(ColumnNodeID, "A").
		Set(ColumnStatus, "submitted"))
	tracer.Log(New(LevelError, "run.end").
		Set(ColumnStatus, "failed").
		SetError(errors.New("boom")))
	if err := tracer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readTrace(t, path)
	if len(lines) != 3 {
		t.Fatalf("want header + 2 rows, got %d lines: %q", len(lines), lines)
	}
	if lines[0] != NodeSchema().Header() {
		t.Fatalf("header = %q, want %q", lines[0], NodeSchema().Header())
	}

	first := strings.Split(lines[1], ColumnSeparator)
	if len(first) != 11 {
		t.Fatalf("row split into %d cells, want 11: %q", len(first), lines[1])
	}
	if _, err := time.Parse(TimestampFormat, first[0]); err != nil {
		t.Fatalf("timestamp %q is not parseable: %v", first[0], err)
	}
	for index, want := range map[int]string{
		1: LevelInfo, 2: "node.submit", 3: "node-1", 4: "A", 6: "submitted",
	} {
		if first[index] != want {
			t.Errorf("cell %d = %q, want %q", index, first[index], want)
		}
	}
	// Columns the event never set must be empty cells, not missing columns, or the
	// table would stop being parseable by position.
	for _, index := range []int{5, 7, 8, 9, 10} {
		if first[index] != "" {
			t.Errorf("cell %d = %q, want empty", index, first[index])
		}
	}

	second := strings.Split(lines[2], ColumnSeparator)
	if len(second) != 11 {
		t.Fatalf("second row split into %d cells, want 11: %q", len(second), lines[2])
	}
	if second[2] != "run.end" || second[6] != "failed" || second[10] != "boom" {
		t.Fatalf("unexpected second row: %q", lines[2])
	}
}

func TestLogIgnoresBlankEventAndUnknownColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.log")
	tracer := openTestTracer(t, path)
	tracer.Log(nil)
	tracer.Log(New(LevelInfo, "   "))
	tracer.Log(New(LevelInfo, "node.create").
		Set("not_a_column", "dropped").
		Set(ColumnStatus, "ready"))
	if err := tracer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readTrace(t, path)
	if len(lines) != 2 {
		t.Fatalf("want header + 1 row, got %q", lines)
	}
	if strings.Contains(lines[1], "dropped") {
		t.Fatalf("an undeclared column leaked into the row: %q", lines[1])
	}
}

func TestRowValuesStayOnOneLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.log")
	tracer := openTestTracer(t, path)
	tracer.Log(New(LevelError, "run.end").
		Set(ColumnError, "line one\nline two\twith tab"))
	if err := tracer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readTrace(t, path)
	if len(lines) != 2 {
		t.Fatalf("a multi-line value split the table into %d lines: %q", len(lines), lines)
	}
	cells := strings.Split(lines[1], ColumnSeparator)
	if len(cells) != 11 {
		t.Fatalf("row split into %d cells, want 11: %q", len(cells), lines[1])
	}
	if cells[10] != "line one line two with tab" {
		t.Fatalf("error cell = %q", cells[10])
	}
}

func TestOpenTruncatesPreviousRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.log")

	first := openTestTracer(t, path)
	first.Log(New(LevelInfo, "run.begin"))
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := openTestTracer(t, path)
	second.Log(New(LevelInfo, "run.resume"))
	if err := second.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readTrace(t, path)
	if len(lines) != 2 {
		t.Fatalf("want header + 1 row, got %q", lines)
	}
	if !strings.Contains(lines[1], "run.resume") || strings.Contains(lines[1], "run.begin") {
		t.Fatalf("reopening kept the previous run: %q", lines)
	}
}

func TestLogAfterCloseIsDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.log")
	tracer := openTestTracer(t, path)
	if err := tracer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := tracer.Close(); err != nil {
		t.Fatalf("second Close must be a no-op, got %v", err)
	}

	tracer.Log(New(LevelInfo, "node.submit"))
	if lines := readTrace(t, path); len(lines) != 1 {
		t.Fatalf("a closed tracer wrote a row: %q", lines)
	}
}

func TestNilTracerAndRegistryAreSafe(t *testing.T) {
	var tracer *Tracer
	tracer.Log(New(LevelInfo, "node.submit"))
	if err := tracer.Close(); err != nil {
		t.Fatalf("nil tracer Close = %v, want nil", err)
	}

	var registry *Registry[int64]
	if registry.Get(1) != nil {
		t.Fatal("nil registry must report no tracer")
	}
	registry.Close(1)

	opened, err := registry.Open(1, filepath.Join(t.TempDir(), "trace.log"), NodeSchema())
	if err != nil {
		t.Fatalf("Open on a nil registry: %v", err)
	}
	if opened != nil {
		t.Fatal("a nil registry must not hand out a live tracer")
	}
}

func TestRegistryOpenReplacesAndClosesPreviousTracer(t *testing.T) {
	registry := NewRegistry[int64]()
	path := filepath.Join(t.TempDir(), "trace.log")

	if _, err := registry.Open(7, path, NodeSchema()); err != nil {
		t.Fatalf("first Open: %v", err)
	}
	previous := registry.Get(7)
	if previous == nil {
		t.Fatal("Get after Open must return the tracer")
	}

	current, err := registry.Open(7, path, NodeSchema())
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	if current == nil || current == previous {
		t.Fatalf("second Open must install a fresh tracer, got %v", current)
	}

	// The replaced tracer was closed, so its rows cannot land in the new file.
	previous.Log(New(LevelInfo, "stale"))
	if lines := readTrace(t, path); len(lines) != 1 {
		t.Fatalf("a replaced tracer wrote a row: %q", lines)
	}

	registry.Close(7)
	if registry.Get(7) != nil {
		t.Fatal("Close must forget the tracer")
	}
	registry.Close(7) // idempotent for a key that is already gone
}

func TestTracerIsSafeForConcurrentWriters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trace.log")
	tracer := openTestTracer(t, path)

	const writers, perWriter = 8, 25
	var wg sync.WaitGroup
	for writer := 0; writer < writers; writer++ {
		wg.Add(1)
		go func(writer int) {
			defer wg.Done()
			for row := 0; row < perWriter; row++ {
				tracer.Log(New(LevelInfo, "node.submit").
					Set(ColumnAnalysisNodeID, fmt.Sprintf("n-%d", writer)))
			}
		}(writer)
	}
	wg.Wait()
	if err := tracer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	lines := readTrace(t, path)
	if want := 1 + writers*perWriter; len(lines) != want {
		t.Fatalf("got %d lines, want %d", len(lines), want)
	}
	for _, line := range lines[1:] {
		if cells := strings.Split(line, ColumnSeparator); len(cells) != 11 {
			t.Fatalf("torn row (%d cells): %q", len(cells), line)
		}
	}
}
