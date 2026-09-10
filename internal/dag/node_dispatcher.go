package dag

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/biox-dev/gobrave/internal/dag/executor"
	"github.com/biox-dev/gobrave/internal/dag/prepare"
	"github.com/biox-dev/gobrave/internal/event"
	"github.com/biox-dev/gobrave/internal/logger"
	"github.com/biox-dev/gobrave/internal/types"
	"github.com/biox-dev/gobrave/internal/types/interfaces"
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
	repo interfaces.AnalysisRepository,
	bus event.Bus,
	factory *executor.ExecuterFactory,
	preparer prepare.NodeRuntimePreparer,
	// cleanup NodeFailureCleanupFunc,
) *NodeDispatcher {
	runtime := NewRuntimeEngine(repo)

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

	// Record the digests of the artifacts that are about to run. Execution-time
	// preparation is the only place where the authoritative run.sh / params.json
	// pair is produced (schedulers explicitly skip output cleanup when they probe),
	// so it is also the only place where the digests can be captured without
	// preparing the node a second time. Missing digests would only cost a rerun.
	d.persistArtifactFingerprint(ctx, node)

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

// persistArtifactFingerprint stores the md5 digests of the artifacts a node is
// about to execute with, giving the scheduler-side cache policies (cache_type 3/4)
// a baseline to compare against on the next run.
//
// Failures are logged instead of returned: the digests only drive cache reuse, so
// a missing baseline degrades to "run the node again" rather than to a wrong result.
func (d *NodeDispatcher) persistArtifactFingerprint(ctx context.Context, node *types.AnalysisNode) {
	commandMD5, err := fileMD5Hex(node.CommandPath)
	if err != nil {
		logger.Warnf(ctx, "[NodeDispatcher] hash run script failed, node_id=%s path=%s err=%v", node.NodeID, node.CommandPath, err)
		return
	}
	paramsMD5, err := fileMD5Hex(node.ParamsPath)
	if err != nil {
		logger.Warnf(ctx, "[NodeDispatcher] hash params payload failed, node_id=%s path=%s err=%v", node.NodeID, node.ParamsPath, err)
		return
	}
	if err := d.repo.UpdateAnalysisNodeByID(ctx, node.ID, map[string]any{
		"command_md5": commandMD5,
		"params_md5":  paramsMD5,
	}); err != nil {
		logger.Warnf(ctx, "[NodeDispatcher] persist artifact digests failed, node_id=%s err=%v", node.NodeID, err)
		return
	}
	node.CommandMD5 = commandMD5
	node.ParamsMD5 = paramsMD5
}

// fileMD5Hex streams a file through MD5 and returns the lowercase hex digest.
func fileMD5Hex(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("file path is empty")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hasher := md5.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
