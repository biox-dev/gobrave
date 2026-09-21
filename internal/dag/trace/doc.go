// Package trace provides the append-only, table-shaped run trace shared by the
// DAG schedulers.
//
// # Format
//
// A trace file is a tab-separated table that is never updated in place:
//
//	line 1   header: the schema's column names, in order
//	line 2   one row per observed event
//	line 3   ...
//
// A run opens its trace by truncating the file and writing a fresh header, so a
// trace always describes exactly one attempt; everything after that is appended.
// One row is one event, which matches how a scheduler actually observes a node -
// the same node legitimately appears on several rows as it moves
// ready -> submitted -> running -> done - and it means a row never has to be
// found and rewritten.
//
// # Extending
//
// The column set is a Schema, so adding a field is a two-step change: declare the
// column (see NodeSchema for the columns the DAG schedulers share) and set it on
// the records that need it. Existing rows do not have to be migrated, because a
// column an event does not fill simply renders as an empty cell and the header
// stays a complete description of every row.
//
// # Safety
//
// Tracing is best effort and must never fail a run. A blank destination path
// yields a nil tracer, a nil *Tracer and a nil *Registry are safe to use, and
// every row is written with a single write so a crashed run still leaves a
// readable prefix of the table on disk.
package trace
