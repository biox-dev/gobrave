package dag

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/gobravedev/gobrave/internal/config"
	"github.com/gobravedev/gobrave/internal/dag/executor"
	"github.com/gobravedev/gobrave/internal/dag/prepare"
	"github.com/gobravedev/gobrave/internal/event"
	"github.com/gobravedev/gobrave/internal/types"
	"github.com/gobravedev/gobrave/internal/types/interfaces"
)

type NodeFailureCleanupFunc func(ctx context.Context, node *types.AnalysisNode)

type NodeDispatcher struct {
	runtime  *RuntimeEngine
	repo     interfaces.AnalysisRepository
	bus      event.Bus
	factory  *executor.ExecuterFactory
	cleanup  NodeFailureCleanupFunc
	preparer prepare.NodeRuntimePreparer
}

func NewNodeDispatcher(
	// runtime *RuntimeEngine,
	repo interfaces.AnalysisRepository,
	bus event.Bus,
	factory *executor.ExecuterFactory,
	workflowRepo interfaces.WorkflowRepository,
	projectRepo interfaces.ProjectRepository,
	workflowService interfaces.WorkflowService,
	cfg *config.Config,
	runScriptBuilders map[string]prepare.RunScriptBuilder,
	// cleanup NodeFailureCleanupFunc,
	// preparer NodeRuntimePreparer,
) *NodeDispatcher {
	// if preparer == nil {
	// 	preparer = NoopNodeRuntimePreparer{}
	// }
	storageBase := strings.TrimSpace(cfg.Storage.BaseDir)
	runtime := NewRuntimeEngine(repo)
	preparer := prepare.NewFileSystemNodeRuntimePreparerWithBuilders(
		repo,
		workflowRepo,
		projectRepo,
		workflowService,
		storageBase,
		runScriptBuilders)

	return &NodeDispatcher{runtime: runtime, repo: repo, bus: bus, factory: factory,
		cleanup: nil, preparer: preparer}
}

func (d *NodeDispatcher) Dispatch(ctx context.Context, analysisNodeID int64) error {
	node, err := d.repo.GetAnalysisNodeByID(ctx, analysisNodeID)
	if err != nil {
		return err
	}
	analysisID := node.AnalysisID
	if err := d.preparer.Prepare(ctx, node); err != nil {
		_, _ = d.runtime.CompleteNode(ctx, node.ID, StatusFailed, nil, 1, fmt.Sprintf("prepare runtime failed: %v", err))
		d.runCleanup(ctx, node)
		d.publish(RuntimeEvent{
			Name:           EventNodeFailed,
			AnalysisID:     analysisID,
			AnalysisNodeID: node.ID,
			NodeID:         node.NodeID,
			OccurredAt:     time.Now().UTC(),
			Payload: map[string]any{
				"error": fmt.Sprintf("prepare runtime failed: %v", err),
			},
		})
		return err
	}

	node, err = d.runtime.MarkNodeRunning(ctx, node.ID)
	if err != nil {
		return err
	}
	d.publish(RuntimeEvent{
		Name:           EventNodeRunning,
		AnalysisID:     analysisID,
		AnalysisNodeID: node.ID,
		NodeID:         node.NodeID,
		OccurredAt:     time.Now().UTC(),
	})
	// TODO 这里都走到docker了，真正的runtime在 CreateByTemplate
	ex := d.factory.Resolve(node.Executor)
	result, execErr := ex.Execute(ctx, node)
	if execErr != nil {
		_, _ = d.runtime.CompleteNode(ctx, node.ID, StatusFailed, nil, 1, execErr.Error())
		d.runCleanup(ctx, node)
		d.publish(RuntimeEvent{
			Name:           EventNodeFailed,
			AnalysisID:     analysisID,
			AnalysisNodeID: node.ID,
			NodeID:         node.NodeID,
			OccurredAt:     time.Now().UTC(),
			Payload: map[string]any{
				"error": execErr.Error(),
			},
		})
		return execErr
	}

	if result == nil {
		result = &executor.Result{Status: StatusDone, ExitCode: 0}
	}
	if result.Deferred {
		// d.publish(RuntimeEvent{
		// 	Name:           EventNodeStateChange,
		// 	AnalysisID:     analysisID,
		// 	AnalysisNodeID: node.ID,
		// 	NodeID:         node.NodeID,
		// 	OccurredAt:     time.Now().UTC(),
		// 	Payload: map[string]any{
		// 		"status":   StatusRunning,
		// 		"deferred": true,
		// 	},
		// })
		return nil
	}
	status := result.Status
	if status == "" {
		status = StatusDone
	}
	_, err = d.runtime.CompleteNode(
		ctx,
		node.ID,
		status,
		result.ResolvedOutputs,
		result.ExitCode,
		result.ErrorMessage,
	)
	if err != nil {
		return fmt.Errorf("complete node failed: %w", err)
	}

	eventName := EventNodeCompleted
	if status == StatusFailed {
		eventName = EventNodeFailed
		d.runCleanup(ctx, node)
	}
	d.publish(RuntimeEvent{
		Name:           eventName,
		AnalysisID:     analysisID,
		AnalysisNodeID: node.ID,
		NodeID:         node.NodeID,
		OccurredAt:     time.Now().UTC(),
		Payload: map[string]any{
			"status": status,
		},
	})
	return nil
}

func (d *NodeDispatcher) Stop(ctx context.Context, node *types.AnalysisNode) (*executor.Result, error) {
	if node == nil {
		return nil, fmt.Errorf("analysis node is required")
	}
	if d.factory == nil {
		return nil, fmt.Errorf("executor factory is required")
	}
	stopCtx := executor.WithAction(ctx, executor.ActionStop)
	ex := d.factory.Resolve(node.Executor)
	return ex.Execute(stopCtx, node)
}

func (d *NodeDispatcher) publish(evt RuntimeEvent) {
	if d.bus == nil {
		return
	}
	d.bus.Publish(evt)
}

func (d *NodeDispatcher) runCleanup(ctx context.Context, node *types.AnalysisNode) {
	if d.cleanup == nil || node == nil {
		return
	}
	d.cleanup(ctx, node)
}
