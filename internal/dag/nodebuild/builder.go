package nodebuild

import (
	"fmt"
	"os"
	"strings"
	"time"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
	"github.com/google/uuid"
)

// CreationSourceScheduler marks rows produced by a runtime scheduler.
//
// Both V2 and V3 stamp the same value so downstream tooling (and the legacy
// imported rows) can tell scheduler-generated nodes apart from other producers.
const CreationSourceScheduler = "scheduler"

// Spec is the scheduler-neutral description of one process instance to persist.
//
// It is the common denominator the V2 compiler rows and the V3 process specs are
// both translated into, so the actual row assembly happens exactly once. Fields
// that only one scheduler produces are simply left at their zero value.
type Spec struct {
	// AnalysisNodeID is the compiler-assigned, rerun-stable identity. Empty means
	// "generate one".
	AnalysisNodeID string
	// NodeID is the runtime identity used by edges.
	NodeID string
	// NodeName is the human readable instance name.
	NodeName string
	// SampleID identifies the scattered sample, empty for singletons.
	SampleID string
	// ScriptID is the resolved pipeline_components primary key.
	ScriptID int64
	// Executor selects the runtime backend (container, local, ...).
	Executor string
	// InputsPatterns declares input handles and their schemas.
	InputsPatterns types.JSONMap
	// ResolvedInputs holds the resolved input payload.
	ResolvedInputs types.JSONMap
	// OutputPatterns declares output handles and their schemas.
	OutputPatterns types.JSONMap
	// ResolvedOutputs holds the predicted outputs.
	ResolvedOutputs types.JSONMap
	// Params are the process parameters merged from workflow defaults.
	Params types.JSONMap
	// UpstreamIDs lists declared upstream node identities.
	UpstreamIDs []string
	// DownstreamIDs lists declared downstream node identities.
	DownstreamIDs []string
	// InputValidationErrors lists compile-time input problems.
	InputValidationErrors types.JSONSlice
	// Retry / MaxRetry configure in-place retries.
	Retry    int
	MaxRetry int
	// RerunReason carries a compiler supplied rerun explanation.
	RerunReason string
	// InputHash overrides the computed instance identity; empty computes it.
	InputHash string
}

// MaterializeRequest describes a node that does not exist yet for this run.
type MaterializeRequest struct {
	// Analysis is the owning analysis row; its output directory anchors the
	// workspace and its project id is copied onto the node.
	Analysis *types.Analysis
	// Spec is the compiled process template.
	Spec *Spec
	// Status is the initial node status.
	Status string
	// ErrorMessage explains a non-ready initial status (for example skipped).
	ErrorMessage string
	// CreationSource overrides the default creation_source marker.
	CreationSource string
	// Incoming are the edges that target this node, used to bootstrap inputs.
	Incoming []*types.AnalysisEdge
	// Upstream maps node id -> persisted node, used to resolve edge sources.
	Upstream map[string]*types.AnalysisNode
	// WorkspaceDir overrides the derived workspace root when a caller has
	// already allocated one.
	WorkspaceDir string
	// NodeRecordID overrides the generated primary key (tests only).
	NodeRecordID int64
}

// ProbeRequest describes a re-evaluation of an already persisted node.
type ProbeRequest struct {
	// Analysis is the owning analysis row.
	Analysis *types.Analysis
	// Spec is the freshly compiled template to compare against.
	Spec *Spec
	// Existing is the node persisted by a previous run.
	Existing *types.AnalysisNode
	// Incoming are the edges that target this node.
	Incoming []*types.AnalysisEdge
	// Upstream maps node id -> persisted node.
	Upstream map[string]*types.AnalysisNode
}

// Materialize assembles a brand new analysis node row with a dedicated
// workspace.
//
// Upstream outputs are merged into params / resolved_inputs before the instance
// identity is computed, because the merged payload is what the node will
// actually execute with and therefore what must decide cache equality.
func Materialize(req MaterializeRequest) (*types.AnalysisNode, error) {
	if req.Analysis == nil {
		return nil, fmt.Errorf("analysis is required")
	}
	if req.Spec == nil {
		return nil, fmt.Errorf("node spec is required")
	}
	if strings.TrimSpace(req.Spec.NodeID) == "" {
		return nil, fmt.Errorf("node spec node_id is required")
	}

	nodeRecordID := req.NodeRecordID
	if nodeRecordID == 0 {
		nodeRecordID = utils.GenerateID()
	}
	layout := ResolveLayout(req.Analysis.OutputDir, req.WorkspaceDir, nodeRecordID)

	params := cloneJSONMap(req.Spec.Params)
	resolvedInputs := cloneJSONMap(req.Spec.ResolvedInputs)
	MergeUpstreamInputs(req.Spec, params, resolvedInputs, req.Incoming, req.Upstream)

	// Only create the tree when a workspace could be derived; a caller that
	// supplies paths later owns creating them (see v3's populateNodePathDefaults).
	if !layout.IsZero() {
		if err := os.MkdirAll(layout.OutputDir, 0o755); err != nil {
			return nil, fmt.Errorf("create node output dir failed: %w", err)
		}
	}

	inputHash := strings.TrimSpace(req.Spec.InputHash)
	if inputHash == "" {
		inputHash = InstanceInputHash(req.Spec.NodeID, resolvedInputs, params)
	}

	var finishedAt *time.Time
	if strings.EqualFold(strings.TrimSpace(req.Status), dagruntime.StatusSkipped) {
		now := time.Now().UTC()
		finishedAt = &now
	}

	creationSource := strings.TrimSpace(req.CreationSource)
	if creationSource == "" {
		creationSource = CreationSourceScheduler
	}

	analysisNodeID := strings.TrimSpace(req.Spec.AnalysisNodeID)
	if analysisNodeID == "" {
		analysisNodeID = "node-" + uuid.NewString()
	}

	return &types.AnalysisNode{
		ID:                     nodeRecordID,
		AnalysisNodeID:         analysisNodeID,
		AnalysisID:             req.Analysis.ID,
		ProjectID:              req.Analysis.ProjectID,
		NodeID:                 strings.TrimSpace(req.Spec.NodeID),
		NodeName:               req.Spec.NodeName,
		SampleID:               req.Spec.SampleID,
		ScriptID:               req.Spec.ScriptID,
		InputsPatterns:         cloneJSONMap(req.Spec.InputsPatterns),
		ResolvedInputs:         resolvedInputs,
		OutputPatterns:         cloneJSONMap(req.Spec.OutputPatterns),
		ResolvedOutputs:        cloneJSONMap(req.Spec.ResolvedOutputs),
		Params:                 params,
		InputHash:              inputHash,
		Status:                 strings.TrimSpace(req.Status),
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
		CreationSource:         creationSource,
		FinishedAt:             finishedAt,
	}, nil
}

// Probe rewrites the volatile fields of an existing node with the freshly
// compiled template while keeping its identity and workspace.
//
// The returned copy is never persisted directly: it is fed to the fingerprint
// probe and the cache policy, and its fields are copied back only when the node
// is actually rerun.
func Probe(req ProbeRequest) (*types.AnalysisNode, error) {
	if req.Existing == nil {
		return nil, fmt.Errorf("existing node is required")
	}
	if req.Spec == nil {
		return nil, fmt.Errorf("node spec is required")
	}

	probe := *req.Existing
	probe.NodeName = req.Spec.NodeName
	probe.SampleID = req.Spec.SampleID
	probe.Executor = req.Spec.Executor
	if req.Spec.ScriptID > 0 {
		probe.ScriptID = req.Spec.ScriptID
	}
	probe.InputsPatterns = cloneJSONMap(req.Spec.InputsPatterns)
	probe.OutputPatterns = cloneJSONMap(req.Spec.OutputPatterns)
	probe.Params = cloneJSONMap(req.Spec.Params)
	probe.ResolvedInputs = cloneJSONMap(req.Spec.ResolvedInputs)
	probe.ResolvedOutputs = cloneJSONMap(req.Spec.ResolvedOutputs)
	probe.InputValidationErrors = cloneJSONSlice(req.Spec.InputValidationErrors)

	MergeUpstreamInputs(req.Spec, probe.Params, probe.ResolvedInputs, req.Incoming, req.Upstream)
	return &probe, nil
}

// MergeUpstreamInputs injects the resolved outputs of successful upstream nodes
// into the downstream params / resolved_inputs payload.
//
// The runtime engine performs the same merge when an upstream completes, but a
// node materialized *after* that point is not yet persisted when the propagation
// happens, so the merge must be repeated here.
//
// Fan-in is honoured: when the target handle is declared "multiple", or the
// current value is already list-like, produced values accumulate instead of
// overwriting each other.
func MergeUpstreamInputs(
	spec *Spec,
	params types.JSONMap,
	resolvedInputs types.JSONMap,
	incoming []*types.AnalysisEdge,
	upstream map[string]*types.AnalysisNode,
) {
	if spec == nil || len(incoming) == 0 || len(upstream) == 0 {
		return
	}

	for _, edge := range incoming {
		if edge == nil {
			continue
		}
		targetHandle := strings.TrimSpace(edge.TargetHandle)
		if targetHandle == "" {
			continue
		}
		source := upstream[strings.TrimSpace(edge.SourceNode)]
		if !IsSuccessNode(source) {
			continue
		}
		value, ok := source.ResolvedOutputs[edge.SourceHandle]
		if !ok || value == nil {
			continue
		}

		pattern := asMap(spec.InputsPatterns[targetHandle])
		fanIn := toBool(pattern["multiple"]) ||
			isList(params[targetHandle]) ||
			isList(resolvedInputs[targetHandle])

		if fanIn {
			params[targetHandle] = appendToList(params[targetHandle], value)
			resolvedInputs[targetHandle] = appendToList(resolvedInputs[targetHandle], value)
			continue
		}
		params[targetHandle] = value
		resolvedInputs[targetHandle] = value
	}
}

// IsSuccessNode reports whether a persisted node holds a reusable successful
// result.
//
// A node that is still "ready" but flagged as a cache hit represents a reused
// result from a previous run, so it counts as successful for input propagation.
func IsSuccessNode(node *types.AnalysisNode) bool {
	if node == nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(node.Status))
	if status == dagruntime.StatusReady && node.CacheHit {
		return true
	}
	return dagruntime.IsSuccessStatus(status)
}
