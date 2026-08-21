package service

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gobravedev/gobrave/internal/config"
	dagruntime "github.com/gobravedev/gobrave/internal/dag"
	"github.com/gobravedev/gobrave/internal/errors"
	"github.com/gobravedev/gobrave/internal/event"
	"github.com/gobravedev/gobrave/internal/logger"
	"github.com/gobravedev/gobrave/internal/manager"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
)

type nodeOrchestrator struct {
	repo             interfaces.AnalysisRepository
	workflowRepo     interfaces.WorkflowRepository
	containerMgr     *manager.ContainerManager
	containerService interfaces.ContainerService
	dispatcher       *dagruntime.NodeDispatcher
	bus              event.Bus
	projectRepo      interfaces.ProjectRepository
	cfg              *config.Config
}

func NewNodeOrchestrator(
	repo interfaces.AnalysisRepository,
	workflowRepo interfaces.WorkflowRepository,
	containerMgr *manager.ContainerManager,
	projectRepo interfaces.ProjectRepository,
	dispatcher *dagruntime.NodeDispatcher,
	containerService interfaces.ContainerService,
	bus event.Bus,
	cfg *config.Config,
) interfaces.NodeOrchestrator {
	return &nodeOrchestrator{
		repo:             repo,
		workflowRepo:     workflowRepo,
		containerMgr:     containerMgr,
		containerService: containerService,
		dispatcher:       dispatcher,
		projectRepo:      projectRepo,
		bus:              bus,
		cfg:              cfg,
	}
}

// StartAsync submits one existing analysis_node to NodeDispatcher directly.
// This is a bootstrap path that intentionally does not require analysis DAG scheduler wiring.
func (o *nodeOrchestrator) StartAsync(ctx context.Context, analysisNodeID int64) error {
	if analysisNodeID <= 0 {
		return fmt.Errorf("analysis node id is required")
	}

	if err := o.containerService.DeleteContainerInstancesByOwnerTypeAndOwnerIDs(
		ctx,
		types.ContainerOwnerDagNode,
		[]int64{analysisNodeID},
	); err != nil {
		return errors.NewInternalServerError("failed to cleanup previous container instances before submit").WithDetails(err.Error())
	}

	node, err := o.repo.GetAnalysisNodeByID(ctx, analysisNodeID)

	// 运行前删除文件夹ouput下内容 如果路径存在就删除 如果不存在就不删除
	// outputDir := node.OutputDir
	// if outputDir != "" {
	// 	project, err := o.projectRepo.GetProjectByID(ctx, node.ProjectID)
	// 	if err != nil {
	// 		logger.Warnf(ctx, "[NodeOrchestrator] failed to get project by id=%d, err=%v", node.ProjectID, err)
	// 		return err
	// 	}
	// 	prefix := filepath.Join(o.cfg.Storage.BaseDir, "data", project.ProjectID)
	// 	//判断是否以prefix开头 如果不是就不删除
	// 	if !strings.HasPrefix(outputDir, prefix) {
	// 		logger.Warnf(ctx, "[NodeOrchestrator] output dir=%s is not under project data dir=%s, skip delete", outputDir, prefix)
	// 		return fmt.Errorf("output dir is not under project data dir")
	// 	}
	// 	// 删除outputDir下的所有内容，，直接判断文件夹是否存在，如果存在就删除，如果不存在就不删除
	// 	if _, err := os.Stat(outputDir); err == nil {
	// 		// 不删除outputDir本身，只删除里面的内容
	// 		files, err := os.ReadDir(outputDir)
	// 		if err != nil {
	// 			logger.Warnf(ctx, "[NodeOrchestrator] failed to read output dir=%s, err=%v", outputDir, err)
	// 			return err
	// 		}
	// 		for _, file := range files {
	// 			filePath := filepath.Join(outputDir, file.Name())
	// 			if err := os.RemoveAll(filePath); err != nil {
	// 				logger.Warnf(ctx, "[NodeOrchestrator] failed to delete file=%s in output dir=%s, err=%v", filePath, outputDir, err)
	// 				return err
	// 			}
	// 		}
	// 	}

	// }

	if err != nil {
		return err
	}
	if node == nil || node.ID == 0 {
		return fmt.Errorf("analysis node not found")
	}

	status := strings.ToLower(strings.TrimSpace(node.Status))
	shouldPublishSubmitted := false
	switch status {
	case dagruntime.StatusRunning, dagruntime.StatusDone, dagruntime.StatusFailed, dagruntime.StatusSkipped, dagruntime.StatusCached:
		return nil
	case dagruntime.StatusSubmitted:
		// keep status as submitted
		shouldPublishSubmitted = true
	case dagruntime.StatusReady:
		if err := o.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, map[string]any{
			"status":     dagruntime.StatusSubmitted,
			"updated_at": time.Now().UTC(),
		}); err != nil {
			return err
		}
		shouldPublishSubmitted = true
	default:
		return fmt.Errorf("analysis node status must be ready/submitted, current=%s", node.Status)
	}

	if shouldPublishSubmitted && o.bus != nil {
		o.bus.Publish(dagruntime.RuntimeEvent{
			Name:           dagruntime.EventNodeSubmitted,
			AnalysisID:     node.AnalysisID,
			AnalysisNodeID: node.ID,
			NodeID:         node.NodeID,
			OccurredAt:     time.Now().UTC(),
			Payload: map[string]any{
				"status":           dagruntime.StatusSubmitted,
				"analysis_node_id": node.AnalysisNodeID,
			},
		})
	}

	// runtime := dagruntime.NewRuntimeEngine(o.repo)
	// preparer := dagruntime.NoopNodeRuntimePreparer{}
	// dispatcher := dagruntime.NewNodeDispatcher(
	// 	runtime,
	// 	o.repo,
	// 	o.bus,
	// 	executor.NewFactory(executor.FactoryDeps{
	// 		WorkflowRepository: o.workflowRepo,
	// 		ContainerManager:   o.containerMgr,
	// 	}),
	// 	nil,
	// 	preparer,
	// )

	go func(nodeID int64, runtimeNodeID string) {
		if dispatchErr := o.dispatcher.Dispatch(context.Background(), nodeID); dispatchErr != nil {
			logger.Warnf(context.Background(), "[NodeOrchestrator] dispatch failed, analysis_node_db_id=%d node_id=%s err=%v", analysisNodeID, runtimeNodeID, dispatchErr)
		}
	}(node.ID, node.NodeID)

	return nil
}

// StopAsync requests stop for one analysis node.
// It first marks node status as stopping, then delegates executor/container stop.
// Final status transition to stopped is completed by NodeCompletionCoordinator.
func (o *nodeOrchestrator) StopAsync(ctx context.Context, analysisNodeID int64) error {
	if analysisNodeID <= 0 {
		return fmt.Errorf("analysis node id is required")
	}

	node, err := o.repo.GetAnalysisNodeByID(ctx, analysisNodeID)
	if err != nil {
		return err
	}
	if node == nil || node.ID == 0 {
		return fmt.Errorf("analysis node not found")
	}

	status := strings.ToLower(strings.TrimSpace(node.Status))
	if status == dagruntime.StatusStopping {
		return nil
	}
	if status == dagruntime.StatusStopped || status == dagruntime.StatusDone || status == dagruntime.StatusFailed || status == dagruntime.StatusSkipped || status == dagruntime.StatusCached {
		return nil
	}
	if status != dagruntime.StatusReady && status != dagruntime.StatusSubmitted && status != dagruntime.StatusRunning {
		return fmt.Errorf("analysis node status must be ready/submitted/running, current=%s", node.Status)
	}

	values := map[string]any{
		"status":        dagruntime.StatusStopping,
		"server_status": "stopping",
		"error_message": "node stop requested by user",
		"updated_at":    time.Now().UTC(),
	}
	if err := o.repo.UpdateAnalysisNodeByAnalysisNodeID(ctx, node.AnalysisNodeID, values); err != nil {
		return err
	}
	// if o.bus != nil {
	// 	o.bus.Publish(dagruntime.RuntimeEvent{
	// 		Name:           dagruntime.EventNodeStateChange,
	// 		AnalysisID:     node.AnalysisID,
	// 		AnalysisNodeID: node.ID,
	// 		NodeID:         node.NodeID,
	// 		OccurredAt:     time.Now().UTC(),
	// 		Payload: map[string]any{
	// 			"status":           dagruntime.StatusStopping,
	// 			"analysis_node_id": node.AnalysisNodeID,
	// 		},
	// 	})
	// }

	// runtime := dagruntime.NewRuntimeEngine(o.repo)
	// preparer := dagruntime.NoopNodeRuntimePreparer{}
	// dispatcher := dagruntime.NewNodeDispatcher(
	// 	runtime,
	// 	o.repo,
	// 	o.bus,
	// 	executor.NewFactory(executor.FactoryDeps{
	// 		WorkflowRepository: o.workflowRepo,
	// 		ContainerManager:   o.containerMgr,
	// 	}),
	// 	nil,
	// 	preparer,
	// )

	if _, execErr := o.dispatcher.Stop(ctx, node); execErr != nil {
		_ = o.repo.UpdateAnalysisNodeByAnalysisNodeID(context.Background(), node.AnalysisNodeID, map[string]any{
			"status":        dagruntime.StatusFailed,
			"server_status": "stopped",
			"error_message": fmt.Sprintf("node stop failed: %v", execErr),
			"finished_at":   time.Now().UTC(),
			"updated_at":    time.Now().UTC(),
		})
		return execErr
	}

	return nil
}
