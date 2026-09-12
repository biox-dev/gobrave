package dataflow

import (
	"context"
	"strings"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/dag/nodebuild"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
)

// decideExistingInstance applies the shared cache policy to a persisted instance
// matched by identity.
//
// It turns V3 reuse from "same input hash therefore always reuse" into a
// fingerprint-aware decision: with cache types 3/4 a node whose generated
// run.sh / params.json changed is reset to ready and dispatched again instead of
// silently serving a stale result. Policies that do not compare fingerprints
// (reuse_existing / rerun_all) decide from the persisted row alone.
//
// The returned bool reports whether the instance must be dispatched (rerun); the
// returned node is always the persisted row to use.
func (r *persistentDataflowRuntime) decideExistingInstance(ctx context.Context, payload *DataflowAnalysisNodePersistParams, existing *types.AnalysisNode) (bool, *types.AnalysisNode, error) {
	if r == nil || existing == nil || payload == nil {
		return false, existing, nil
	}
	// A node still owned by an in-flight dispatch must never be reset: the submit is
	// a duplicate of an instance this run is already executing.
	if isDataflowInFlightStatus(existing.Status) {
		return false, existing, nil
	}

	analysis, err := r.repo.GetAnalysisByID(ctx, payload.AnalysisID)
	if err != nil {
		return false, existing, err
	}
	cacheType := 0
	if analysis != nil {
		cacheType = analysis.CacheType
	}

	policy := r.cachePolicies.Resolve(cacheType)
	if !policy.RequiresFingerprint() {
		decision := policy.Decide(dagruntime.CacheFacts{CacheType: cacheType, Existing: existing})
		if !decision.Rerun {
			return false, existing, nil
		}
		return true, existing, r.resetExistingNodeForRerun(ctx, existing, existing, decision.Reason)
	}

	probe, err := r.buildReuseProbe(ctx, payload, existing)
	if err != nil {
		return false, existing, err
	}
	if probe == nil {
		return false, existing, nil
	}
	if r.fingerprinter == nil {
		// Without a fingerprinter the fingerprints cannot be trusted; fall back to
		// reuse rather than rerunning on an unverifiable decision.
		logger.Warnf(ctx,
			"[DataflowDagOrchestratorV3] cache policy %s needs fingerprints but no fingerprinter is configured, keep persisted node, analysis_id=%d node_id=%s",
			policy.Name(),
			payload.AnalysisID,
			payload.NodeID,
		)
		return false, existing, nil
	}
	if err := r.fingerprinter.Fingerprint(ctx, probe); err != nil {
		return false, existing, err
	}

	decision := policy.Decide(dagruntime.CacheFacts{
		CacheType:       cacheType,
		Existing:        existing,
		ProbeCommandMD5: probe.CommandMD5,
		ProbeParamsMD5:  probe.ParamsMD5,
	})
	if !decision.Rerun {
		return false, existing, nil
	}
	return true, existing, r.resetExistingNodeForRerun(ctx, existing, probe, decision.Reason)
}

// buildReuseProbe rebuilds the node payload from the current process definition so
// the fingerprint policies can compare what would run now against what is
// persisted.
func (r *persistentDataflowRuntime) buildReuseProbe(ctx context.Context, payload *DataflowAnalysisNodePersistParams, existing *types.AnalysisNode) (*types.AnalysisNode, error) {
	if existing == nil || payload == nil {
		return nil, nil
	}
	script, err := r.workflowRepo.GetScriptByScriptID(ctx, r.projectID, strings.TrimSpace(payload.ScriptID))
	if err != nil {
		return nil, err
	}
	analysis, err := r.repo.GetAnalysisByID(ctx, payload.AnalysisID)
	if err != nil {
		return nil, err
	}
	// Probe keeps the persisted identity and workspace while rewriting the volatile
	// fields from the freshly compiled template, so the fingerprint describes the
	// artifacts a rerun would actually produce.
	probe, err := nodebuild.Probe(nodebuild.ProbeRequest{
		Analysis: analysis,
		Spec:     buildNodeSpec(script, payload),
		Existing: existing,
	})
	if err != nil {
		// Probing must never fail the run: fall back to the persisted node so the
		// policy decides from the stored row alone.
		logger.Warnf(ctx,
			"[DataflowDagOrchestratorV3] build reuse probe failed, fall back to persisted node, analysis_id=%d node_id=%s err=%v",
			payload.AnalysisID,
			payload.NodeID,
			err,
		)
		return existing, nil
	}
	probe.Status = dagruntime.StatusReady
	r.populateNodePathDefaults(ctx, probe)
	return probe, nil
}

// resetExistingNodeForRerun rewrites a persisted node with the probed artifacts and
// puts it back to ready so the runtime can claim and dispatch it again. It mirrors
// the reset onto the in-memory row so the caller dispatches the refreshed definition.
func (r *persistentDataflowRuntime) resetExistingNodeForRerun(ctx context.Context, existing, probe *types.AnalysisNode, reason string) error {
	if existing == nil || probe == nil {
		return nil
	}
	if strings.TrimSpace(existing.AnalysisNodeID) == "" {
		return nil
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "node cache invalidated"
	}
	if err := r.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, existing.AnalysisNodeID, map[string]any{
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
	}); err != nil {
		return err
	}

	existing.NodeName = probe.NodeName
	existing.SampleID = probe.SampleID
	existing.ScriptID = probe.ScriptID
	existing.InputsPatterns = probe.InputsPatterns
	existing.ResolvedInputs = probe.ResolvedInputs
	existing.OutputPatterns = probe.OutputPatterns
	existing.ResolvedOutputs = types.JSONMap{}
	existing.Params = probe.Params
	existing.Executor = probe.Executor
	existing.CommandMD5 = probe.CommandMD5
	existing.ParamsMD5 = probe.ParamsMD5
	existing.RerunReason = reason
	existing.ErrorMessage = ""
	existing.ExitCode = 0
	existing.StartedAt = nil
	existing.FinishedAt = nil
	existing.OutputValidationErrors = types.JSONSlice{}
	existing.CacheHit = false
	existing.Status = dagruntime.StatusReady
	return nil
}

// isDataflowInFlightStatus reports whether a persisted node is currently owned by
// an active dispatch and therefore must not be reset for a rerun.
func isDataflowInFlightStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case dagruntime.StatusSubmitted, dagruntime.StatusRunning, dagruntime.StatusStopping:
		return true
	default:
		return false
	}
}
