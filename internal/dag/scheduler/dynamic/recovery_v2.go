package dynamic

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
)

// RecoverRunningAnalyses adopts a single analysis owned by dynamic_v2.
//
// The container recovery loop fetches all running/stopping analyses and routes
// each one here (or to the legacy orchestrator) according to scheduler_mode, so
// this method no longer scans the tables itself:
//   - running:  a stale-lease run is restarted with resume semantics.
//   - stopping: a live run is cancelled, otherwise the analysis is finalized.
//
// It reports whether a live run is now tracked in this process.
func (o *dynamicDagOrchestratorV2) RecoverRunningAnalyses(ctx context.Context, item *types.Analysis) (bool, error) {
	if o == nil || o.repo == nil || !o.ownsAnalysis(item) {
		return false, nil
	}

	switch strings.ToLower(strings.TrimSpace(item.JobStatus)) {
	case types.AnalysisStatusStopping:
		if o.registry != nil && o.registry.RequestStop(item.ID) {
			// A live run in this process was cancelled; its run loop writes the
			// terminal status, so nothing else to do here.
			return false, nil
		}
		// No live owner to cancel: converge the analysis directly.
		o.finalizeStop(item.ID)
		return false, nil
	default:
		if o.registry != nil && o.registry.IsRunning(item.ID) {
			// Already alive in this process.
			return false, nil
		}
		if !o.isLeaseStale(item) {
			// Another instance is still heartbeating this run. Skip before touching
			// any node state: startAsyncV2 would refuse the lease anyway, and the
			// node rollback must not happen for a live run.
			return false, nil
		}
		if err := o.recoverRunningAnalysis(ctx, item); err != nil {
			return false, err
		}
		return o.registry != nil && o.registry.IsRunning(item.ID), nil
	}
}

// ownsAnalysis reports whether dynamic_v2 is the scheduler responsible for item.
func (o *dynamicDagOrchestratorV2) ownsAnalysis(item *types.Analysis) bool {
	if item == nil || item.ID <= 0 {
		return false
	}
	return types.NormalizeSchedulerMode(item.SchedulerMode) == types.SchedulerModeDynamic
}

// isLeaseStale reports whether the run's lease looks abandoned.
//
// A live owner refreshes updated_at on every heartbeat, so a stale timestamp means
// the owner is gone. This is only a cheap pre-filter: TryMarkAnalysisRunning inside
// startAsyncV2 remains the authoritative, race-free gate.
func (o *dynamicDagOrchestratorV2) isLeaseStale(item *types.Analysis) bool {
	if item == nil {
		return false
	}
	if item.UpdatedAt.IsZero() {
		return true
	}
	return time.Since(item.UpdatedAt) > dynamicV2LeaseTTL
}

// recoverRunningAnalysis rebuilds the runtime inputs and hands the analysis back to
// startAsyncV2 with resume semantics.
func (o *dynamicDagOrchestratorV2) recoverRunningAnalysis(ctx context.Context, item *types.Analysis) error {
	parseAnalysisResult, dagDefinition, err := o.loadRecoveryInputs(ctx, item)
	if err != nil {
		return err
	}
	return o.startAsyncV2(ctx, item.ID, parseAnalysisResult, dagDefinition, true)
}

// loadRecoveryInputs rebuilds the two inputs the dynamic scheduler needs.
//
// Neither is stored in a directly reusable column, but both are recoverable:
//   - parseAnalysisResult: SaveAnalysisController writes it to the analysis params
//     file (analysis.ParamsPath), so the exact submitted params are on disk.
//   - dagDefinition: re-read from the workflow referenced by analysis.relation_id,
//     the same call the submit handler makes.
func (o *dynamicDagOrchestratorV2) loadRecoveryInputs(ctx context.Context, item *types.Analysis) (map[string]any, map[string]any, error) {
	if o.workflowService == nil {
		return nil, nil, fmt.Errorf("workflow service is not configured, cannot rebuild dag definition")
	}

	paramsPath := strings.TrimSpace(item.ParamsPath)
	if paramsPath == "" {
		return nil, nil, fmt.Errorf("analysis params_path is empty, cannot rebuild runtime inputs")
	}
	raw, err := os.ReadFile(paramsPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read analysis params file %q failed: %w", paramsPath, err)
	}
	parseAnalysisResult := map[string]any{}
	if err := json.Unmarshal(raw, &parseAnalysisResult); err != nil {
		return nil, nil, fmt.Errorf("decode analysis params file %q failed: %w", paramsPath, err)
	}

	workflowID := strings.TrimSpace(item.WorkflowID)
	if workflowID == "" {
		return nil, nil, fmt.Errorf("analysis relation_id is empty, cannot rebuild dag definition")
	}
	dagDefinition, err := o.workflowService.GetWorkflowVisByWorkflowID(ctx, workflowID)
	if err != nil {
		return nil, nil, fmt.Errorf("rebuild dag definition for workflow_id=%s failed: %w", workflowID, err)
	}

	return parseAnalysisResult, dagDefinition, nil
}

// prepareNodesForResume rolls back in-flight nodes left behind by a dead process.
//
// Semantics match the legacy DagOrchestrator, backed by the shared
// dag.ResumeNodeStatusForRestart decision: submitted nodes always return to ready,
// while a running node is kept only when its container is still alive, in which case
// deferred reconciliation is expected to complete it.
func (o *dynamicDagOrchestratorV2) prepareNodesForResume(ctx context.Context, analysisID int64) error {
	nodes, err := o.repo.ListAnalysisNodesByAnalysisID(ctx, analysisID)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return nil
	}

	instanceByOwnerID, err := o.latestContainerInstanceByNode(ctx, nodes)
	if err != nil {
		// Resetting nodes without knowing container liveness could re-dispatch a node
		// that is still running, so recovery is aborted instead of guessed.
		return err
	}

	for _, node := range nodes {
		if node == nil || strings.TrimSpace(node.AnalysisNodeID) == "" {
			continue
		}
		targetStatus, shouldReset := dagruntime.ResumeNodeStatusForRestart(node.Status, instanceByOwnerID[int64(node.ID)] != nil)
		if !shouldReset {
			continue
		}
		if err := o.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{
			"status":        targetStatus,
			"started_at":    nil,
			"finished_at":   nil,
			"error_message": nil,
			"exit_code":     0,
		}); err != nil {
			return err
		}
	}
	return nil
}

// latestContainerInstanceByNode indexes the newest container instance per node.
// It returns an empty map when no container repository is wired (for example in
// tests), which makes every in-flight node eligible for rollback.
func (o *dynamicDagOrchestratorV2) latestContainerInstanceByNode(ctx context.Context, nodes []*types.AnalysisNode) (map[int64]*types.ContainerInstance, error) {
	result := map[int64]*types.ContainerInstance{}
	if o.containerRepo == nil {
		return result, nil
	}

	ownerIDs := make([]int64, 0, len(nodes))
	for _, node := range nodes {
		if node != nil && node.ID > 0 {
			ownerIDs = append(ownerIDs, int64(node.ID))
		}
	}
	if len(ownerIDs) == 0 {
		return result, nil
	}

	instances, err := o.containerRepo.ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx, types.ContainerOwnerDagNode, ownerIDs)
	if err != nil {
		return nil, fmt.Errorf("list dag node container instances failed: %w", err)
	}
	for _, inst := range instances {
		if inst == nil || inst.OwnerType != types.ContainerOwnerDagNode || inst.OwnerID <= 0 {
			continue
		}
		if existing := result[inst.OwnerID]; existing == nil || inst.ID > existing.ID {
			result[inst.OwnerID] = inst
		}
	}
	return result, nil
}

// finalizeStop converges an analysis to a terminal stopped state when this process
// has no live run to cancel. Like the legacy finalizeStop, leftover non-terminal
// nodes are terminalized before the analysis status is written.
func (o *dynamicDagOrchestratorV2) finalizeStop(analysisID int64) {
	ctx := context.Background()
	finalStatus := types.AnalysisStatusStopped
	if err := o.markActiveNodesStopped(ctx, analysisID, "dag stopped by user"); err != nil {
		finalStatus = types.AnalysisStatusFailed
		logger.Warnf(ctx, "[DynamicDagOrchestratorV2] mark nodes stopped failed, analysis_id=%d err=%v", analysisID, err)
	}
	if o.registry != nil {
		o.registry.MarkFinished(analysisID, finalStatus)
	}
	if err := o.repo.UpdateAnalysisByID(ctx, analysisID, map[string]any{
		"job_status": finalStatus,
		"updated_at": time.Now().UTC(),
	}); err != nil {
		logger.Warnf(ctx, "[DynamicDagOrchestratorV2] mark analysis stopped failed, analysis_id=%d err=%v", analysisID, err)
	}
}

// markActiveNodesStopped terminalizes every non-terminal node of the analysis.
func (o *dynamicDagOrchestratorV2) markActiveNodesStopped(ctx context.Context, analysisID int64, reason string) error {
	nodes, err := o.repo.ListAnalysisNodesByAnalysisID(ctx, analysisID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, node := range nodes {
		if node == nil || strings.TrimSpace(node.AnalysisNodeID) == "" {
			continue
		}
		if dagruntime.IsTerminalStatus(node.Status) {
			continue
		}
		if err := o.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{
			"status":        dagruntime.StatusStopped,
			"error_message": reason,
			"finished_at":   now,
		}); err != nil {
			return err
		}
	}
	return nil
}
