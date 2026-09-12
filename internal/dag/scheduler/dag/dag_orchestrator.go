package dag

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/biox-dev/gobrave/internal/config"
	dagruntime "github.com/biox-dev/gobrave/internal/dag"
	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/manager"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
)

const (
	cleanupPolicyNone   = "none"
	cleanupPolicyStop   = "stop"
	cleanupPolicyDelete = "delete"
	analysisRunLeaseTTL = 90 * time.Second
	// analysisRunHeartbeat is the interval for lease renewal while a run is active.
	analysisRunHeartbeat = 15 * time.Second
)

type dagOrchestrator struct {
	repo          interfaces.AnalysisRepository
	workflowRepo  interfaces.WorkflowRepository
	projectRepo   interfaces.ProjectRepository
	containerRepo interfaces.ContainerRepository
	containerMgr  *manager.ContainerManager
	// runScriptBuilders map[string]prepare.RunScriptBuilder
	dispatcher *dagruntime.NodeDispatcher
	cfg        *config.Config
	bus        event.Bus
	// registry is the process-wide running registry injected by the container and
	// shared with the other schedulers, so duplicate-run and stop lookups agree
	// across schedulers.
	registry *dagruntime.RunningRegistry
}

func NewDagOrchestrator(
	repo interfaces.AnalysisRepository,
	workflowRepo interfaces.WorkflowRepository,
	projectRepo interfaces.ProjectRepository,
	containerRepo interfaces.ContainerRepository,
	dispatcher *dagruntime.NodeDispatcher,
	containerMgr *manager.ContainerManager,
	// runScriptBuilders map[string]prepare.RunScriptBuilder,
	cfg *config.Config,
	bus event.Bus,
	registry *dagruntime.RunningRegistry,
) interfaces.DagOrchestrator {
	o := &dagOrchestrator{
		repo:          repo,
		workflowRepo:  workflowRepo,
		containerRepo: containerRepo,
		projectRepo:   projectRepo,
		containerMgr:  containerMgr,
		dispatcher:    dispatcher,
		// runScriptBuilders: runScriptBuilders,
		cfg:      cfg,
		bus:      bus,
		registry: registry,
	}
	return o
}

// Name implements interfaces.DagOrchestrator. It is the persisted
// analysis.scheduler_mode value and the registry key for this scheduler.
func (o *dagOrchestrator) Name() string { return types.SchedulerModeDag }

// PersistsGraphOnSave implements interfaces.DagOrchestrator: the static graph
// scheduler needs analysis_nodes/analysis_edges persisted at save time.
func (o *dagOrchestrator) PersistsGraphOnSave() bool { return true }

// GetRunningInfo implements interfaces.DagOrchestrator.
func (o *dagOrchestrator) GetRunningInfo(_ context.Context, analysisID int64) (*interfaces.DagRunningInfo, error) {
	if analysisID <= 0 || o.registry == nil {
		return nil, nil
	}
	entry := o.registry.Get(analysisID)
	if entry == nil {
		return nil, nil
	}
	return &interfaces.DagRunningInfo{
		AnalysisID:     entry.AnalysisID,
		TaskName:       entry.TaskName,
		Status:         entry.Status,
		StartedAt:      entry.StartedAt,
		UpdatedAt:      entry.UpdatedAt,
		MaxConcurrency: entry.MaxConcurrency,
		QueueSize:      entry.QueueSize,
		PollIntervalMs: entry.PollIntervalMs,
		TimeoutSeconds: entry.TimeoutSeconds,
		StopRequested:  entry.StopRequested,
	}, nil
}

// StartAsync implements interfaces.DagOrchestrator. The static graph scheduler
// works from the graph persisted at save time, so the request payload is unused.
func (o *dagOrchestrator) StartAsync(ctx context.Context, analysisID int64, _ map[string]any, _ map[string]any) error {
	if analysisID == 0 {
		return fmt.Errorf("analysis_id is required")
	}
	if o.registry.IsRunning(analysisID) {
		return nil
	}
	now := time.Now().UTC()
	locked, err := o.repo.TryMarkAnalysisRunning(ctx, analysisID, now, now.Add(-analysisRunLeaseTTL))
	if err != nil {
		return err
	}
	if !locked {
		return nil
	}

	if err := o.cleanupDagNodeContainersBeforeStart(ctx, analysisID); err != nil {
		_ = o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
			"job_status": types.AnalysisStatusFailed,
			"updated_at": time.Now().UTC(),
		})
		return fmt.Errorf("cleanup dag node containers before start failed: %w", err)
	}

	if err := o.prepareNodesForResume(ctx, analysisID); err != nil {
		_ = o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
			"job_status": types.AnalysisStatusFailed,
			"updated_at": time.Now().UTC(),
		})
		return fmt.Errorf("prepare nodes for resume failed: %w", err)
	}

	heartbeatStop := make(chan struct{})
	go o.renewAnalysisRunningLease(analysisID, heartbeatStop)

	runtime := dagruntime.NewRuntimeEngine(o.repo)
	// onNodeFailedCleanupPolicy := o.cleanupPolicyOnNodeFailed()
	onDagFinishedCleanupPolicy := o.cleanupPolicyOnDagFinished()
	// storageBase := ""
	// if o.cfg != nil && o.cfg.Storage != nil {
	// 	storageBase = strings.TrimSpace(o.cfg.Storage.BaseDir)
	// }
	// preparer := dagruntime.NewFileSystemNodeRuntimePreparerWithBuilders(o.repo, o.workflowRepo, o.projectRepo, storageBase, o.runScriptBuilders)
	// dispatcher := dagruntime.NewNodeDispatcher(runtime, o.repo, o.bus, executor.NewFactory(executor.FactoryDeps{
	// 	WorkflowRepository: o.workflowRepo,
	// 	ContainerManager:   o.containerMgr,
	// }), func(cleanupCtx context.Context, node *types.AnalysisNode) {
	// 	o.cleanupDagNodeContainer(cleanupCtx, node, onNodeFailedCleanupPolicy)
	// }, preparer)
	scheduler := dagruntime.NewDagScheduler(
		analysisID,
		runtime,
		o.dispatcher,
		o.bus,
		dagruntime.SchedulerConfig{
			MaxSteps:       10000,
			MaxConcurrency: 1,
			QueueSize:      64,
			PollInterval:   500 * time.Millisecond,
		},
	)
	runCtx, runCancel := context.WithCancel(context.Background())

	o.registry.Register(&dagruntime.RunningEntry{
		AnalysisID:     analysisID,
		TaskName:       "dag-run-" + fmt.Sprintf("%d", analysisID),
		MaxConcurrency: 1,
		QueueSize:      64,
		PollIntervalMs: 500,
		Status:         types.AnalysisStatusRunning,
		Cancel:         runCancel,
	})

	go func() {
		defer close(heartbeatStop)
		result, err := scheduler.Run(runCtx)
		stoppedByUser := o.registry != nil && o.registry.IsStopping(analysisID)

		if err != nil {
			finalStatus := types.AnalysisStatusFailed
			if stoppedByUser {
				_ = o.markActiveNodesStopped(context.Background(), analysisID, "dag stopped by user")
				if cleanupErr := o.cleanupByAnalysisIDStrict(context.Background(), analysisID, cleanupPolicyDelete); cleanupErr == nil {
					finalStatus = types.AnalysisStatusStopped
				} else {
					logger.Warnf(context.Background(), "[DagOrchestrator] stop cleanup failed after scheduler error, analysis_id=%d err=%v", analysisID, cleanupErr)
				}
			} else {
				o.cleanupByAnalysisID(context.Background(), analysisID, onDagFinishedCleanupPolicy)
			}

			o.registry.MarkFinished(analysisID, finalStatus)
			_ = o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
				"job_status": finalStatus,
				"updated_at": time.Now().UTC(),
			})
			return
		}

		finalStatus := types.AnalysisStatusFinished
		if stoppedByUser {
			_ = o.markActiveNodesStopped(context.Background(), analysisID, "dag stopped by user")
			if cleanupErr := o.cleanupByAnalysisIDStrict(context.Background(), analysisID, cleanupPolicyDelete); cleanupErr != nil {
				finalStatus = types.AnalysisStatusFailed
				logger.Warnf(context.Background(), "[DagOrchestrator] stop cleanup failed, analysis_id=%d err=%v", analysisID, cleanupErr)
			} else {
				finalStatus = types.AnalysisStatusStopped
			}
		} else {
			if result == nil || result.Snapshot == nil {
				finalStatus = types.AnalysisStatusFailed
			} else if failedCount := result.Snapshot.StatusCount[dagruntime.StatusFailed]; failedCount > 0 {
				finalStatus = types.AnalysisStatusFailed
			}
			if finalStatus == types.AnalysisStatusFinished {
				o.cleanupByAnalysisID(context.Background(), analysisID, onDagFinishedCleanupPolicy)
			}
		}

		o.registry.MarkFinished(analysisID, finalStatus)
		_ = o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
			"job_status": finalStatus,
			"updated_at": time.Now().UTC(),
		})
	}()

	return nil
}

func (o *dagOrchestrator) cleanupDagNodeContainersBeforeStart(ctx context.Context, analysisID int64) error {
	if o == nil || analysisID <= 0 {
		return nil
	}
	if !o.cleanupDagNodeContainersBeforeStartEnabled() {
		return nil
	}
	return o.cleanupByAnalysisIDStrict(ctx, analysisID, cleanupPolicyDelete)
}

func (o *dagOrchestrator) cleanupDagNodeContainersBeforeStartEnabled() bool {
	if o == nil || o.cfg == nil || o.cfg.Container == nil {
		return true
	}
	return o.cfg.Container.CleanupDagNodeContainersBeforeStart
}

func (o *dagOrchestrator) StopAsync(ctx context.Context, analysisID int64) error {
	if analysisID <= 0 {
		return fmt.Errorf("analysis_id is required")
	}

	analysis, err := o.repo.GetAnalysisByID(ctx, analysisID)
	if err != nil {
		return err
	}

	current := strings.TrimSpace(strings.ToLower(analysis.JobStatus))
	if current == types.AnalysisStatusStopped {
		return nil
	}

	if err := o.repo.UpdateAnalysisByID(ctx, analysisID, map[string]any{
		"job_status": types.AnalysisStatusStopping,
		"updated_at": time.Now().UTC(),
	}); err != nil {
		return err
	}

	if o.registry != nil && o.registry.RequestStop(analysisID) {
		return nil
	}

	go o.finalizeStop(analysisID)
	return nil
}

// RecoverRunningAnalyses adopts a single analysis owned by the legacy scheduler.
//
// The container recovery loop fetches all running/stopping analyses and routes
// each one here (or to the dynamic V2 orchestrator) according to scheduler_mode,
// so this method no longer scans the tables itself:
//   - running:  restart the run so it can continue after a crash.
//   - stopping: converge the stop request, cancelling a live run or finalizing.
//
// It reports whether a live run is now tracked in this process.
func (o *dagOrchestrator) RecoverRunningAnalyses(ctx context.Context, item *types.Analysis) (bool, error) {
	if o == nil || o.repo == nil || item == nil || item.ID <= 0 {
		return false, nil
	}

	switch strings.ToLower(strings.TrimSpace(item.JobStatus)) {
	case types.AnalysisStatusStopping:
		// StopAsync persists "stopping" and either cancels the live run or, when no
		// process owns it, converges the analysis to a terminal stopped state.
		if err := o.StopAsync(ctx, item.ID); err != nil {
			return false, fmt.Errorf("recover stopping analysis failed: %w", err)
		}
		return false, nil
	default:
		wasRunning := o.registry != nil && o.registry.IsRunning(item.ID)
		if err := o.StartAsync(ctx, item.ID, nil, nil); err != nil {
			return false, fmt.Errorf("recover running analysis failed: %w", err)
		}
		return !wasRunning && o.registry != nil && o.registry.IsRunning(item.ID), nil
	}
}

func (o *dagOrchestrator) finalizeStop(analysisID int64) {
	ctx := context.Background()
	finalStatus := types.AnalysisStatusStopped
	if err := o.markActiveNodesStopped(ctx, analysisID, "dag stopped by user"); err != nil {
		finalStatus = types.AnalysisStatusFailed
		logger.Warnf(ctx, "[DagOrchestrator] mark nodes stopped failed, analysis_id=%d err=%v", analysisID, err)
	}
	if err := o.cleanupByAnalysisIDStrict(ctx, analysisID, cleanupPolicyDelete); err != nil {
		finalStatus = types.AnalysisStatusFailed
		logger.Warnf(ctx, "[DagOrchestrator] stop cleanup failed, analysis_id=%d err=%v", analysisID, err)
	}
	if o.registry != nil {
		o.registry.MarkFinished(analysisID, finalStatus)
	}
	if err := o.repo.UpdateAnalysisByID(ctx, analysisID, map[string]any{
		"job_status": finalStatus,
		"updated_at": time.Now().UTC(),
	}); err != nil {
		logger.Warnf(ctx, "[DagOrchestrator] mark analysis stopped failed, analysis_id=%d err=%v", analysisID, err)
	}
}

func (o *dagOrchestrator) markActiveNodesStopped(ctx context.Context, analysisID int64, reason string) error {
	nodes, err := o.repo.ListAnalysisNodesByAnalysisID(ctx, analysisID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, node := range nodes {
		if node == nil || strings.TrimSpace(node.AnalysisNodeID) == "" {
			continue
		}
		status := strings.TrimSpace(strings.ToLower(node.Status))
		if dagruntime.IsTerminalStatus(status) {
			continue
		}
		if err := o.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{
			"status":        dagruntime.StatusFailed,
			"error_message": reason,
			"finished_at":   now,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (o *dagOrchestrator) prepareNodesForResume(ctx context.Context, analysisID int64) error {
	if o == nil || o.repo == nil || analysisID <= 0 {
		return nil
	}

	nodes, err := o.repo.ListAnalysisNodesByAnalysisID(ctx, analysisID)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return nil
	}

	instancesByOwner := map[int64]*types.ContainerInstance{}
	if o.containerRepo != nil {
		ownerIDs := make([]int64, 0, len(nodes))
		for _, node := range nodes {
			if node != nil && node.ID > 0 {
				ownerIDs = append(ownerIDs, int64(node.ID))
			}
		}
		instances, listErr := o.containerRepo.ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx, types.ContainerOwnerDagNode, ownerIDs)
		if listErr != nil {
			return listErr
		}
		for _, inst := range instances {
			if inst == nil || inst.OwnerType != types.ContainerOwnerDagNode || inst.OwnerID <= 0 {
				continue
			}
			existing := instancesByOwner[inst.OwnerID]
			if existing == nil || inst.ID > existing.ID {
				instancesByOwner[inst.OwnerID] = inst
			}
		}
	}

	for _, node := range nodes {
		if node == nil || strings.TrimSpace(node.AnalysisNodeID) == "" {
			continue
		}

		status := strings.TrimSpace(strings.ToLower(node.Status))
		hasContainer := instancesByOwner[int64(node.ID)] != nil
		targetStatus, shouldReset := dagruntime.ResumeNodeStatusForRestart(status, hasContainer)
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

func (o *dagOrchestrator) renewAnalysisRunningLease(analysisID int64, stop <-chan struct{}) {
	if analysisID <= 0 || o.repo == nil {
		return
	}
	ticker := time.NewTicker(analysisRunHeartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			err := o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
				"updated_at": time.Now().UTC(),
			})
			if err != nil {
				logger.Warnf(context.Background(), "[DagOrchestrator] renew analysis running lease failed, analysis_id=%d err=%v", analysisID, err)
			}
		}
	}
}

func (o *dagOrchestrator) cleanupPolicyOnNodeFailed() string {
	if o.cfg != nil && o.cfg.Container != nil {
		return normalizeCleanupPolicy(o.cfg.Container.DagNodeCleanupOnFailed, cleanupPolicyStop)
	}
	return cleanupPolicyStop
}

func (o *dagOrchestrator) cleanupPolicyOnDagFinished() string {
	if o.cfg != nil && o.cfg.Container != nil {
		return normalizeCleanupPolicy(o.cfg.Container.DagNodeCleanupOnDagFinished, cleanupPolicyDelete)
	}
	return cleanupPolicyDelete
}

func (o *dagOrchestrator) deleteContainerOnNodeSuccess() bool {
	if o.cfg == nil || o.cfg.Container == nil {
		return false
	}
	return o.cfg.Container.DeleteContainerOnNodeSuccess
}

func normalizeCleanupPolicy(value string, defaultValue string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case cleanupPolicyNone, cleanupPolicyStop, cleanupPolicyDelete:
		return strings.TrimSpace(strings.ToLower(value))
	default:
		return defaultValue
	}
}

func (o *dagOrchestrator) cleanupByAnalysisID(ctx context.Context, analysisID int64, policy string) {
	if err := o.cleanupByAnalysisIDStrict(ctx, analysisID, policy); err != nil {
		logger.Warnf(ctx, "[DagOrchestrator] cleanup by analysis_id failed, analysis_id=%d err=%v", analysisID, err)
	}
}

func (o *dagOrchestrator) cleanupByAnalysisIDStrict(ctx context.Context, analysisID int64, policy string) error {
	if policy == cleanupPolicyNone || o.containerRepo == nil || o.containerMgr == nil || analysisID <= 0 {
		return nil
	}

	nodes, err := o.repo.ListAnalysisNodesByAnalysisID(ctx, analysisID)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return nil
	}

	ownerSet := make(map[int64]struct{}, len(nodes))
	ownerIDs := make([]int64, 0, len(nodes))
	for _, node := range nodes {
		if node != nil && node.ID > 0 {
			ownerID := int64(node.ID)
			ownerSet[ownerID] = struct{}{}
			ownerIDs = append(ownerIDs, ownerID)
		}
	}

	instances, err := o.containerRepo.ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx, types.ContainerOwnerDagNode, ownerIDs)
	if err != nil {
		return err
	}

	var firstErr error

	for _, inst := range instances {
		if inst == nil || inst.OwnerType != types.ContainerOwnerDagNode {
			continue
		}
		if _, ok := ownerSet[inst.OwnerID]; !ok {
			continue
		}
		if err := o.cleanupContainerInstance(ctx, inst, policy); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}

func (o *dagOrchestrator) cleanupDagNodeContainer(ctx context.Context, node *types.AnalysisNode, policy string) {
	if policy == cleanupPolicyNone || o.containerRepo == nil || o.containerMgr == nil || node == nil || node.ID == 0 {
		return
	}

	instances, err := o.containerRepo.ListContainerInstanceByOwnerTypeAndOwnerIDs(ctx, types.ContainerOwnerDagNode, []int64{int64(node.ID)})
	if err != nil {
		logger.Warnf(ctx, "[DagOrchestrator] list container instances for node cleanup failed, node_id=%s err=%v", node.NodeID, err)
		return
	}

	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if inst.OwnerType == types.ContainerOwnerDagNode && inst.OwnerID == int64(node.ID) {
			_ = o.cleanupContainerInstance(ctx, inst, policy)
		}
	}
}

func (o *dagOrchestrator) cleanupContainerInstance(ctx context.Context, inst *types.ContainerInstance, policy string) error {
	if inst == nil || o.containerMgr == nil {
		return nil
	}

	var err error
	switch policy {
	case cleanupPolicyDelete:
		err = o.containerMgr.Delete(ctx, inst.ID)
	case cleanupPolicyStop:
		err = o.containerMgr.Stop(ctx, inst.ID)
	default:
		return nil
	}

	if err != nil {
		logger.Warnf(ctx, "[DagOrchestrator] dag node container cleanup failed, policy=%s instance_id=%d runtime_id=%s err=%v", policy, inst.ID, inst.RuntimeID, err)
		return err
	}
	return nil
}
