package trace

import "strings"

// ColumnSeparator is the field separator of every trace file.
//
// Tabs keep the output in the same family as a Nextflow -with-trace table, so the
// usual tooling for those files (cut, awk, column -t) applies unchanged.
const ColumnSeparator = "\t"

// Columns owned by the tracer itself. They are always the first columns of a
// trace table, so call sites never set them and every row carries them.
const (
	// ColumnTimestamp is when the row was written (UTC, microsecond precision).
	ColumnTimestamp = "timestamp"
	// ColumnLevel is a plain label (see the Level* constants). The tracer never
	// filters on it: it exists so a reader can tell a routine transition from a
	// warning without parsing the event name.
	ColumnLevel = "level"
	// ColumnEvent is the dotted event name, for example "node.submit".
	ColumnEvent = "event"
	// ColumnError is the standard place for a failure message. It is shared by
	// every run trace so the reason for a row can be found without knowing which
	// scheduler wrote it.
	ColumnError = "error"
)

// systemColumns is the fixed left-hand side of every trace table.
var systemColumns = []string{ColumnTimestamp, ColumnLevel, ColumnEvent}

// Schema is the ordered column contract of a trace file.
//
// The schema is the single definition of the table: the tracer writes it as the
// first line of the file and lays every row out in exactly this order. That is
// what lets a reader parse a trace by position, and what stops a new field from
// silently shifting the columns that already exist.
type Schema struct {
	columns []string
}

// NewSchema builds a schema from explicit column names, keeping the given order
// and dropping blanks and duplicates.
func NewSchema(columns ...string) *Schema {
	ordered := make([]string, 0, len(columns))
	seen := make(map[string]struct{}, len(columns))
	for _, column := range columns {
		column = strings.TrimSpace(column)
		if column == "" {
			continue
		}
		if _, ok := seen[column]; ok {
			continue
		}
		seen[column] = struct{}{}
		ordered = append(ordered, column)
	}
	return &Schema{columns: ordered}
}

// SystemSchema builds a schema whose table starts with the tracer-owned columns
// followed by the given domain columns. Every run trace should be built this way
// so all trace files share the same leading columns.
func SystemSchema(domainColumns ...string) *Schema {
	columns := make([]string, 0, len(systemColumns)+len(domainColumns))
	columns = append(columns, systemColumns...)
	columns = append(columns, domainColumns...)
	return NewSchema(columns...)
}

// Columns returns a copy of the ordered column names.
func (s *Schema) Columns() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.columns...)
}

// Header renders the schema as the trace file's first line.
func (s *Schema) Header() string {
	if s == nil {
		return ""
	}
	return strings.Join(s.columns, ColumnSeparator)
}

// render lays one record out as a single tab-separated row in schema order.
//
// Columns the record does not carry become empty cells and columns the schema
// does not declare are dropped: the header is the contract, so it must stay a
// complete and truthful description of every row.
func (s *Schema) render(record *Record, timestamp string) string {
	values := make(map[string]string, len(record.fields))
	for _, field := range record.fields {
		values[field.name] = field.value
	}
	values[ColumnTimestamp] = timestamp
	values[ColumnLevel] = record.level
	values[ColumnEvent] = record.event

	cells := make([]string, len(s.columns))
	for i, column := range s.columns {
		cells[i] = oneLine(values[column])
	}
	return strings.Join(cells, ColumnSeparator)
}

// rowBreaker keeps a value on one line. A raw newline or tab inside a value would
// split one event into several rows (or cells), which is exactly the property the
// format relies on, so they are folded into spaces.
var rowBreaker = strings.NewReplacer("\n", " ", "\r", " ", "\t", " ")

func oneLine(value string) string {
	if value == "" {
		return ""
	}
	return rowBreaker.Replace(value)
}
