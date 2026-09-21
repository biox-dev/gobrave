package trace

// Columns of the shared DAG run trace (see NodeSchema).
//
// They are one flat namespace rather than per-scheduler fields, so that the
// legacy, dynamic and dataflow schedulers all emit rows a single reader can
// consume - adding a scheduler should not add a dialect.
const (
	// ColumnAnalysisNodeID identifies the analysis_node row the event is about.
	// It is empty on run-level rows.
	ColumnAnalysisNodeID = "analysis_node_id"
	// ColumnNodeID is the logical id of the compiled node. Unlike the record id
	// above it survives reruns, which makes it the right key when comparing two
	// traces of the same DAG.
	ColumnNodeID = "node_id"
	// ColumnNodeName is the display name of the node.
	ColumnNodeName = "node_name"
	// ColumnStatus is the status the event observed or produced. On a run-level
	// row it carries the analysis job status.
	ColumnStatus = "status"
	// ColumnSampleID is the scatter sample the node instance belongs to.
	ColumnSampleID = "sample_id"
	// ColumnScriptID is the script backing the node.
	ColumnScriptID = "script_id"
	// ColumnRerunReason explains why a reusable node was invalidated.
	ColumnRerunReason = "rerun_reason"
)

// NodeSchema is the column contract of a DAG run trace.
//
// It is a function rather than a shared variable on purpose: a caller cannot then
// mutate the schema the other schedulers render with, and rebuilding a handful of
// strings once per run costs nothing.
func NodeSchema() *Schema {
	return SystemSchema(
		ColumnAnalysisNodeID,
		ColumnNodeID,
		ColumnNodeName,
		ColumnStatus,
		ColumnSampleID,
		ColumnScriptID,
		ColumnRerunReason,
		ColumnError,
	)
}
