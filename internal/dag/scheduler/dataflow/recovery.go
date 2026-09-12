package dataflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

// GetRunningInfo implements interfaces.DagOrchestrator. The dataflow scheduler
// keeps no in-process running registry, so the caller falls back to the persisted
// analysis state for the runtime snapshot.
func (o *dataflowDagOrchestratorV3) GetRunningInfo(_ context.Context, _ int64) (*interfaces.DagRunningInfo, error) {
	return nil, nil
}

// StopAsync implements interfaces.DagOrchestrator. The dataflow run loop polls
// the persisted job_status, so writing "stopping" is enough to converge the run.
func (o *dataflowDagOrchestratorV3) StopAsync(ctx context.Context, analysisID int64) error {
	if analysisID <= 0 {
		return fmt.Errorf("analysis_id is required")
	}
	analysis, err := o.repo.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		return err
	}
	if analysis != nil && strings.EqualFold(strings.TrimSpace(analysis.JobStatus), types.AnalysisStatusStopped) {
		return nil
	}
	return o.repo.UpdateAnalysisByID(ctx, analysisID, map[string]any{
		"job_status": types.AnalysisStatusStopping,
		"updated_at": time.Now().UTC(),
	})
}

// RecoverRunningAnalyses implements interfaces.DagOrchestrator. It adopts a
// single analysis owned by the dataflow scheduler:
//   - stopping: persist the stop signal and let the loop converge.
//   - running:  rebuild the runtime inputs and restart with resume semantics; the
//     cross-instance lease guard keeps a live run from being doubled.
func (o *dataflowDagOrchestratorV3) RecoverRunningAnalyses(ctx context.Context, item *types.Analysis) (bool, error) {
	if o == nil || o.repo == nil || item == nil || item.ID <= 0 {
		return false, nil
	}
	if types.NormalizeSchedulerMode(item.SchedulerMode) != types.SchedulerModeDataflow {
		return false, nil
	}

	switch strings.ToLower(strings.TrimSpace(item.JobStatus)) {
	case types.AnalysisStatusStopping:
		if err := o.StopAsync(ctx, item.ID); err != nil {
			return false, fmt.Errorf("recover stopping analysis failed: %w", err)
		}
		return false, nil
	default:
		parseAnalysisResult, dagDefinition, err := o.loadRecoveryInputs(ctx, item)
		if err != nil {
			return false, err
		}
		return false, o.StartAsync(ctx, item.ID, parseAnalysisResult, dagDefinition)
	}
}

// loadRecoveryInputs rebuilds the two inputs a resumed dataflow run needs: the
// submitted params (written to analysis.ParamsPath at save time) and the workflow
// dag definition referenced by analysis.relation_id.
func (o *dataflowDagOrchestratorV3) loadRecoveryInputs(ctx context.Context, item *types.Analysis) (map[string]any, map[string]any, error) {
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
