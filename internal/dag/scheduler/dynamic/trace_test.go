package dynamic

import (
	"os"
	"strings"
	"testing"

	"github.com/biox-dev/gobrave/internal/dag/trace"
	"github.com/biox-dev/gobrave/internal/types"
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
