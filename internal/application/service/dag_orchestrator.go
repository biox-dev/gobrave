package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gobravedev/gobrave/internal/config"
	dagruntime "github.com/gobravedev/gobrave/internal/dag"
	"github.com/gobravedev/gobrave/internal/event"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/manager"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
)

const (
	cleanupPolicyNone      = "none"
	cleanupPolicyStop      = "stop"
	cleanupPolicyDelete    = "delete"
	analysisRunLeaseTTL    = 90 * time.Second
	analysisRunHeartbeat   = 15 * time.Second
	analysisStatusRunning  = "running"
	analysisStatusStopping = "stopping"
	analysisStatusStopped  = "stopped"
	analysisStatusFinished = "finished"
	analysisStatusFailed   = "failed"
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
	registry   *dagruntime.RunningRegistry
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
		registry: dagruntime.NewRunningRegistry(),
	}
	return o
}

func (o *dagOrchestrator) StartAsync(ctx context.Context, analysisID int64) error {
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
			"job_status": analysisStatusFailed,
			"updated_at": time.Now().UTC(),
		})
		return fmt.Errorf("cleanup dag node containers before start failed: %w", err)
	}

	if err := o.prepareNodesForResume(ctx, analysisID); err != nil {
		_ = o.repo.UpdateAnalysisByID(context.Background(), analysisID, map[string]any{
			"job_status": analysisStatusFailed,
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
		Status:         analysisStatusRunning,
		Cancel:         runCancel,
	})

	go func() {
		defer close(heartbeatStop)
		result, err := scheduler.Run(runCtx)
		stoppedByUser := o.registry != nil && o.registry.IsStopping(analysisID)

		if err != nil {
			finalStatus := analysisStatusFailed
			if stoppedByUser {
				_ = o.markActiveNodesStopped(context.Background(), analysisID, "dag stopped by user")
				if cleanupErr := o.cleanupByAnalysisIDStrict(context.Background(), analysisID, cleanupPolicyDelete); cleanupErr == nil {
					finalStatus = analysisStatusStopped
				} else {
					logger.Warnf(context.Background(), "[DagOrchestrator] stop cleanup failed after scheduler error, analysis_id=%s err=%v", analysisID, cleanupErr)
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

		finalStatus := analysisStatusFinished
		if stoppedByUser {
			_ = o.markActiveNodesStopped(context.Background(), analysisID, "dag stopped by user")
			if cleanupErr := o.cleanupByAnalysisIDStrict(context.Background(), analysisID, cleanupPolicyDelete); cleanupErr != nil {
				finalStatus = analysisStatusFailed
				logger.Warnf(context.Background(), "[DagOrchestrator] stop cleanup failed, analysis_id=%s err=%v", analysisID, cleanupErr)
			} else {
				finalStatus = analysisStatusStopped
			}
		} else {
			if result == nil || result.Snapshot == nil {
				finalStatus = analysisStatusFailed
			} else if failedCount := result.Snapshot.StatusCount[dagruntime.StatusFailed]; failedCount > 0 {
				finalStatus = analysisStatusFailed
			}
			if finalStatus == analysisStatusFinished {
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
	if current == analysisStatusStopped {
		return nil
	}

	if err := o.repo.UpdateAnalysisByID(ctx, analysisID, map[string]any{
		"job_status": analysisStatusStopping,
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

func (o *dagOrchestrator) RecoverRunningAnalyses(ctx context.Context) (int, error) {
	if o == nil || o.repo == nil {
		return 0, nil
	}

	items, err := o.repo.ListAnalysisByJobStatus(ctx, "running")
	if err != nil {
		return 0, err
	}

	recovered := 0
	for _, item := range items {
		if item == nil || item.ID <= 0 {
			continue
		}
		wasRunning := o.registry != nil && o.registry.IsRunning(item.ID)
		if err := o.StartAsync(ctx, item.ID); err != nil {
			logger.Warnf(ctx, "[DagOrchestrator] recover running analysis failed, analysis_id=%d err=%v", item.ID, err)
			continue
		}
		isRunning := o.registry != nil && o.registry.IsRunning(item.ID)
		if !wasRunning && isRunning {
			recovered++
		}
	}

	stoppingItems, err := o.repo.ListAnalysisByJobStatus(ctx, analysisStatusStopping)
	if err != nil {
		return recovered, err
	}
	for _, item := range stoppingItems {
		if item == nil || item.ID <= 0 {
			continue
		}
		if err := o.StopAsync(ctx, item.ID); err != nil {
			logger.Warnf(ctx, "[DagOrchestrator] recover stopping analysis failed, analysis_id=%d err=%v", item.ID, err)
		}
	}

	return recovered, nil
}

func (o *dagOrchestrator) finalizeStop(analysisID int64) {
	ctx := context.Background()
	finalStatus := analysisStatusStopped
	if err := o.markActiveNodesStopped(ctx, analysisID, "dag stopped by user"); err != nil {
		finalStatus = analysisStatusFailed
		logger.Warnf(ctx, "[DagOrchestrator] mark nodes stopped failed, analysis_id=%d err=%v", analysisID, err)
	}
	if err := o.cleanupByAnalysisIDStrict(ctx, analysisID, cleanupPolicyDelete); err != nil {
		finalStatus = analysisStatusFailed
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
		targetStatus, shouldReset := resumeNodeStatusForRestart(status, hasContainer)
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

func resumeNodeStatusForRestart(status string, hasContainer bool) (string, bool) {
	status = strings.TrimSpace(strings.ToLower(status))
	switch status {
	case dagruntime.StatusSubmitted:
		return dagruntime.StatusReady, true
	case dagruntime.StatusRunning:
		if hasContainer {
			return "", false
		}
		return dagruntime.StatusReady, true
	default:
		return "", false
	}
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
