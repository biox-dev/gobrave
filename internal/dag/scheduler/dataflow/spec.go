package dataflow

// DataflowGraphSpec is a V3 planning view converted from dag_definition.
// It is intentionally lightweight for the first framework milestone.
type DataflowGraphSpec struct {
	AnalysisID int64
	Processes  []DataflowProcessSpec
	Channels   []DataflowChannelSpec
}

// DataflowProcessSpec represents a process template (not a persisted node instance).
type DataflowProcessSpec struct {
	NodeID       string
	NodeName     string
	SampleID     string
	ScriptID     string
	InputKeys    []string
	UpstreamIDs  []string
	Downstream   []string
	Inputs       map[string]any
	Outputs      map[string]any
	Params       map[string]any
	ResolvedIn   map[string]any
	ResolvedOut  map[string]any
	Executor     string
	Retry        int
	MaxRetry     int
	RerunReason  string
	OperatorType string
	ScatterField string
	ScatterMode  string
	GatherField  string
	GatherMode   string
}

// DataflowAnalysisNodePersistParams is a normalized payload that contains
// the fields needed to create an analysis_node record.
//
// It is assembled at the runtime submit boundary and will be consumed by
// a persistent runtime implementation in the next step.
type DataflowAnalysisNodePersistParams struct {
	AnalysisID      int64
	NodeID          string
	InputHash       string
	NodeName        string
	SampleID        string
	ScriptID        string
	InputsPatterns  map[string]any
	OutputPatterns  map[string]any
	Params          map[string]any
	ResolvedInputs  map[string]any
	ResolvedOutputs map[string]any
	UpstreamIDs     []string
	DownstreamIDs   []string
	Executor        string
	Retry           int
	MaxRetry        int
	RerunReason     string
	Status          string
	SubmitReason    string
	WorkspaceDir    string
	OutputDir       string
	CommandPath     string
	ParamsPath      string
	LogPath         string
	CacheDir        string
}

// DataflowChannelSpec represents a logical edge/channel in the dataflow graph.
type DataflowChannelSpec struct {
	ChannelID  string
	FromNodeID string
	ToNodeID   string
	FromPort   string
	ToPort     string
}

type dataflowChannelState string

type dataflowOperatorType string

const (
	dataflowChannelStateOpen   dataflowChannelState = "open"
	dataflowChannelStateClosed dataflowChannelState = "closed"

	dataflowOperatorTypeInput   dataflowOperatorType = "input"
	dataflowOperatorTypeGather  dataflowOperatorType = "gather"
	dataflowOperatorTypeScatter dataflowOperatorType = "scatter"
)
