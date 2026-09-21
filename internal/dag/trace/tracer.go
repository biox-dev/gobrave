package trace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// TimestampFormat is the layout of ColumnTimestamp. Microseconds are deliberate:
// several scheduler events for one node routinely land inside the same
// millisecond, and their relative order is the interesting part.
const TimestampFormat = "2006-01-02T15:04:05.000000Z07:00"

// Tracer appends rows to one trace file.
//
// A nil *Tracer is valid and drops every row, so best-effort tracing never needs
// a nil check at the call site. All methods are safe for concurrent use.
type Tracer struct {
	mu     sync.Mutex
	file   *os.File
	schema *Schema
	closed bool
}

// Open creates path (truncating any existing file) and writes the schema header,
// so every run starts from an empty table that describes exactly that run.
//
// A blank path is not an error: it returns (nil, nil) and the caller simply runs
// untraced, which is what keeps tracing optional.
func Open(path string, schema *Schema) (*Tracer, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	if schema == nil || len(schema.Columns()) == 0 {
		return nil, fmt.Errorf("trace schema is required")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create trace directory %q failed: %w", dir, err)
		}
	}
	// os.Create truncates: one trace file per run, never a mix of two attempts.
	file, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create trace file %q failed: %w", path, err)
	}
	if _, err := file.WriteString(schema.Header() + "\n"); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write trace header %q failed: %w", path, err)
	}
	return &Tracer{file: file, schema: schema}, nil
}

// Log appends one record.
//
// It is a no-op for a nil tracer, a closed tracer, or a record without an event
// name, so callers can trace unconditionally.
func (t *Tracer) Log(record *Record) {
	if t == nil || record == nil || record.event == "" {
		return
	}
	line := t.schema.render(record, time.Now().UTC().Format(TimestampFormat))

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.file == nil {
		return
	}
	// One write per row, no buffering: a crashed run still leaves every line it
	// managed to report readable on disk, which is the whole point of a trace.
	_, _ = t.file.WriteString(line + "\n")
}

// Close releases the underlying file. It is idempotent.
func (t *Tracer) Close() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil
	}
	t.closed = true
	if t.file == nil {
		return nil
	}
	err := t.file.Close()
	t.file = nil
	return err
}
