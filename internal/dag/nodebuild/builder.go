package nodebuild

import (
	"context"
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
// 取值统一引用 types 中的规范常量（analysis_nodes.creation_source）。
const CreationSourceScheduler = types.AnalysisNodeCreationSourceScheduler

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
	// RequestParam is the original analysis request_param carried onto the node so
	// a single node can be re-run or edited with the same request context.
	RequestParam types.JSONMap
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
	// Ctx carries the request context for the optional select-assay input parser.
	Ctx context.Context
	// Parser optionally produces the structured recipe for inputs whose declared
	// type needs the original request form (currently SelectAssay). nil skips it.
	Parser dagruntime.NodeRequestParser
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
	// Ctx carries the request context for the optional select-assay input parser.
	Ctx context.Context
	// Parser optionally produces the structured recipe for inputs whose declared
	// type needs the original request form (currently SelectAssay). nil skips it.
	Parser dagruntime.NodeRequestParser
}

// Materialize assembles a brand new analysis node row with a dedicated
// workspace.
//
// Upstream outputs are merged into resolved_inputs before the instance identity
// is computed, because the merged payload is what the node will actually execute
// with and therefore what must decide cache equality.
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
	layout := ResolveLayout(req.Analysis.WorkspaceDir, req.WorkspaceDir, nodeRecordID)

	resolvedInputs := cloneJSONMap(req.Spec.ResolvedInputs)
	MergeUpstreamInputs(req.Spec, resolvedInputs, req.Incoming, req.Upstream)
	requestParam := encodeRequestParam(req.Spec.RequestParam)
	resolvedInputs, requestParam = applyUpstreamBindings(req.Ctx, req.Parser, req.Analysis, req.Spec, req.Incoming, req.Upstream, resolvedInputs, requestParam)
	resolvedInputs, requestParam = applyRequestParamParse(req.Ctx, req.Parser, req.Analysis, req.Spec, resolvedInputs, requestParam)

	// Only create the tree when a workspace could be derived; a caller that
	// supplies paths later owns creating them (see v3's populateNodePathDefaults).
	if !layout.IsZero() {
		if err := os.MkdirAll(layout.OutputDir, 0o755); err != nil {
			return nil, fmt.Errorf("create node output dir failed: %w", err)
		}
	}

	inputHash := strings.TrimSpace(req.Spec.InputHash)
	if inputHash == "" {
		inputHash = InstanceInputHash(req.Spec.NodeID, resolvedInputs)
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
		RequestParam:           requestParam,
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
	probe.ResolvedInputs = cloneJSONMap(req.Spec.ResolvedInputs)
	probe.ResolvedOutputs = cloneJSONMap(req.Spec.ResolvedOutputs)
	// Only overwrite the persisted request_param when the freshly compiled spec
	// carries one; the dataflow scheduler leaves it empty and must not blank an
	// already stored value.
	if encoded := encodeRequestParam(req.Spec.RequestParam); encoded != "" {
		probe.RequestParam = encoded
	}
	probe.InputValidationErrors = cloneJSONSlice(req.Spec.InputValidationErrors)

	MergeUpstreamInputs(req.Spec, probe.ResolvedInputs, req.Incoming, req.Upstream)
	dagruntime.ApplySelectAssayBindings(req.Ctx, req.Parser, &probe, dagruntime.BuildUpstreamInputBindings(req.Incoming, req.Upstream))
	probe.ResolvedInputs, probe.RequestParam = applyRequestParamParse(req.Ctx, req.Parser, req.Analysis, req.Spec, probe.ResolvedInputs, probe.RequestParam)
	return &probe, nil
}

// MergeUpstreamInputs injects the resolved outputs of successful upstream nodes
// into the downstream resolved_inputs payload.
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
		fanIn := toBool(pattern["multiple"]) || isList(resolvedInputs[targetHandle])

		if fanIn {
			resolvedInputs[targetHandle] = appendToList(resolvedInputs[targetHandle], value)
			continue
		}
		resolvedInputs[targetHandle] = value
	}
}

// applyUpstreamBindings rewrites the inputs whose declared type needs the original
// request form (currently SelectAssay) using the upstream nodes' recorded output
// file ids, then returns the possibly rewritten resolved_inputs and request_param.
//
// It is the pull-side half of the input binding: MergeUpstreamInputs copies the
// upstream's raw resolved output into params/resolved_inputs, while this step turns
// a file output back into the `{"files": "<go_file id>"}` reference the script form
// expects and replaces resolved_inputs with the structured recipe the parser builds.
// It is a no-op when the caller has no graph snapshot (Incoming/Upstream empty), as
// is the case for schedulers that bind at dispatch time instead.
func applyUpstreamBindings(
	ctx context.Context,
	parser dagruntime.NodeRequestParser,
	analysis *types.Analysis,
	spec *Spec,
	incoming []*types.AnalysisEdge,
	upstream map[string]*types.AnalysisNode,
	resolvedInputs types.JSONMap,
	requestParam string,
) (types.JSONMap, string) {
	if spec == nil || len(incoming) == 0 || len(upstream) == 0 {
		return resolvedInputs, requestParam
	}
	target := &types.AnalysisNode{
		NodeID:         spec.NodeID,
		ScriptID:       spec.ScriptID,
		InputsPatterns: spec.InputsPatterns,
		ResolvedInputs: resolvedInputs,
		RequestParam:   requestParam,
	}
	if analysis != nil {
		target.AnalysisID = analysis.ID
	}
	bindings := dagruntime.BuildUpstreamInputBindings(incoming, upstream)
	if !dagruntime.ApplySelectAssayBindings(ctx, parser, target, bindings) {
		return resolvedInputs, requestParam
	}
	if target.ResolvedInputs == nil {
		target.ResolvedInputs = types.JSONMap{}
	}
	return target.ResolvedInputs, target.RequestParam
}

// applyRequestParamParse derives structured resolved_inputs from the node's own
// request_param, without requiring an upstream graph snapshot.
//
// It is the pull-side counterpart of the compiler writing raw request values into
// request_param - most notably a scatter(each) item's per-handle value, keyed by
// the input name. Once the raw value is on request_param, this step parses it
// through the injected parser (the same one select-assay binding uses) so the row
// persisted with the node carries the structured recipe rather than the raw form.
//
// The parse is skipped unless a declared input handle is provided by request_param
// and is still absent from resolved_inputs, so nodes whose inputs the compiler (or
// an upstream merge) already resolved never pay for the parser.
func applyRequestParamParse(
	ctx context.Context,
	parser dagruntime.NodeRequestParser,
	analysis *types.Analysis,
	spec *Spec,
	resolvedInputs types.JSONMap,
	requestParam string,
) (types.JSONMap, string) {
	if parser == nil || spec == nil || len(spec.InputsPatterns) == 0 {
		return resolvedInputs, requestParam
	}
	params := decodeRequestParam(requestParam)
	if len(params) == 0 {
		return resolvedInputs, requestParam
	}

	pending := false
	for handle := range spec.InputsPatterns {
		value, ok := params[handle]
		if !ok || value == nil {
			continue
		}
		if resolvedInputs[handle] == nil {
			pending = true
			break
		}
	}
	if !pending {
		return resolvedInputs, requestParam
	}

	target := &types.AnalysisNode{
		NodeID:         spec.NodeID,
		ScriptID:       spec.ScriptID,
		InputsPatterns: spec.InputsPatterns,
		ResolvedInputs: resolvedInputs,
		RequestParam:   requestParam,
	}
	if analysis != nil {
		target.AnalysisID = analysis.ID
	}
	if !dagruntime.ApplyRequestParamParse(ctx, parser, target) {
		return resolvedInputs, requestParam
	}
	if target.ResolvedInputs == nil {
		target.ResolvedInputs = types.JSONMap{}
	}
	return target.ResolvedInputs, target.RequestParam
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
