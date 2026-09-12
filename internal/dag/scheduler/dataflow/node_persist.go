package dataflow

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/dag/nodebuild"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/utils"
)

func (r *persistentDataflowRuntime) SubmitProcessInstance(ctx context.Context, req DataflowProcessRunRequest) error {
	logger.Infof(ctx,
		"[DataflowDagOrchestratorV3] submit process instance, analysis_id=%d node_id=%s reason=%s inputs=%d",
		req.AnalysisID,
		req.NodeID,
		req.Reason,
		len(req.Inputs),
	)

	if r.repo != nil && r.buildPersistParams != nil {
		persistParams, ok := r.buildPersistParams(req)
		if ok {
			node, created, err := r.persistAnalysisNode(ctx, persistParams)
			if err != nil {
				return err
			}
			if !created {
				// The instance already has a persisted row, so it is not dispatched
				// again. Reconcile it so a cache reuse can still converge the run.
				return r.reconcileReusedInstance(ctx, req, node)
			}
			if node != nil {
				dispatch := r.dispatchFn
				if dispatch == nil {
					dispatch = r.dispatcher.Dispatch
				}
				if dispatch == nil {
					return nil
				}
				if err := r.markNodeSubmitted(ctx, node); err != nil {
					return err
				}
				if r.onNodeSubmitChange != nil {
					r.onNodeSubmitChange(strings.TrimSpace(node.NodeID), 1)
				}
				r.incrementInflight(node.ID)
				if err := dispatch(ctx, node.ID); err != nil {
					r.decrementInflight(node.ID)
					if r.onNodeSubmitChange != nil {
						r.onNodeSubmitChange(strings.TrimSpace(node.NodeID), -1)
					}
					if rollbackErr := r.rollbackSubmittedNodeOnDispatchError(ctx, node.ID); rollbackErr != nil {
						logger.Warnf(ctx,
							"[DataflowDagOrchestratorV3] rollback submitted node failed, analysis_id=%d node_id=%s analysis_node_id=%s err=%v",
							req.AnalysisID,
							req.NodeID,
							node.AnalysisNodeID,
							rollbackErr,
						)
					}
					return err
				}
			}
		}
	}
	return nil
}

// reconcileReusedInstance converges the kernel when a submitted instance already
// has a persisted row and therefore is not dispatched again.
//
// A successful row (done/cached) is treated as already submitted and completed:
// the instance is counted in and its persisted outputs are propagated downstream.
// This is what lets a reuse_existing_node rerun advance and terminate instead of
// waiting forever for a completion event that will never be published.
//
// A non-successful row keeps the previous no-op behavior: it is either a same-run
// duplicate of an instance still in flight, or a failed row, and completing it
// here would double-count and close channels prematurely.
func (r *persistentDataflowRuntime) reconcileReusedInstance(ctx context.Context, req DataflowProcessRunRequest, node *types.AnalysisNode) error {
	if r == nil || node == nil {
		return nil
	}
	nodeID := strings.TrimSpace(node.NodeID)
	if nodeID == "" {
		return nil
	}
	status := strings.ToLower(strings.TrimSpace(node.Status))
	if !dagruntime.IsSuccessStatus(status) {
		logger.Infof(ctx,
			"[DataflowDagOrchestratorV3] reused analysis node is not successful, keep as-is, analysis_id=%d node_id=%s status=%s",
			req.AnalysisID,
			nodeID,
			status,
		)
		return nil
	}
	if r.onNodeSubmitChange != nil {
		r.onNodeSubmitChange(nodeID, 1)
	}
	if r.onInstanceReused != nil {
		return r.onInstanceReused(ctx, node)
	}
	return nil
}

func (r *persistentDataflowRuntime) markNodeSubmitted(ctx context.Context, node *types.AnalysisNode) error {
	if r == nil || r.repo == nil || node == nil {
		return nil
	}
	status := strings.TrimSpace(strings.ToLower(node.Status))
	if status == "" {
		status = dagruntime.StatusReady
	}
	if err := dagruntime.EnsureTransition(status, dagruntime.StatusSubmitted); err != nil {
		return err
	}
	if err := r.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{"status": dagruntime.StatusSubmitted}); err != nil {
		return err
	}
	node.Status = dagruntime.StatusSubmitted
	return nil
}

func (r *persistentDataflowRuntime) rollbackSubmittedNodeOnDispatchError(ctx context.Context, analysisNodeID int64) error {
	if r == nil || r.repo == nil {
		return nil
	}
	node, err := r.repo.GetAnalysisNodeByID(ctx, analysisNodeID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(strings.ToLower(node.Status)) != dagruntime.StatusSubmitted {
		return nil
	}
	return r.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{"status": dagruntime.StatusReady})
}

func (r *persistentDataflowRuntime) persistAnalysisNode(ctx context.Context, payload *DataflowAnalysisNodePersistParams) (*types.AnalysisNode, bool, error) {
	if r.repo == nil || payload == nil {
		return nil, false, nil
	}
	if payload.AnalysisID <= 0 || strings.TrimSpace(payload.NodeID) == "" {
		return nil, false, nil
	}
	if payload.InputHash == "" {
		payload.InputHash = nodebuild.InstanceInputHash(payload.NodeID, types.JSONMap(payload.ResolvedInputs), types.JSONMap(payload.Params))
	}

	existingNodes, err := r.repo.ListAnalysisNodesByAnalysisID(ctx, payload.AnalysisID)
	if err != nil {
		return nil, false, err
	}
	// MatchInstance is the shared identity lookup: an exact instance hash wins, and a
	// legacy successful row with the same node id is still reusable. Because the hash
	// algorithm lives in nodebuild, this also matches nodes materialized by V2.
	existing := nodebuild.MatchInstance(existingNodes, payload.NodeID, payload.InputHash)
	if existing != nil {
		rerun, decided, err := r.decideExistingInstance(ctx, payload, existing)
		if err != nil {
			return nil, false, err
		}
		if decided == nil {
			decided = existing
		}
		if rerun {
			logger.Infof(ctx,
				"[DataflowDagOrchestratorV3] cache invalidated, rerun persisted analysis node, analysis_id=%d node_id=%s analysis_node_id=%s reason=%s",
				payload.AnalysisID,
				payload.NodeID,
				decided.AnalysisNodeID,
				decided.RerunReason,
			)
			return decided, true, nil
		}
		logger.Infof(ctx,
			"[DataflowDagOrchestratorV3] skip persist duplicated analysis node instance, analysis_id=%d node_id=%s input_hash=%s",
			payload.AnalysisID,
			payload.NodeID,
			payload.InputHash,
		)
		return decided, false, nil
	}
	scriptID := strings.TrimSpace(payload.ScriptID)
	script, err := r.workflowRepo.GetScriptByScriptID(ctx, r.projectID, scriptID)
	if err != nil {
		return nil, false, err
	}
	item, err := r.buildAnalysisNode(ctx, script, payload)
	if err != nil {
		return nil, false, err
	}
	if err := r.repo.CreateAnalysisNodes(ctx, []*types.AnalysisNode{item}); err != nil {
		return nil, false, err
	}

	logger.Infof(ctx,
		"[DataflowDagOrchestratorV3] persisted analysis node, analysis_id=%d node_id=%s analysis_node_id=%s",
		item.AnalysisID,
		item.NodeID,
		item.AnalysisNodeID,
	)
	return item, true, nil
}

func (r *persistentDataflowRuntime) populateNodePathDefaults(ctx context.Context, node *types.AnalysisNode) {
	if node == nil {
		return
	}

	baseWorkspace := strings.TrimSpace(node.WorkspaceDir)
	if baseWorkspace == "" {
		analysisOutputDir := r.lookupAnalysisOutputDir(ctx, node.AnalysisID)
		if analysisOutputDir != "" {
			baseWorkspace = filepath.Join(analysisOutputDir, fmt.Sprintf("%d", node.ID))
		}
	}

	if strings.TrimSpace(node.WorkspaceDir) == "" {
		node.WorkspaceDir = baseWorkspace
	}
	if strings.TrimSpace(node.OutputDir) == "" && baseWorkspace != "" {
		node.OutputDir = utils.GetAnalysisNodeOutputDir(baseWorkspace) //filepath.Join(baseWorkspace, "output")
	}
	if strings.TrimSpace(node.CacheDir) == "" && baseWorkspace != "" {
		node.CacheDir = utils.GetAnalysisNodeCacheDir(baseWorkspace) //filepath.Join(baseWorkspace, "cache")
	}
	if strings.TrimSpace(node.ParamsPath) == "" && baseWorkspace != "" {
		node.ParamsPath = filepath.Join(baseWorkspace, "params.json")
	}
	if strings.TrimSpace(node.CommandPath) == "" && baseWorkspace != "" {
		node.CommandPath = filepath.Join(baseWorkspace, "run.sh")
	}
	if strings.TrimSpace(node.LogPath) == "" && baseWorkspace != "" {
		node.LogPath = filepath.Join(baseWorkspace, "command.log")
	}
}

func (r *persistentDataflowRuntime) lookupAnalysisOutputDir(ctx context.Context, analysisID int64) string {
	if r == nil || r.repo == nil || analysisID <= 0 {
		return ""
	}
	analysis, err := r.repo.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		logger.Warnf(ctx,
			"[DataflowDagOrchestratorV3] lookup analysis output_dir failed, analysis_id=%d err=%v",
			analysisID,
			err,
		)
		return ""
	}
	if analysis == nil {
		return ""
	}
	return strings.TrimSpace(analysis.OutputDir)
}

func (r *persistentDataflowRuntime) incrementInflight(analysisNodeID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if analysisNodeID > 0 {
		if r.inflightNodeIDs == nil {
			r.inflightNodeIDs = make(map[int64]struct{})
		}
		if _, exists := r.inflightNodeIDs[analysisNodeID]; exists {
			return
		}
		r.inflightNodeIDs[analysisNodeID] = struct{}{}
	}
	r.inflightDispatches++
}

func (r *persistentDataflowRuntime) decrementInflight(analysisNodeID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if analysisNodeID > 0 {
		if _, exists := r.inflightNodeIDs[analysisNodeID]; !exists {
			return
		}
		delete(r.inflightNodeIDs, analysisNodeID)
	}
	if r.inflightDispatches > 0 {
		r.inflightDispatches--
	}
}

func (r *persistentDataflowRuntime) onRuntimeEvent(evt dagruntime.RuntimeEvent) {
	name := strings.TrimSpace(evt.Name)
	if name != dagruntime.EventNodeCompleted && name != dagruntime.EventNodeFailed {
		return
	}
	if evt.AnalysisNodeID <= 0 {
		return
	}
	r.decrementInflight(evt.AnalysisNodeID)
}

func (r *persistentDataflowRuntime) InflightDispatches() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inflightDispatches
}

// buildAnalysisNode translates a normalized persist payload into an
// analysis_node row through the shared nodebuild builder, so V3 nodes carry the
// same workspace layout and instance identity as V2 nodes.
func (r *persistentDataflowRuntime) buildAnalysisNode(ctx context.Context, script *types.Script, payload *DataflowAnalysisNodePersistParams) (*types.AnalysisNode, error) {
	analysis, err := r.repo.GetAnalysisByID(ctx, payload.AnalysisID)
	if err != nil {
		return nil, err
	}
	if analysis == nil {
		analysis = &types.Analysis{ID: payload.AnalysisID, ProjectID: r.projectID}
	}

	status := strings.ToLower(strings.TrimSpace(payload.Status))
	if status == "" {
		status = dagruntime.StatusReady
	}

	node, err := nodebuild.Materialize(nodebuild.MaterializeRequest{
		Analysis:       analysis,
		Spec:           buildNodeSpec(script, payload),
		Status:         status,
		WorkspaceDir:   strings.TrimSpace(payload.WorkspaceDir),
		CreationSource: nodebuild.CreationSourceScheduler,
	})
	if err != nil {
		return nil, err
	}
	// Fallback for analyses without a configured output directory; normally the
	// builder already derived every path.
	r.populateNodePathDefaults(ctx, node)
	return node, nil
}

// buildNodeSpec is the single translation from the V3 persist payload to the
// shared nodebuild spec, so the row the scheduler creates and the probe it
// fingerprints for cache decisions can never describe different artifacts.
func buildNodeSpec(script *types.Script, payload *DataflowAnalysisNodePersistParams) *nodebuild.Spec {
	if payload == nil {
		return nil
	}
	scriptID := int64(0)
	if script != nil {
		scriptID = script.ID
	}
	return &nodebuild.Spec{
		NodeID:          strings.TrimSpace(payload.NodeID),
		NodeName:        strings.TrimSpace(payload.NodeName),
		SampleID:        strings.TrimSpace(payload.SampleID),
		ScriptID:        scriptID,
		Executor:        strings.TrimSpace(payload.Executor),
		InputsPatterns:  nodebuild.ToJSONMap(payload.InputsPatterns),
		ResolvedInputs:  nodebuild.ToJSONMap(payload.ResolvedInputs),
		OutputPatterns:  nodebuild.ToJSONMap(payload.OutputPatterns),
		ResolvedOutputs: nodebuild.ToJSONMap(payload.ResolvedOutputs),
		Params:          nodebuild.ToJSONMap(payload.Params),
		UpstreamIDs:     payload.UpstreamIDs,
		DownstreamIDs:   payload.DownstreamIDs,
		Retry:           payload.Retry,
		MaxRetry:        payload.MaxRetry,
		RerunReason:     strings.TrimSpace(payload.RerunReason),
		// Keep the hash that was just used for deduplication so the persisted
		// identity and the dedup identity can never diverge.
		InputHash: strings.TrimSpace(payload.InputHash),
	}
}
