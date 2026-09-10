package orchestratorv2

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/google/uuid"
)

// WorkspaceLayout is the on-disk contract of a single analysis node.
type WorkspaceLayout struct {
	// Dir is the node root directory.
	Dir string
	// OutputDir receives produced artifacts.
	OutputDir string
	// CacheDir holds intermediate cache artifacts.
	CacheDir string
	// ParamsPath is the generated params.json path.
	ParamsPath string
	// CommandPath is the generated run.sh path.
	CommandPath string
	// LogPath is the captured command log path.
	LogPath string
}

// newWorkspaceLayout allocates a fresh directory tree below the analysis
// output directory. The node primary key doubles as the directory name so the
// on-disk layout stays traceable from the database.
func newWorkspaceLayout(analysisOutputDir string, nodeID int64) WorkspaceLayout {
	dir := filepath.Join(analysisOutputDir, strconv.FormatInt(nodeID, 10))
	return WorkspaceLayout{
		Dir:         dir,
		OutputDir:   utils.GetAnalysisNodeOutputDir(dir),
		CacheDir:    utils.GetAnalysisNodeCacheDir(dir),
		ParamsPath:  filepath.Join(dir, "params.json"),
		CommandPath: filepath.Join(dir, "run.sh"),
		LogPath:     filepath.Join(dir, "command.log"),
	}
}

// NodeBuilder assembles analysis_node entities from compiled templates.
//
// It owns the whole "template -> persisted row" translation, including
// workspace allocation and upstream input bootstrapping, so the reconciler only
// has to decide *when* to materialize a node.
type NodeBuilder struct{}

// NewNodeBuilder creates a builder.
func NewNodeBuilder() *NodeBuilder { return &NodeBuilder{} }

// MaterializeRequest describes a node that does not exist yet for this run.
type MaterializeRequest struct {
	Analysis *types.Analysis
	Spec     *NodeSpec
	// ScriptID is the pipeline_components primary key resolved from
	// Spec.ScriptID (the compiler emitted component id). The runtime preparer
	// loads the script by primary key, so it must be carried over here.
	ScriptID     int64
	Status       string
	ErrorMessage string
	Incoming     []*EdgeSpec
	Upstream     map[string]*types.AnalysisNode
}

// ProbeRequest describes a re-evaluation of an already persisted node.
type ProbeRequest struct {
	Analysis *types.Analysis
	Spec     *NodeSpec
	Existing *types.AnalysisNode
	// ScriptID overrides the script primary key when it was resolved already.
	ScriptID int64
	Incoming []*EdgeSpec
	Upstream map[string]*types.AnalysisNode
}

// Materialize creates a brand new analysis node row with a dedicated workspace.
func (b *NodeBuilder) Materialize(req MaterializeRequest) (*types.AnalysisNode, error) {
	if req.Analysis == nil {
		return nil, fmt.Errorf("analysis is required")
	}
	if req.Spec == nil {
		return nil, fmt.Errorf("node spec is required")
	}

	nodeID := utils.GenerateID()
	layout := newWorkspaceLayout(req.Analysis.OutputDir, nodeID)

	params := cloneJSONMap(req.Spec.Params)
	resolvedInputs := cloneJSONMap(req.Spec.ResolvedInputs)
	mergeUpstreamInputs(req.Spec, params, resolvedInputs, req.Incoming, req.Upstream)

	if err := os.MkdirAll(layout.OutputDir, 0o755); err != nil {
		return nil, fmt.Errorf("create node output dir failed: %w", err)
	}

	var finishedAt *time.Time
	if req.Status == dagruntime.StatusSkipped {
		now := time.Now().UTC()
		finishedAt = &now
	}

	return &types.AnalysisNode{
		ID:                     nodeID,
		AnalysisNodeID:         fallbackString(strings.TrimSpace(req.Spec.AnalysisNodeID), "node-"+uuid.NewString()),
		AnalysisID:             req.Analysis.ID,
		ProjectID:              req.Analysis.ProjectID,
		NodeID:                 req.Spec.NodeID,
		NodeName:               req.Spec.NodeName,
		SampleID:               req.Spec.SampleID,
		ScriptID:               req.ScriptID,
		InputsPatterns:         cloneJSONMap(req.Spec.InputsPatterns),
		ResolvedInputs:         resolvedInputs,
		OutputPatterns:         cloneJSONMap(req.Spec.OutputPatterns),
		ResolvedOutputs:        cloneJSONMap(req.Spec.ResolvedOutputs),
		Params:                 params,
		Status:                 req.Status,
		Executor:               req.Spec.Executor,
		Retry:                  req.Spec.Retry,
		MaxRetry:               req.Spec.MaxRetry,
		CacheHit:               false,
		UpstreamIDs:            types.JSONSlice(stringSliceToAny(req.Spec.UpstreamIDs)),
		DownstreamIDs:          types.JSONSlice(stringSliceToAny(req.Spec.DownstreamIDs)),
		InputValidationErrors:  cloneJSONSlice(req.Spec.InputValidationErrors),
		OutputValidationErrors: types.JSONSlice{},
		LogPath:                layout.LogPath,
		WorkspaceDir:           layout.Dir,
		OutputDir:              layout.OutputDir,
		CacheDir:               layout.CacheDir,
		CommandPath:            layout.CommandPath,
		ParamsPath:             layout.ParamsPath,
		ErrorMessage:           req.ErrorMessage,
		RerunReason:            req.Spec.RerunReason,
		CreationSource:         creationSourceScheduler,
		FinishedAt:             finishedAt,
	}, nil
}

// Probe rewrites the volatile fields of an existing node with the freshly
// compiled template while keeping its identity and workspace. The result is
// only used for fingerprint comparison and rerun bookkeeping.
func (b *NodeBuilder) Probe(req ProbeRequest) *types.AnalysisNode {
	if req.Existing == nil || req.Spec == nil {
		return nil
	}

	probe := *req.Existing
	probe.NodeName = req.Spec.NodeName
	probe.SampleID = req.Spec.SampleID
	probe.Executor = req.Spec.Executor
	if req.ScriptID > 0 {
		probe.ScriptID = req.ScriptID
	}
	probe.InputsPatterns = cloneJSONMap(req.Spec.InputsPatterns)
	probe.OutputPatterns = cloneJSONMap(req.Spec.OutputPatterns)
	probe.Params = cloneJSONMap(req.Spec.Params)
	probe.ResolvedInputs = cloneJSONMap(req.Spec.ResolvedInputs)
	probe.ResolvedOutputs = cloneJSONMap(req.Spec.ResolvedOutputs)
	probe.InputValidationErrors = cloneJSONSlice(req.Spec.InputValidationErrors)

	mergeUpstreamInputs(req.Spec, probe.Params, probe.ResolvedInputs, req.Incoming, req.Upstream)
	return &probe
}

// creationSourceScheduler marks rows produced by this orchestrator.
const creationSourceScheduler = "scheduler"

// mergeUpstreamInputs injects the resolved outputs of successful upstream nodes
// into the downstream params / resolved_inputs payload.
//
// The runtime engine performs the same merge when an upstream completes, but a
// node materialized *after* that point (the normal dynamic-v2 order) is not yet
// persisted when the propagation happens, so the merge must be repeated here.
func mergeUpstreamInputs(
	spec *NodeSpec,
	params types.JSONMap,
	resolvedInputs types.JSONMap,
	incoming []*EdgeSpec,
	upstream map[string]*types.AnalysisNode,
) {
	if spec == nil || len(incoming) == 0 || len(upstream) == 0 {
		return
	}

	for _, edge := range incoming {
		if edge == nil || edge.TargetHandle == "" {
			continue
		}
		source := upstream[edge.SourceNode]
		if !isSuccessNode(source) {
			continue
		}
		value, ok := source.ResolvedOutputs[edge.SourceHandle]
		if !ok || value == nil {
			continue
		}

		pattern := asMap(spec.InputsPatterns[edge.TargetHandle])
		fanIn := toBool(pattern["multiple"]) ||
			isList(params[edge.TargetHandle]) ||
			isList(resolvedInputs[edge.TargetHandle])

		if fanIn {
			params[edge.TargetHandle] = appendToList(params[edge.TargetHandle], value)
			resolvedInputs[edge.TargetHandle] = appendToList(resolvedInputs[edge.TargetHandle], value)
			continue
		}
		params[edge.TargetHandle] = value
		resolvedInputs[edge.TargetHandle] = value
	}
}

// cloneJSONSlice copies a JSON slice so callers cannot mutate compiler output.
func cloneJSONSlice(src types.JSONSlice) types.JSONSlice {
	if len(src) == 0 {
		return types.JSONSlice{}
	}
	return append(types.JSONSlice(nil), src...)
}
