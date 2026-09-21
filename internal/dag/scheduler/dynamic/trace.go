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
//
// Rows come from two sources, both on the run goroutine: the scheduler's own
// decisions (node.create / node.demote / node.rerun / node.abort / node.submit,
// plus cache.reset), and the runtime node events delivered to the run's sink
// (node.running / node.completed / node.failed), which are the only notice the
// scheduler gets that a node actually started or reached a terminal state.
const (
	traceEventRunBegin        = "run.begin"
	traceEventRunResume       = "run.resume"
	traceEventRunEnd          = "run.end"
	traceEventNodeCreate      = "node.create"
	traceEventNodeDemote      = "node.demote"
	traceEventNodeRerun       = "node.rerun"
	traceEventNodeAbort       = "node.abort"
	traceEventNodeSubmit      = "node.submit"
	traceEventNodeRunning     = "node.running"
	traceEventNodeCompleted   = "node.completed"
	traceEventNodeFailed      = "node.failed"
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

// nodeTraceFilter is the dynamic scheduler's sink whitelist.
//
// It is the shared scheduling filter plus the running transition. Scheduling only
// needs the terminal transitions - they are the only ones that can change derived
// readiness - but the trace also wants to show that a node actually started, and
// that event is dropped before it would ever reach the sink.
//
// Only this scheduler's sink is widened, so the dataflow scheduler keeps the
// original, narrower delivery set.
func nodeTraceFilter(evt dagruntime.RuntimeEvent) bool {
	if dagruntime.SchedulerEventFilter(evt) {
		return true
	}
	return isNodeRunningEvent(evt)
}

// isNodeRunningEvent reports whether evt is the running transition: the one
// delivered event that cannot change derived readiness.
func isNodeRunningEvent(evt dagruntime.RuntimeEvent) bool {
	return strings.TrimSpace(evt.Name) == dagruntime.EventNodeRunning
}

// runtimeEventToTraceEvent maps a bus event to its trace event name and level.
//
// The two vocabularies are kept apart on purpose: trace rows are read by people
// and grepped by scripts, so their names stay stable even if bus event names are
// renamed.
func runtimeEventToTraceEvent(name string) (string, string) {
	switch strings.TrimSpace(name) {
	case dagruntime.EventNodeRunning:
		return traceEventNodeRunning, trace.LevelInfo
	case dagruntime.EventNodeFailed:
		return traceEventNodeFailed, trace.LevelError
	default:
		return traceEventNodeCompleted, trace.LevelInfo
	}
}

// traceNodeEvent records one runtime node transition: the node started running, or
// it reached a terminal state.
//
// The event is the only notice the scheduler gets of those states - the node
// dispatcher and the completion coordinator are the ones that drive them - so the
// row is written here, in the run loop, where the event is already delivered,
// instead of being polled out of the node table.
//
// The node row is re-read rather than trusting the event payload so a traced row
// carries the same identity columns (analysis_node_id, node_name, sample_id,
// script_id) as every other node row in the file, and so the status is the one
// that was actually persisted.
func (o *dynamicDagOrchestratorV2) traceNodeEvent(ctx context.Context, analysisID int64, evt dagruntime.RuntimeEvent) {
	tracer := o.tracer(analysisID)
	if tracer == nil {
		// Untraced run: skip the lookup rather than pay for a row nobody keeps.
		return
	}

	event, level := runtimeEventToTraceEvent(evt.Name)
	node, err := o.repo.GetAnalysisNodeByID(ctx, evt.AnalysisNodeID)
	if err != nil || node == nil {
		// Degrade instead of dropping the row: node_id still identifies the node, and
		// the payload still carries the status and the reason.
		logger.Debugf(ctx,
			"[DynamicDagOrchestratorV2] resolve traced node failed, analysis_id=%d analysis_node_pk=%d err=%v",
			analysisID, evt.AnalysisNodeID, err)
		tracer.Log(trace.New(level, event).
			Set(trace.ColumnNodeID, evt.NodeID).
			Set(trace.ColumnStatus, dynamicToString(evt.Payload["status"])).
			Set(trace.ColumnError, dynamicToString(evt.Payload["error"])))
		return
	}

	status := strings.TrimSpace(dynamicToString(evt.Payload["status"]))
	if status == "" {
		// A running transition and the dispatcher's failure path carry no status in
		// the payload; the persisted row is the authoritative value.
		status = node.Status
	}
	errorMessage := strings.TrimSpace(dynamicToString(evt.Payload["error"]))
	if errorMessage == "" {
		errorMessage = strings.TrimSpace(node.ErrorMessage)
	}

	tracer.Log(nodeRecord(level, event, node).
		Set(trace.ColumnStatus, status).
		Set(trace.ColumnError, errorMessage))
}
