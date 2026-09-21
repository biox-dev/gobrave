package trace

import (
	"strconv"
	"strings"
)

// Levels are plain labels: the tracer writes every record it is given and never
// filters by level, so a level is context for whoever reads the trace, not a
// switch that decides whether a row exists.
const (
	LevelDebug = "DEBUG"
	LevelInfo  = "INFO"
	LevelWarn  = "WARN"
	LevelError = "ERROR"
)

// Record is one row of a trace: a level, an event name, and the column values the
// event wants to publish.
//
// A record is only a payload. The tracer owns the destination, the timestamp and
// the ordering, so a call site describes what happened and nothing else, and the
// same record can be handed to a different sink unchanged.
type Record struct {
	level  string
	event  string
	fields []field
}

type field struct {
	name  string
	value string
}

// New starts a record. The event name should be a stable dotted identifier such
// as "node.submit": that is what a reader greps, and what stays comparable across
// runs and across schedulers.
func New(level string, event string) *Record {
	return &Record{level: level, event: strings.TrimSpace(event)}
}

// Set adds a column value; the last call for a column wins.
//
// The column must be part of the tracer's schema, otherwise it is ignored when
// the row is rendered - see Schema.
func (r *Record) Set(column string, value string) *Record {
	if r == nil {
		return nil
	}
	r.fields = append(r.fields, field{name: column, value: value})
	return r
}

// SetInt adds a numeric column value.
func (r *Record) SetInt(column string, value int64) *Record {
	return r.Set(column, strconv.FormatInt(value, 10))
}

// SetBool adds a boolean column value.
func (r *Record) SetBool(column string, value bool) *Record {
	return r.Set(column, strconv.FormatBool(value))
}

// SetError adds err's message under ColumnError. A nil error is a no-op rather
// than the literal "<nil>", so a success row stays clean.
func (r *Record) SetError(err error) *Record {
	if err == nil || r == nil {
		return r
	}
	return r.Set(ColumnError, err.Error())
}
