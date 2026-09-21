package dynamic

import (
	"context"
	"strings"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/dag/trace"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
)

// Run tracing for the dynamic scheduler.
//
// Every run writes one append-only table to analysis.TraceFile
// (<workspace_dir>/trace.log): a header line, then one row per scheduler
// observation. Nothing is updated in place - the same node simply appears on
// several rows as it moves ready -> submitted -> running -> done - which is what
// lets a row be written with a single append and still be read as a table.
//
// The contract is:
//
//   - one file per run; opening truncates it, so a trace never mixes attempts;
//   - node rows carry at least analysis_node_id and status;
//   - tracing is best effort - a blank path or an unwritable file only warns,
//     because a missing trace must never be the reason a DAG fails to run.
//
// The registry (instead of a *trace.Tracer threaded through every method of the
// run) keeps the call sites in dag_orchestrator_v2.go to
// `o.trace(analysisID, record)`, so instrumenting another step stays a one-line
// change rather than a signature change through the call chain.
const (
	traceEventRunBegin        = "run.begin"
	traceEventRunResume       = "run.resume"
	traceEventRunEnd          = "run.end"
	traceEventNodeCreate      = "node.create"
	traceEventNodeDemote      = "node.demote"
	traceEventNodeRerun       = "node.rerun"
	traceEventNodeAbort       = "node.abort"
	traceEventNodeSubmit      = "node.submit"
	traceEventSubmitCancelled = "node.submit_cancelled"
	traceEventNodeResumeReset = "node.resume_reset"
	traceEventCacheReset      = "cache.reset"
)

// traceRegistry returns the per-run tracer registry, creating it on first use.
//
// The lazy creation is what lets the orchestrator struct be assembled directly -
// the tests do that, and a future embedder might - without every caller having to
// remember the registry. The lock is only taken on the first traced run, since
// the cached pointer is read-only afterwards.
func (o *dynamicDagOrchestratorV2) traceRegistry() *trace.Registry[int64] {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.traces == nil {
		o.traces = trace.NewRegistry[int64]()
	}
	return o.traces
}

// tracer returns the tracer of one run, or nil when the run is not traced.
func (o *dynamicDagOrchestratorV2) tracer(analysisID int64) *trace.Tracer {
	o.mu.Lock()
	registry := o.traces
	o.mu.Unlock()
	return registry.Get(analysisID)
}

// trace appends one record to the run's trace.
//
// It is a no-op when the run is not traced, so any step can trace unconditionally
// without a guard (a nil *trace.Tracer drops the row).
func (o *dynamicDagOrchestratorV2) trace(analysisID int64, record *trace.Record) {
	o.tracer(analysisID).Log(record)
}

// openRunTrace starts a fresh trace for one run.
//
// The file is truncated here, which is what makes a trace describe exactly one
// attempt: a resumed or restarted run rewrites it from the beginning instead of
// appending to evidence of an earlier run.
//
// Tracing is best effort. A blank destination or an unwritable file only warns,
// because losing the trace must not abort the run it was meant to explain.
func (o *dynamicDagOrchestratorV2) openRunTrace(analysis *types.Analysis, resume bool) {
	if analysis == nil {
		return
	}
	path := strings.TrimSpace(analysis.TraceFile)
	if _, err := o.traceRegistry().Open(analysis.ID, path, trace.NodeSchema()); err != nil {
		logger.Warnf(context.Background(),
			"[DynamicDagOrchestratorV2] open run trace failed, analysis_id=%d path=%s err=%v",
			analysis.ID, path, err)
		return
	}

	// The first row anchors the file: fresh submission and crash recovery produce
	// different scheduling, so they must not look alike when reading a trace.
	event := traceEventRunBegin
	if resume {
		event = traceEventRunResume
	}
	o.trace(analysis.ID, trace.New(trace.LevelInfo, event))
}

// closeRunTrace flushes and forgets the trace of a run.
//
// It is safe to call for a run that was never traced (or already closed), which
// is what lets failure and success paths clean up unconditionally.
func (o *dynamicDagOrchestratorV2) closeRunTrace(analysisID int64) {
	o.mu.Lock()
	registry := o.traces
	o.mu.Unlock()
	registry.Close(analysisID)
}

// nodeRecord starts a record carrying the node's identifying columns, so every
// node event is self-describing regardless of what it is about.
//
// Adding a column that every node row should carry is therefore a change here
// plus one line in the schema, not a change at every call site.
func nodeRecord(level string, event string, node *types.AnalysisNode) *trace.Record {
	record := trace.New(level, event)
	if node == nil {
		return record
	}
	return record.
		Set(trace.ColumnAnalysisNodeID, node.AnalysisNodeID).
		Set(trace.ColumnNodeID, node.NodeID).
		Set(trace.ColumnNodeName, node.NodeName).
		Set(trace.ColumnSampleID, node.SampleID).
		SetInt(trace.ColumnScriptID, node.ScriptID)
}

// nodeEventLevel rates a node transition: a skipped node is worth noticing (the
// run deliberately did not execute it), everything else is routine scheduling.
func nodeEventLevel(status string) string {
	if status == dagruntime.StatusSkipped {
		return trace.LevelWarn
	}
	return trace.LevelInfo
}
