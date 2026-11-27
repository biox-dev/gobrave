package orchestratorv2

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
	Analysis *types.AnalysisWorkflow
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
	Analysis *types.AnalysisWorkflow
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

	resolvedInputs := cloneJSONMap(req.Spec.ResolvedInputs)
	mergeUpstreamInputs(req.Spec, resolvedInputs, req.Incoming, req.Upstream)

	var finishedAt *time.Time
	if req.Status == dagruntime.StatusSkipped {
		now := time.Now().UTC()
		finishedAt = &now
	}

	node := &types.AnalysisNode{
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
		Status:                 req.Status,
		Executor:               req.Spec.Executor,
		Retry:                  req.Spec.Retry,
		MaxRetry:               req.Spec.MaxRetry,
		CacheHit:               false,
		UpstreamIDs:            types.JSONSlice(stringSliceToAny(req.Spec.UpstreamIDs)),
		DownstreamIDs:          types.JSONSlice(stringSliceToAny(req.Spec.DownstreamIDs)),
		InputValidationErrors:  cloneJSONSlice(req.Spec.InputValidationErrors),
		OutputValidationErrors: types.JSONSlice{},
		ErrorMessage:           req.ErrorMessage,
		RerunReason:            req.Spec.RerunReason,
		CreationSource:         creationSourceScheduler,
		FinishedAt:             finishedAt,
	}
	// 节点目录由 base_dir + 项目 + 节点主键推导（见 types.AnalysisNode.Dir）。
	node.SetBaseDir(req.Analysis.BaseDir())
	if outDir := node.OutputDirectory(); outDir != "" {
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return nil, fmt.Errorf("create node output dir failed: %w", err)
		}
	}
	return node, nil
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
	probe.ResolvedInputs = cloneJSONMap(req.Spec.ResolvedInputs)
	probe.ResolvedOutputs = cloneJSONMap(req.Spec.ResolvedOutputs)
	probe.InputValidationErrors = cloneJSONSlice(req.Spec.InputValidationErrors)

	mergeUpstreamInputs(req.Spec, probe.ResolvedInputs, req.Incoming, req.Upstream)
	return &probe
}

// creationSourceScheduler marks rows produced by this orchestrator.
const creationSourceScheduler = types.AnalysisNodeCreationSourceScheduler

// mergeUpstreamInputs injects the resolved outputs of successful upstream nodes
// into the downstream resolved_inputs payload.
//
// The runtime engine performs the same merge when an upstream completes, but a
// node materialized *after* that point (the normal dynamic-v2 order) is not yet
// persisted when the propagation happens, so the merge must be repeated here.
func mergeUpstreamInputs(
	spec *NodeSpec,
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
		fanIn := toBool(pattern["multiple"]) || isList(resolvedInputs[edge.TargetHandle])

		if fanIn {
			resolvedInputs[edge.TargetHandle] = appendToList(resolvedInputs[edge.TargetHandle], value)
			continue
		}
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
