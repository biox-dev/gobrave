package orchestratorv2

import (
	"context"
	"fmt"
	"strings"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// ReconcilerDeps wires the reconciler with its collaborators.
type ReconcilerDeps struct {
	// Analyses is the persistence boundary for analysis nodes.
	Analyses interfaces.AnalysisRepository
	// Workflows resolves script metadata for cache fingerprints.
	Workflows interfaces.WorkflowRepository
	// Analysis is the run being scheduled.
	Analysis *types.Analysis
	// Graph is the compiled template graph.
	Graph *Graph
	// Tracker answers readiness questions.
	Tracker *DependencyTracker
	// Builder assembles analysis node rows.
	Builder *NodeBuilder
	// Fingerprinter computes run script / params digests for cache decisions.
	Fingerprinter Fingerprinter
	// Policies resolves cache strategies per analysis cache type.
	Policies *CachePolicyRegistry
	// Existing is the node set already persisted for this analysis.
	Existing map[string]*types.AnalysisNode
}

// Reconciler materializes compiled templates into analysis_node rows.
//
// It is the only component allowed to create nodes, and it is idempotent: a
// node whose scheduling decision has already been taken is remembered in the
// settled set and never fingerprinted or rewritten again during the same run.
type Reconciler struct {
	analyses      interfaces.AnalysisRepository
	workflows     interfaces.WorkflowRepository
	analysis      *types.Analysis
	graph         *Graph
	tracker       *DependencyTracker
	builder       *NodeBuilder
	fingerprinter Fingerprinter
	policies      *CachePolicyRegistry

	known        map[string]*types.AnalysisNode
	settled      map[string]struct{}
	materialized int
}

// NewReconciler builds a reconciler and seeds its view of persisted nodes.
func NewReconciler(deps ReconcilerDeps) *Reconciler {
	known := deps.Existing
	if known == nil {
		known = make(map[string]*types.AnalysisNode)
	}

	reconciler := &Reconciler{
		analyses:      deps.Analyses,
		workflows:     deps.Workflows,
		analysis:      deps.Analysis,
		graph:         deps.Graph,
		tracker:       deps.Tracker,
		builder:       deps.Builder,
		fingerprinter: deps.Fingerprinter,
		policies:      deps.Policies,
		known:         known,
		settled:       make(map[string]struct{}, len(known)),
	}

	for nodeID := range known {
		if _, ok := deps.Graph.Node(nodeID); ok {
			reconciler.materialized++
		}
	}
	return reconciler
}

// AllMaterialized reports whether every template has a persisted node.
func (r *Reconciler) AllMaterialized() bool {
	return r.graph != nil && r.materialized >= r.graph.Len()
}

// Known returns the in-run view of a node.
func (r *Reconciler) Known(nodeID string) (*types.AnalysisNode, bool) {
	node, ok := r.known[strings.TrimSpace(nodeID)]
	return node, ok
}

// Reopen clears the settled marker of skipped nodes so they are re-evaluated
// when an upstream that previously failed succeeds (after a retry or rerun).
func (r *Reconciler) Reopen(nodeIDs []string) {
	for _, nodeID := range nodeIDs {
		node, ok := r.known[strings.TrimSpace(nodeID)]
		if !ok || node == nil {
			continue
		}
		if normaliseStatus(node.Status) == dagruntime.StatusSkipped {
			delete(r.settled, node.NodeID)
		}
	}
}

// Reconcile materializes every candidate whose dependencies are satisfied, and
// marks blocked candidates as skipped so completion detection stays simple.
func (r *Reconciler) Reconcile(ctx context.Context, candidates []string) error {
	if len(candidates) == 0 || r.graph == nil {
		return nil
	}

	queue := make([]string, 0, len(candidates))
	queue = append(queue, candidates...)
	visited := make(map[string]struct{}, len(queue))
	pending := make([]*types.AnalysisNode, 0, len(queue))

	for len(queue) > 0 {
		nodeID := strings.TrimSpace(queue[0])
		queue = queue[1:]
		if nodeID == "" {
			continue
		}
		if _, seen := visited[nodeID]; seen {
			continue
		}
		visited[nodeID] = struct{}{}

		if _, done := r.settled[nodeID]; done {
			continue
		}
		spec, ok := r.graph.Node(nodeID)
		if !ok {
			continue
		}

		if existing, exists := r.known[nodeID]; exists && existing != nil {
			settle, err := r.reconcileExisting(ctx, existing, spec)
			if err != nil {
				return err
			}
			if settle {
				r.settled[nodeID] = struct{}{}
			}
			continue
		}

		if !r.tracker.IsReady(nodeID) && !r.tracker.IsBlocked(nodeID) {
			// Upstreams are still in flight: leave the node untouched.
			continue
		}

		node, err := r.materialize(ctx, spec)
		if err != nil {
			return err
		}
		if node == nil {
			continue
		}
		pending = append(pending, node)
		r.known[nodeID] = node
		r.materialized++
		r.settled[nodeID] = struct{}{}

		if normaliseStatus(node.Status) == dagruntime.StatusSkipped {
			// Cascade the skip immediately so the completion gate converges
			// even when no further runtime events are produced.
			queue = append(queue, r.tracker.OnFailure(nodeID)...)
		}
	}

	if len(pending) == 0 {
		return nil
	}
	if err := r.analyses.CreateAnalysisNodes(ctx, pending); err != nil {
		return fmt.Errorf("create analysis nodes failed: %w", err)
	}
	return nil
}

// materialize creates the node row for a ready or blocked template.
func (r *Reconciler) materialize(ctx context.Context, spec *NodeSpec) (*types.AnalysisNode, error) {
	status := dagruntime.StatusReady
	errorMessage := ""
	if r.tracker.IsBlocked(spec.NodeID) {
		status = dagruntime.StatusSkipped
		errorMessage = "blocked by failed upstream dependency"
	}

	scriptID, err := r.resolveScriptPK(ctx, spec)
	if err != nil {
		return nil, fmt.Errorf("resolve node script failed: node_id=%s: %w", spec.NodeID, err)
	}

	node, err := r.builder.Materialize(MaterializeRequest{
		Analysis:     r.analysis,
		Spec:         spec,
		ScriptID:     scriptID,
		Status:       status,
		ErrorMessage: errorMessage,
		Incoming:     r.graph.Incoming(spec.NodeID),
		Upstream:     r.known,
	})
	if err != nil {
		return nil, err
	}

	// Record the digests of the artifacts this node will run with so the next
	// run can decide whether its script or params changed.
	if r.fingerprinter != nil {
		if err := r.fingerprinter.Fingerprint(ctx, node); err != nil {
			return nil, err
		}
	}
	return node, nil
}

// reconcileExisting decides what to do with a node persisted by a previous run.
// It returns true when the node decision is final for this run.
func (r *Reconciler) reconcileExisting(ctx context.Context, existing *types.AnalysisNode, spec *NodeSpec) (bool, error) {
	nodeID := spec.NodeID
	if r.tracker.IsBlocked(nodeID) {
		return true, nil
	}
	if !r.tracker.IsReady(nodeID) {
		return false, nil
	}

	status := normaliseStatus(existing.Status)
	switch status {
	case dagruntime.StatusRunning, dagruntime.StatusSubmitted:
		// Someone else is already executing this node.
		return false, nil
	case dagruntime.StatusReady:
		if !existing.CacheHit {
			// Already queued for execution.
			return false, nil
		}
	}

	policy := r.policies.Resolve(r.analysis.CacheType)

	if !policy.RequiresFingerprint() {
		decision := policy.Decide(CacheFacts{CacheType: r.analysis.CacheType, Spec: spec, Existing: existing})
		if !decision.Rerun {
			return true, nil
		}
		return true, r.scheduleRerun(ctx, existing, spec, decision.Reason)
	}

	probe, err := r.buildProbe(ctx, spec, existing)
	if err != nil {
		return false, err
	}
	if err := r.fingerprinter.Fingerprint(ctx, probe); err != nil {
		return false, err
	}

	decision := policy.Decide(CacheFacts{
		CacheType:       r.analysis.CacheType,
		Spec:            spec,
		Existing:        existing,
		ProbeCommandMD5: probe.CommandMD5,
		ProbeParamsMD5:  probe.ParamsMD5,
	})
	if !decision.Rerun {
		return true, nil
	}
	return true, r.markReadyForRerun(ctx, existing, probe, decision.Reason)
}

// resolveScriptPK translates the compiler emitted component id (spec.ScriptID)
// into the pipeline_components primary key stored in analysis_nodes.script_id.
//
// The two are different keys: the DAG definition references scripts by their
// component id, while the runtime preparer and the container executor load the
// script by primary key. Skipping this translation leaves script_id=0 on the
// node and makes the preparer fail with "record not found".
func (r *Reconciler) resolveScriptPK(ctx context.Context, spec *NodeSpec) (int64, error) {
	if spec == nil {
		return 0, fmt.Errorf("node spec is required")
	}
	componentID := strings.TrimSpace(spec.ScriptID)
	if componentID == "" {
		return 0, fmt.Errorf("node template has no script_id")
	}
	if r.workflows == nil {
		return 0, fmt.Errorf("workflow repository is not configured")
	}

	script, err := r.workflows.GetScriptByScriptID(ctx, r.analysis.ProjectID, componentID)
	if err != nil {
		return 0, fmt.Errorf("load script failed: project_id=%d script_id=%s: %w", r.analysis.ProjectID, componentID, err)
	}
	if script == nil {
		return 0, fmt.Errorf("script %s is not registered in project %d", componentID, r.analysis.ProjectID)
	}
	return script.ID, nil
}

// buildProbe rebuilds the node payload from the freshly compiled template.
func (r *Reconciler) buildProbe(ctx context.Context, spec *NodeSpec, existing *types.AnalysisNode) (*types.AnalysisNode, error) {
	scriptID := existing.ScriptID
	if resolved, err := r.resolveScriptPK(ctx, spec); err == nil {
		scriptID = resolved
	}
	return r.builder.Probe(ProbeRequest{
		Analysis: r.analysis,
		Spec:     spec,
		Existing: existing,
		ScriptID: scriptID,
		Incoming: r.graph.Incoming(spec.NodeID),
		Upstream: r.known,
	}), nil
}

// scheduleRerun fingerprints the node with the current template and flips it
// back to ready.
func (r *Reconciler) scheduleRerun(ctx context.Context, existing *types.AnalysisNode, spec *NodeSpec, reason string) error {
	probe, err := r.buildProbe(ctx, spec, existing)
	if err != nil {
		return err
	}
	if r.fingerprinter != nil {
		if err := r.fingerprinter.Fingerprint(ctx, probe); err != nil {
			return err
		}
	}
	return r.markReadyForRerun(ctx, existing, probe, reason)
}

// markReadyForRerun resets a persisted node so the runtime can claim it again.
func (r *Reconciler) markReadyForRerun(ctx context.Context, existing *types.AnalysisNode, probe *types.AnalysisNode, reason string) error {
	if existing == nil || probe == nil {
		return nil
	}
	if !isValidNodeIdentity(existing.AnalysisNodeID) {
		return fmt.Errorf("analysis node identity is required")
	}

	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "node cache invalidated"
	}

	updates := map[string]any{
		"node_name":                probe.NodeName,
		"sample_id":                probe.SampleID,
		"script_id":                probe.ScriptID,
		"inputs_patterns":          probe.InputsPatterns,
		"resolved_inputs":          probe.ResolvedInputs,
		"output_patterns":          probe.OutputPatterns,
		"resolved_outputs":         types.JSONMap{},
		"params":                   probe.Params,
		"status":                   dagruntime.StatusReady,
		"executor":                 probe.Executor,
		"cache_hit":                false,
		"command_md5":              probe.CommandMD5,
		"params_md5":               probe.ParamsMD5,
		"rerun_reason":             reason,
		"error_message":            "",
		"exit_code":                0,
		"started_at":               nil,
		"finished_at":              nil,
		"output_validation_errors": types.JSONSlice{},
	}
	if err := r.analyses.UpdateAnalysisNodeByAnalysisNodeID(ctx, existing.AnalysisNodeID, updates); err != nil {
		return fmt.Errorf("reset analysis node for rerun failed: %w", err)
	}

	applyRerunToNode(existing, probe, reason)
	return nil
}

// ScheduleRetry moves a failed node back to ready and bumps its retry counter.
// It is the only mutation performed by the retry strategy path.
func (r *Reconciler) ScheduleRetry(ctx context.Context, node *types.AnalysisNode) error {
	if node == nil {
		return fmt.Errorf("analysis node is nil")
	}
	if !isValidNodeIdentity(node.AnalysisNodeID) {
		return fmt.Errorf("analysis node identity is required")
	}

	attempt := node.Retry + 1
	if err := r.analyses.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{
		"status":        dagruntime.StatusReady,
		"retry":         attempt,
		"cache_hit":     false,
		"error_message": "",
		"exit_code":     0,
		"started_at":    nil,
		"finished_at":   nil,
		"rerun_reason":  retryReason(attempt, node.MaxRetry),
	}); err != nil {
		return fmt.Errorf("schedule node retry failed: %w", err)
	}

	node.Retry = attempt
	node.Status = dagruntime.StatusReady
	node.CacheHit = false
	node.ErrorMessage = ""
	node.StartedAt = nil
	node.FinishedAt = nil
	node.RerunReason = retryReason(attempt, node.MaxRetry)

	delete(r.settled, node.NodeID)
	return nil
}

// applyRerunToNode mirrors a successful rerun reset into the in-memory view so
// the rest of the run observes the same state as the database.
func applyRerunToNode(existing *types.AnalysisNode, probe *types.AnalysisNode, reason string) {
	existing.NodeName = probe.NodeName
	existing.SampleID = probe.SampleID
	existing.ScriptID = probe.ScriptID
	existing.InputsPatterns = probe.InputsPatterns
	existing.ResolvedInputs = probe.ResolvedInputs
	existing.OutputPatterns = probe.OutputPatterns
	existing.ResolvedOutputs = types.JSONMap{}
	existing.Params = probe.Params
	existing.Status = dagruntime.StatusReady
	existing.Executor = probe.Executor
	existing.CacheHit = false
	existing.CommandMD5 = probe.CommandMD5
	existing.ParamsMD5 = probe.ParamsMD5
	existing.RerunReason = reason
	existing.ErrorMessage = ""
	existing.ExitCode = 0
	existing.StartedAt = nil
	existing.FinishedAt = nil
	existing.OutputValidationErrors = types.JSONSlice{}
}
