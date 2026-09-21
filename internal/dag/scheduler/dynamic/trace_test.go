package dynamic

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/dag/trace"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

func readTraceLines(t *testing.T, path string) []string {
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

// TestRunTraceIsATruncatedAppendOnlyTable pins the contract the dynamic scheduler
// relies on: the destination comes from the analysis workspace, the file is a
// header plus one row per observation, node rows carry the node columns, and a
// new run rewrites the file from scratch instead of appending to the old one.
func TestRunTraceIsATruncatedAppendOnlyTable(t *testing.T) {
	analysis := &types.Analysis{ID: 7, WorkspaceDir: t.TempDir()}
	analysis.HydrateDerivedPaths()

	orchestrator := &dynamicDagOrchestratorV2{}
	orchestrator.openRunTrace(analysis, false)
	orchestrator.trace(analysis.ID, nodeRecord(trace.LevelInfo, traceEventNodeSubmit, &types.AnalysisNode{
		AnalysisNodeID: "node-record-1",
		NodeID:         "align",
		NodeName:       "Align",
		SampleID:       "sample-1",
		ScriptID:       42,
	}).Set(trace.ColumnStatus, "submitted"))
	orchestrator.closeRunTrace(analysis.ID)

	lines := readTraceLines(t, analysis.TraceFile)
	if len(lines) != 3 {
		t.Fatalf("want header + run.begin + node row, got %q", lines)
	}
	if lines[0] != trace.NodeSchema().Header() {
		t.Fatalf("header = %q, want %q", lines[0], trace.NodeSchema().Header())
	}
	if !strings.Contains(lines[1], traceEventRunBegin) {
		t.Fatalf("first row = %q, want %s", lines[1], traceEventRunBegin)
	}

	schema := trace.NodeSchema().Columns()
	cells := strings.Split(lines[2], trace.ColumnSeparator)
	if len(cells) != len(schema) {
		t.Fatalf("node row split into %d cells, want %d: %q", len(cells), len(schema), lines[2])
	}
	values := make(map[string]string, len(schema))
	for i, column := range schema {
		values[column] = cells[i]
	}
	for column, want := range map[string]string{
		trace.ColumnAnalysisNodeID: "node-record-1",
		trace.ColumnNodeID:         "align",
		trace.ColumnNodeName:       "Align",
		trace.ColumnStatus:         "submitted",
		trace.ColumnSampleID:       "sample-1",
		trace.ColumnScriptID:       "42",
	} {
		if values[column] != want {
			t.Errorf("column %s = %q, want %q", column, values[column], want)
		}
	}

	// A second run must start from an empty table, otherwise the file would mix
	// two attempts and no longer describe one run.
	orchestrator.openRunTrace(analysis, true)
	orchestrator.closeRunTrace(analysis.ID)

	lines = readTraceLines(t, analysis.TraceFile)
	if len(lines) != 2 || !strings.Contains(lines[1], traceEventRunResume) {
		t.Fatalf("a new run did not truncate the trace: %q", lines)
	}
}

// TestRunTraceIsOptionalAndNeverPanics covers the best-effort contract: an
// analysis without a workspace (and a nil analysis) must leave the run untraced
// rather than fail it, which is what lets every scheduler call trace
// unconditionally.
func TestRunTraceIsOptionalAndNeverPanics(t *testing.T) {
	orchestrator := &dynamicDagOrchestratorV2{}
	analysis := &types.Analysis{ID: 7}
	analysis.HydrateDerivedPaths()
	if analysis.TraceFile != "" {
		t.Fatalf("a workspace-less analysis must not derive a trace path, got %q", analysis.TraceFile)
	}

	orchestrator.openRunTrace(analysis, false)
	orchestrator.trace(analysis.ID, nodeRecord(trace.LevelInfo, traceEventNodeSubmit, nil))
	orchestrator.closeRunTrace(analysis.ID)

	orchestrator.openRunTrace(nil, false)
	orchestrator.closeRunTrace(analysis.ID)
}

// traceRowsByEvent reads a trace back as event name -> column -> cell, so
// assertions do not depend on column order or row position.
func traceRowsByEvent(t *testing.T, path string) map[string]map[string]string {
	t.Helper()
	lines := readTraceLines(t, path)
	if len(lines) == 0 {
		t.Fatalf("trace %s is empty", path)
	}
	columns := strings.Split(lines[0], trace.ColumnSeparator)

	rows := make(map[string]map[string]string, len(lines)-1)
	for _, line := range lines[1:] {
		cells := strings.Split(line, trace.ColumnSeparator)
		if len(cells) != len(columns) {
			t.Fatalf("row has %d cells, want %d: %q", len(cells), len(columns), line)
		}
		values := make(map[string]string, len(columns))
		for i, column := range columns {
			values[column] = cells[i]
		}
		rows[values[trace.ColumnEvent]] = values
	}
	return rows
}

func assertTraceRow(t *testing.T, row map[string]string, want map[string]string) {
	t.Helper()
	if row == nil {
		t.Fatalf("expected row is missing, want %v", want)
	}
	for column, value := range want {
		if row[column] != value {
			t.Errorf("column %s = %q, want %q", column, row[column], value)
		}
	}
}

// TestNodeTraceFilterKeepsOnlyNodeLifecycleEvents pins the delivery whitelist: the
// trace needs the running transition and both terminal events, and everything else
// the bus carries for the same analysis must stay out so it can neither wake the
// loop needlessly nor take a slot in the sink buffer.
func TestNodeTraceFilterKeepsOnlyNodeLifecycleEvents(t *testing.T) {
	for _, name := range []string{
		dagruntime.EventNodeRunning,
		dagruntime.EventNodeCompleted,
		dagruntime.EventNodeFailed,
	} {
		if !nodeTraceFilter(dagruntime.RuntimeEvent{Name: name}) {
			t.Errorf("filter dropped %s", name)
		}
	}
	for _, name := range []string{
		dagruntime.EventNodeSubmitted,
		dagruntime.EventNodeStateChange,
		dagruntime.EventDagStarted,
		dagruntime.EventDagCompleted,
		dagruntime.EventDagFailed,
		"something-else",
	} {
		if nodeTraceFilter(dagruntime.RuntimeEvent{Name: name}) {
			t.Errorf("filter delivered %s", name)
		}
	}
}

// TestTraceNodeEventRecordsRunningAndTerminalRows pins that a node's whole
// lifecycle lands in the run table with the persisted status and with the identity
// columns of the node row: the events themselves only carry the numeric key, so the
// row is what has to supply analysis_node_id.
func TestTraceNodeEventRecordsRunningAndTerminalRows(t *testing.T) {
	analysis := &types.Analysis{ID: 7, WorkspaceDir: t.TempDir()}
	analysis.HydrateDerivedPaths()

	repo := &traceNodeRepoStub{nodes: map[int64]*types.AnalysisNode{
		100: {ID: 100, AnalysisID: 7, AnalysisNodeID: "node-1", NodeID: "align", NodeName: "Align", SampleID: "s1", ScriptID: 42, Status: dagruntime.StatusRunning},
		101: {ID: 101, AnalysisID: 7, AnalysisNodeID: "node-2", NodeID: "call", NodeName: "Call", Status: dagruntime.StatusDone},
		102: {ID: 102, AnalysisID: 7, AnalysisNodeID: "node-3", NodeID: "index", NodeName: "Index", Status: dagruntime.StatusFailed, ErrorMessage: "container exited with non-zero code (1)"},
	}}
	orchestrator := &dynamicDagOrchestratorV2{repo: repo}

	orchestrator.openRunTrace(analysis, false)
	// No payload: the running event carries none, so the status must come from the row.
	orchestrator.traceNodeEvent(context.Background(), analysis.ID, dagruntime.RuntimeEvent{
		Name: dagruntime.EventNodeRunning, AnalysisID: analysis.ID, AnalysisNodeID: 100, NodeID: "align",
	})
	orchestrator.traceNodeEvent(context.Background(), analysis.ID, dagruntime.RuntimeEvent{
		Name: dagruntime.EventNodeCompleted, AnalysisID: analysis.ID, AnalysisNodeID: 101, NodeID: "call",
		Payload: map[string]any{"status": dagruntime.StatusDone, "exit_code": 0},
	})
	orchestrator.traceNodeEvent(context.Background(), analysis.ID, dagruntime.RuntimeEvent{
		Name: dagruntime.EventNodeFailed, AnalysisID: analysis.ID, AnalysisNodeID: 102, NodeID: "index",
		Payload: map[string]any{"status": dagruntime.StatusFailed, "error": "container exited with non-zero code (1)"},
	})
	orchestrator.closeRunTrace(analysis.ID)

	rows := traceRowsByEvent(t, analysis.TraceFile)
	if len(rows) != 4 {
		t.Fatalf("got %d traced events, want run.begin plus 3 node rows: %v", len(rows), rows)
	}
	assertTraceRow(t, rows[traceEventNodeRunning], map[string]string{
		trace.ColumnAnalysisNodeID: "node-1",
		trace.ColumnNodeID:         "align",
		trace.ColumnNodeName:       "Align",
		trace.ColumnStatus:         dagruntime.StatusRunning,
		trace.ColumnSampleID:       "s1",
		trace.ColumnScriptID:       "42",
	})
	assertTraceRow(t, rows[traceEventNodeCompleted], map[string]string{
		trace.ColumnAnalysisNodeID: "node-2",
		trace.ColumnStatus:         dagruntime.StatusDone,
	})
	assertTraceRow(t, rows[traceEventNodeFailed], map[string]string{
		trace.ColumnAnalysisNodeID: "node-3",
		trace.ColumnStatus:         dagruntime.StatusFailed,
		trace.ColumnError:          "container exited with non-zero code (1)",
	})
}

// TestTraceNodeEventDegradesWithoutNodeRow covers the two guards: an untraced run
// must not even look the node up, and a lookup that fails must still produce a row
// from the event instead of losing the transition.
func TestTraceNodeEventDegradesWithoutNodeRow(t *testing.T) {
	// Untraced, and with no repository: a lookup here would panic.
	untraced := &dynamicDagOrchestratorV2{}
	untraced.traceNodeEvent(context.Background(), 7, dagruntime.RuntimeEvent{
		Name: dagruntime.EventNodeRunning, AnalysisID: 7, AnalysisNodeID: 100, NodeID: "align",
	})

	analysis := &types.Analysis{ID: 7, WorkspaceDir: t.TempDir()}
	analysis.HydrateDerivedPaths()
	// The stub knows no rows, so the lookup fails.
	orchestrator := &dynamicDagOrchestratorV2{repo: &traceNodeRepoStub{}}

	orchestrator.openRunTrace(analysis, false)
	orchestrator.traceNodeEvent(context.Background(), analysis.ID, dagruntime.RuntimeEvent{
		Name: dagruntime.EventNodeFailed, AnalysisID: analysis.ID, AnalysisNodeID: 404, NodeID: "missing",
		Payload: map[string]any{"error": "boom"},
	})
	orchestrator.closeRunTrace(analysis.ID)

	row := traceRowsByEvent(t, analysis.TraceFile)[traceEventNodeFailed]
	assertTraceRow(t, row, map[string]string{
		trace.ColumnNodeID: "missing",
		trace.ColumnError:  "boom",
	})
	if row[trace.ColumnAnalysisNodeID] != "" {
		t.Fatalf("a degraded row must leave analysis_node_id empty, got %q", row[trace.ColumnAnalysisNodeID])
	}
}

// traceNodeRepoStub resolves nodes by primary key, the way the runtime events carry
// them: the events only know the numeric id, while the trace table is keyed by the
// analysis_node_id of the row.
type traceNodeRepoStub struct {
	interfaces.AnalysisRepository
	nodes map[int64]*types.AnalysisNode
}

func (r *traceNodeRepoStub) GetAnalysisNodeByID(_ context.Context, analysisNodeID int64) (*types.AnalysisNode, error) {
	if node, ok := r.nodes[analysisNodeID]; ok {
		return node, nil
	}
	return nil, fmt.Errorf("analysis node %d not found", analysisNodeID)
}
